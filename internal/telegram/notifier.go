package telegram

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/config"
	"github.com/jymchng/ccresu/internal/storage"
)

// Notifier defaults.
const (
	// DefaultSendInterval is how often the sender drains the queue even with no
	// wake-up, so a notification persisted before a crash is still delivered.
	DefaultSendInterval = 30 * time.Second
	// DefaultMaxSendAttempts bounds delivery attempts per notification.
	DefaultMaxSendAttempts = 5
	// DefaultBatchSize bounds one drain pass.
	DefaultBatchSize = 20
	// maxStoredError bounds the failure text written back to the database.
	maxStoredError = 500
)

// Store is the persistence the notifier needs. *storage.Store satisfies it.
//
// Delivery state lives in SQLite rather than in the queue, so a restart resumes
// exactly where it stopped and a notification can never be delivered twice: the
// dedupe key is enforced by the database.
type Store interface {
	EnqueueNotification(ctx context.Context, notification storage.Notification) (storage.Notification, bool, error)
	ListPendingNotifications(ctx context.Context, limit int) ([]storage.Notification, error)
	MarkNotificationSent(ctx context.Context, id int64, sentAt time.Time) error
	MarkNotificationFailed(ctx context.Context, id int64, message string, status storage.NotificationStatus) error
}

// Sender delivers one HTML message. *Client satisfies it.
type Sender interface {
	SendMessage(ctx context.Context, text string) (Message, error)
}

// Config assembles a Notifier.
type Config struct {
	// Store persists the queue. Required.
	Store Store
	// Sender delivers messages. Required.
	Sender Sender
	// Clock supplies timestamps and drives the drain ticker. Nil means the
	// system clock.
	Clock clock.Clock
	// Logger receives delivery diagnostics. Nil means slog.Default().
	Logger *slog.Logger
	// Enabled switches each event on or off. A missing event is off.
	Enabled map[storage.NotificationEvent]bool
	// MaxAttempts bounds delivery attempts per notification. Zero means
	// DefaultMaxSendAttempts.
	MaxAttempts int
	// Interval is the drain period. Zero means DefaultSendInterval.
	Interval time.Duration
	// BatchSize bounds one drain pass. Zero means DefaultBatchSize.
	BatchSize int
}

// Notifier turns recovery events into persisted, deduplicated messages and
// delivers them without ever standing in the recovery path.
//
// Two properties matter more than throughput:
//
//   - Notify never blocks on the network. It renders, persists and signals; the
//     only remote call happens on the sender goroutine started by Run.
//   - A notification that cannot be delivered is never lost silently. It stays
//     pending, is retried until its attempt budget is spent, and is marked
//     failed with the reason on disk.
type Notifier struct {
	store       Store
	sender      Sender
	clock       clock.Clock
	log         *slog.Logger
	enabled     map[storage.NotificationEvent]bool
	maxAttempts int
	interval    time.Duration
	batch       int

	// wake is a one-slot signal channel. Sending on it is non-blocking, so a
	// full slot can never hold up a recovery; the pending row is still on disk
	// and the next tick picks it up.
	wake chan struct{}
}

// New validates cfg and builds a Notifier.
func New(cfg Config) (*Notifier, error) {
	if cfg.Store == nil {
		return nil, fmt.Errorf("%w: notification store is required", ErrInvalidConfig)
	}
	if cfg.Sender == nil {
		return nil, fmt.Errorf("%w: message sender is required", ErrInvalidConfig)
	}

	clk := cfg.Clock
	if clk == nil {
		clk = clock.System()
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxSendAttempts
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = DefaultSendInterval
	}
	batch := cfg.BatchSize
	if batch <= 0 {
		batch = DefaultBatchSize
	}

	enabled := make(map[storage.NotificationEvent]bool, len(cfg.Enabled))
	for event, on := range cfg.Enabled {
		enabled[event] = on
	}

	return &Notifier{
		store:       cfg.Store,
		sender:      cfg.Sender,
		clock:       clk,
		log:         logger,
		enabled:     enabled,
		maxAttempts: maxAttempts,
		interval:    interval,
		batch:       batch,
		wake:        make(chan struct{}, 1),
	}, nil
}

// NewFromConfig builds a Notifier from the application configuration.
//
// It returns (nil, nil) when notifications are switched off, so a disabled
// setup costs nothing and callers only need a nil check.
func NewFromConfig(cfg *config.Config, store Store, clk clock.Clock, logger *slog.Logger) (*Notifier, error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil config", ErrInvalidConfig)
	}
	if !cfg.Telegram.Enabled {
		return nil, nil
	}
	client, err := NewClient(Options{
		Token:     cfg.Telegram.BotToken,
		ChatID:    cfg.Telegram.ChatID,
		ParseMode: cfg.Telegram.ParseMode,
		Clock:     clk,
		Logger:    logger,
	})
	if err != nil {
		return nil, err
	}
	return New(Config{
		Store:   store,
		Sender:  client,
		Clock:   clk,
		Logger:  logger,
		Enabled: EnabledFromConfig(cfg.Telegram),
	})
}

// EnabledFromConfig maps the configuration switches onto the event set.
func EnabledFromConfig(cfg config.Telegram) map[storage.NotificationEvent]bool {
	return map[storage.NotificationEvent]bool{
		storage.EventLimitDetected:    cfg.NotifyLimitDetected,
		storage.EventWaiting:          cfg.NotifyWaiting,
		storage.EventRecoveryStarted:  cfg.NotifyRecoveryStarted,
		storage.EventRecoverySuccess:  cfg.NotifyRecoverySuccess,
		storage.EventRecoveryFailure:  cfg.NotifyRecoveryFailure,
		storage.EventPermanentFailure: cfg.NotifyPermanentFailure,
		storage.EventSessionCompleted: cfg.NotifySessionCompleted,
	}
}

// Enabled reports whether event will be delivered.
func (n *Notifier) Enabled(event storage.NotificationEvent) bool {
	if n == nil || n.enabled == nil {
		return false
	}
	return n.enabled[event]
}

// MaxAttempts reports the per-notification delivery budget.
func (n *Notifier) MaxAttempts() int { return n.maxAttempts }

// Request describes one notification to queue.
type Request struct {
	// Event is the notification kind. Required.
	Event storage.NotificationEvent
	// SessionID is the session the message is about.
	SessionID string
	// AttemptID links the notification to a recovery attempt, when known.
	AttemptID int64
	// Data supplies the template values.
	Data EventData
	// DedupeKey overrides the computed identity. Leave empty normally.
	DedupeKey string
}

// Notify renders, persists and signals one notification.
//
// It returns created=false when an identical notification already exists, which
// is how deduplication makes a repeated discovery harmless.
//
// The only blocking work is a local SQLite insert; no network call happens
// here. A caller on the recovery path must still treat an error as a log line
// and carry on: a notification problem is never a recovery problem.
func (n *Notifier) Notify(ctx context.Context, req Request) (storage.Notification, bool, error) {
	if ctx == nil {
		return storage.Notification{}, false, fmt.Errorf("%w: nil context", ErrInvalidConfig)
	}
	if !n.Enabled(req.Event) {
		return storage.Notification{}, false, fmt.Errorf("%w: %s", ErrDisabled, req.Event)
	}

	data := req.Data
	if strings.TrimSpace(data.SessionID) == "" {
		data.SessionID = req.SessionID
	}
	body, renderErr := Render(req.Event, data)
	if renderErr != nil {
		return storage.Notification{}, false, renderErr
	}

	key := strings.TrimSpace(req.DedupeKey)
	if key == "" {
		key = DedupeKey(req.Event, data)
	}

	stored, created, enqueueErr := n.store.EnqueueNotification(ctx, storage.Notification{
		SessionID: req.SessionID,
		AttemptID: req.AttemptID,
		Event:     req.Event,
		DedupeKey: key,
		Body:      body,
		Status:    storage.NotificationPending,
	})
	if enqueueErr != nil {
		n.log.Warn("queue telegram notification",
			"event", string(req.Event), "session", req.SessionID, "error", enqueueErr)
		return storage.Notification{}, false, enqueueErr
	}
	if created {
		n.signal()
	}
	return stored, created, nil
}

// signal wakes the sender without ever blocking.
func (n *Notifier) signal() {
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

// Result summarises one drain pass.
type Result struct {
	// Sent counts messages the Bot API accepted.
	Sent int
	// RetryPending counts failures that stay queued for another attempt.
	RetryPending int
	// Failed counts notifications that spent their attempt budget or failed
	// permanently.
	Failed int
}

// SendOnce sends every pending notification in one bounded pass.
//
// An error is returned only when the queue itself could not be read or the
// context ended; individual delivery failures are summarised in Result and
// recorded on the notification row, so one bad chat id cannot stop the rest.
func (n *Notifier) SendOnce(ctx context.Context) (Result, error) {
	var result Result
	if ctx == nil {
		return result, fmt.Errorf("%w: nil context", ErrInvalidConfig)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}

	pending, err := n.store.ListPendingNotifications(ctx, n.batch)
	if err != nil {
		n.log.Warn("list pending notifications", "error", err)
		return result, err
	}

	for _, notification := range pending {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}

		_, sendErr := n.sender.SendMessage(ctx, notification.Body)
		if sendErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}
			n.recordFailure(ctx, notification, sendErr, &result)
			continue
		}
		if markErr := n.store.MarkNotificationSent(ctx, notification.ID, n.clock.Now().UTC()); markErr != nil {
			// The message did go out, so it is not counted as sent again; the
			// row stays pending and the next pass would resend. Report it.
			n.log.Warn("mark notification sent",
				"id", notification.ID, "event", string(notification.Event), "error", markErr)
			continue
		}
		result.Sent++
	}
	return result, nil
}

// recordFailure classifies a failed delivery and records it.
func (n *Notifier) recordFailure(ctx context.Context, notification storage.Notification,
	cause error, result *Result) {
	attempts := notification.Attempts + 1
	status := storage.NotificationPending
	if attempts >= n.maxAttempts || !retryable(cause) {
		status = storage.NotificationFailed
		result.Failed++
	} else {
		result.RetryPending++
	}

	if err := n.store.MarkNotificationFailed(ctx, notification.ID, truncate(cause.Error()), status); err != nil {
		n.log.Warn("record notification failure",
			"id", notification.ID, "event", string(notification.Event), "error", err)
	}
	n.log.Warn("telegram delivery failed",
		"event", string(notification.Event),
		"session", notification.SessionID,
		"attempts", attempts,
		"status", string(status),
		"error", cause.Error())
}

// Run drains the queue until ctx ends.
//
// It sends once immediately so a backlog left by a previous process is flushed
// on startup, then waits for either a wake-up signal or the drain tick.
func (n *Notifier) Run(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrInvalidConfig)
	}
	ticker := n.clock.NewTimer(n.interval)
	defer ticker.Stop()

	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if _, err := n.SendOnce(ctx); err != nil && ctx.Err() == nil {
			n.log.Warn("notification pass failed", "error", err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-n.wake:
		case <-ticker.C():
		}
		ticker.Reset(n.interval)
	}
}

// truncate caps a stored error so one enormous CLI dump cannot bloat the row.
func truncate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxStoredError {
		return s
	}
	return s[:maxStoredError] + "…(truncated)"
}
