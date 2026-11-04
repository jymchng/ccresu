package telegram

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/config"
	"github.com/jymchng/ccresu/internal/storage"
)

// fakeStore is an in-memory Store with injectable failures.
type fakeStore struct {
	mu         sync.Mutex
	rows       map[string]*storage.Notification
	order      []string
	nextID     int64
	enqueueErr error
	listErr    error
	sentErr    error
	failedErr  error
}

func newFakeStore() *fakeStore {
	return &fakeStore{rows: make(map[string]*storage.Notification)}
}

func (s *fakeStore) EnqueueNotification(_ context.Context, notification storage.Notification) (storage.Notification, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enqueueErr != nil {
		return storage.Notification{}, false, s.enqueueErr
	}
	stored, created := s.enqueueLocked(notification)
	return stored, created, nil
}

// enqueueLocked inserts a row. The caller must hold s.mu.
func (s *fakeStore) enqueueLocked(notification storage.Notification) (storage.Notification, bool) {
	if existing, ok := s.rows[notification.DedupeKey]; ok {
		return *existing, false
	}
	s.nextID++
	notification.ID = s.nextID
	stored := notification
	s.rows[notification.DedupeKey] = &stored
	s.order = append(s.order, notification.DedupeKey)
	return stored, true
}

func (s *fakeStore) ListPendingNotifications(_ context.Context, limit int) ([]storage.Notification, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listErr != nil {
		return nil, s.listErr
	}
	var pending []storage.Notification
	for _, key := range s.order {
		row := s.rows[key]
		if row.Status != storage.NotificationPending {
			continue
		}
		pending = append(pending, *row)
		if limit > 0 && len(pending) >= limit {
			break
		}
	}
	return pending, nil
}

func (s *fakeStore) MarkNotificationSent(_ context.Context, id int64, sentAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sentErr != nil {
		return s.sentErr
	}
	row := s.byIDLocked(id)
	if row == nil {
		return storage.ErrNotFound
	}
	row.Status = storage.NotificationSent
	row.SentAt = sentAt
	row.Attempts++
	return nil
}

func (s *fakeStore) MarkNotificationFailed(_ context.Context, id int64, message string, status storage.NotificationStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failedErr != nil {
		return s.failedErr
	}
	row := s.byIDLocked(id)
	if row == nil {
		return storage.ErrNotFound
	}
	row.Status = status
	row.LastError = message
	row.Attempts++
	return nil
}

func (s *fakeStore) byIDLocked(id int64) *storage.Notification {
	for _, row := range s.rows {
		if row.ID == id {
			return row
		}
	}
	return nil
}

func (s *fakeStore) row(key string) (storage.Notification, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[key]
	if !ok {
		return storage.Notification{}, false
	}
	return *row, true
}

func (s *fakeStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows)
}

// setAttempts pre-loads a delivery attempt count so the budget can be tested.
func (s *fakeStore) setAttempts(key string, attempts int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row, ok := s.rows[key]; ok {
		row.Attempts = attempts
	}
}

// fakeSender records messages and can fail on demand.
type fakeSender struct {
	mu       sync.Mutex
	messages []string
	err      error
	errFn    func(call int, text string) error
}

func (s *fakeSender) SendMessage(_ context.Context, text string) (Message, error) {
	s.mu.Lock()
	s.messages = append(s.messages, text)
	call := len(s.messages)
	errFn := s.errFn
	err := s.err
	s.mu.Unlock()

	if errFn != nil {
		return Message{}, errFn(call, text)
	}
	if err != nil {
		return Message{}, err
	}
	return Message{MessageID: int64(call), Text: text}, nil
}

func (s *fakeSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.messages)
}

func (s *fakeSender) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.messages) == 0 {
		return ""
	}
	return s.messages[len(s.messages)-1]
}

// allEvents switches every notification on.
func allEvents() map[storage.NotificationEvent]bool {
	enabled := make(map[storage.NotificationEvent]bool, len(Events()))
	for _, event := range Events() {
		enabled[event] = true
	}
	return enabled
}

// newTestNotifier builds a notifier with a silent logger and a one-minute tick.
func newTestNotifier(t *testing.T, store Store, sender Sender, mutate func(*Config)) *Notifier {
	t.Helper()
	cfg := Config{
		Store:    store,
		Sender:   sender,
		Clock:    clock.NewFake(time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)),
		Logger:   discardLogger(),
		Enabled:  allEvents(),
		Interval: time.Minute,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	notifier, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return notifier
}

// recoveryRequest is a typical queued notification.
func recoveryRequest(event storage.NotificationEvent) Request {
	return Request{
		Event:     event,
		SessionID: "11111111-1111-4111-8111-111111111111",
		AttemptID: 3,
		Data: EventData{
			SessionID:   "11111111-1111-4111-8111-111111111111",
			AttemptID:   3,
			Attempt:     2,
			MaxAttempts: 10,
			Reason:      "claude: claude executable unavailable",
		},
	}
}

func TestNewValidatesItsDependencies(t *testing.T) {
	if _, err := New(Config{Sender: &fakeSender{}}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("no store = %v, want ErrInvalidConfig", err)
	}
	if _, err := New(Config{Store: newFakeStore()}); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("no sender = %v, want ErrInvalidConfig", err)
	}
}

func TestNewAppliesDefaults(t *testing.T) {
	notifier, err := New(Config{Store: newFakeStore(), Sender: &fakeSender{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if notifier.MaxAttempts() != DefaultMaxSendAttempts {
		t.Errorf("MaxAttempts = %d, want %d", notifier.MaxAttempts(), DefaultMaxSendAttempts)
	}
	if notifier.interval != DefaultSendInterval {
		t.Errorf("interval = %s, want %s", notifier.interval, DefaultSendInterval)
	}
	if notifier.batch != DefaultBatchSize {
		t.Errorf("batch = %d, want %d", notifier.batch, DefaultBatchSize)
	}
	if notifier.clock == nil || notifier.log == nil {
		t.Error("clock and logger must always be set")
	}
	if notifier.Enabled(storage.EventLimitDetected) {
		t.Error("an event without a switch must be disabled")
	}
}

func TestNotifyQueuesOnceAndSignalsWithoutBlocking(t *testing.T) {
	store := newFakeStore()
	notifier := newTestNotifier(t, store, &fakeSender{}, nil)
	ctx := context.Background()

	stored, created, err := notifier.Notify(ctx, recoveryRequest(storage.EventRecoveryStarted))
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if !created {
		t.Fatal("created = false, want the first notification to be new")
	}
	if stored.Status != storage.NotificationPending {
		t.Errorf("status = %q, want pending", stored.Status)
	}
	if !strings.Contains(stored.Body, "Recovery started") {
		t.Errorf("body = %q, want the rendered template", stored.Body)
	}

	// The wake channel holds one token; three more notifications must not block
	// even though nothing is draining it.
	for i := 0; i < 3; i++ {
		if _, _, err := notifier.Notify(ctx, recoveryRequest(storage.EventRecoveryFailure)); err != nil {
			t.Fatalf("Notify: %v", err)
		}
	}

	again, created, err := notifier.Notify(ctx, recoveryRequest(storage.EventRecoveryStarted))
	if err != nil {
		t.Fatalf("Notify again: %v", err)
	}
	if created {
		t.Fatal("created = true, want the duplicate deduplicated")
	}
	if again.ID != stored.ID {
		t.Errorf("duplicate id = %d, want the original %d", again.ID, stored.ID)
	}
	if store.count() != 2 {
		t.Errorf("stored notifications = %d, want one started and one failure", store.count())
	}
}

func TestNotifyRespectsTheEventSwitches(t *testing.T) {
	store := newFakeStore()
	notifier := newTestNotifier(t, store, &fakeSender{}, func(cfg *Config) {
		cfg.Enabled = map[storage.NotificationEvent]bool{storage.EventRecoveryStarted: true}
	})

	if _, _, err := notifier.Notify(context.Background(), recoveryRequest(storage.EventLimitDetected)); !errors.Is(err, ErrDisabled) {
		t.Errorf("disabled event = %v, want ErrDisabled", err)
	}
	if store.count() != 0 {
		t.Error("a disabled event must not be persisted")
	}
	if !notifier.Enabled(storage.EventRecoveryStarted) {
		t.Error("the enabled event must report enabled")
	}
}

func TestNotifyReportsFailuresWithoutPanicking(t *testing.T) {
	store := newFakeStore()
	notifier := newTestNotifier(t, store, &fakeSender{}, nil)

	var nilCtx context.Context
	if _, _, err := notifier.Notify(nilCtx, recoveryRequest(storage.EventRecoveryStarted)); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("nil context = %v, want ErrInvalidConfig", err)
	}

	store.enqueueErr = errors.New("database is locked")
	if _, _, err := notifier.Notify(context.Background(), recoveryRequest(storage.EventRecoveryStarted)); err == nil {
		t.Error("Notify = nil error, want the persistence failure")
	}

	notifier.enabled[storage.NotificationEvent("invented")] = true
	if _, _, err := notifier.Notify(context.Background(), Request{Event: storage.NotificationEvent("invented")}); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("unknown event = %v, want ErrInvalidEvent", err)
	}
}

func TestSendOnceDeliversPendingNotifications(t *testing.T) {
	store := newFakeStore()
	sender := &fakeSender{}
	notifier := newTestNotifier(t, store, sender, nil)
	ctx := context.Background()

	if _, _, err := notifier.Notify(ctx, recoveryRequest(storage.EventRecoverySuccess)); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	result, err := notifier.SendOnce(ctx)
	if err != nil {
		t.Fatalf("SendOnce: %v", err)
	}
	if result.Sent != 1 || result.Failed != 0 || result.RetryPending != 0 {
		t.Errorf("result = %+v, want one sent", result)
	}
	if sender.count() != 1 {
		t.Fatalf("sender calls = %d, want 1", sender.count())
	}
	if !strings.Contains(sender.last(), "Recovery succeeded") {
		t.Errorf("sent body = %q, want the rendered template", sender.last())
	}

	row, ok := store.row(DedupeKey(storage.EventRecoverySuccess, EventData{
		SessionID: "11111111-1111-4111-8111-111111111111", AttemptID: 3,
	}))
	if !ok {
		t.Fatal("the notification is missing from the store")
	}
	if row.Status != storage.NotificationSent {
		t.Errorf("status = %q, want sent", row.Status)
	}
	if row.SentAt.IsZero() {
		t.Error("SentAt is zero, want the delivery time")
	}
	if row.LastError != "" {
		t.Errorf("LastError = %q, want it cleared", row.LastError)
	}

	// A second pass has nothing left to do.
	result, err = notifier.SendOnce(ctx)
	if err != nil {
		t.Fatalf("SendOnce: %v", err)
	}
	if result.Sent != 0 || sender.count() != 1 {
		t.Errorf("second pass = %+v with %d sends, want nothing", result, sender.count())
	}
}

func TestSendOnceKeepsTransientFailuresQueued(t *testing.T) {
	store := newFakeStore()
	sender := &fakeSender{err: &transportError{Method: "sendMessage", Cause: "connection reset"}}
	notifier := newTestNotifier(t, store, sender, func(cfg *Config) { cfg.MaxAttempts = 3 })
	ctx := context.Background()

	if _, _, err := notifier.Notify(ctx, recoveryRequest(storage.EventRecoverySuccess)); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	result, err := notifier.SendOnce(ctx)
	if err != nil {
		t.Fatalf("SendOnce: %v", err)
	}
	if result.RetryPending != 1 || result.Failed != 0 || result.Sent != 0 {
		t.Errorf("result = %+v, want the failure to stay pending", result)
	}

	row, _ := store.row(DedupeKey(storage.EventRecoverySuccess, EventData{
		SessionID: "11111111-1111-4111-8111-111111111111", AttemptID: 3,
	}))
	if row.Status != storage.NotificationPending {
		t.Errorf("status = %q, want it still pending", row.Status)
	}
	if row.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", row.Attempts)
	}
	if !strings.Contains(row.LastError, "connection reset") {
		t.Errorf("LastError = %q, want the failure recorded", row.LastError)
	}
}

func TestSendOnceFailsPermanentlyOnARejection(t *testing.T) {
	store := newFakeStore()
	sender := &fakeSender{err: &APIError{Method: "sendMessage", StatusCode: 400, Description: "chat not found"}}
	notifier := newTestNotifier(t, store, sender, nil)
	ctx := context.Background()

	key := DedupeKey(storage.EventLimitDetected, EventData{SessionID: "s1", LimitID: 4})
	if _, _, err := notifier.Notify(ctx, Request{
		Event: storage.EventLimitDetected, SessionID: "s1",
		Data: EventData{SessionID: "s1", LimitID: 4},
	}); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	result, err := notifier.SendOnce(ctx)
	if err != nil {
		t.Fatalf("SendOnce: %v", err)
	}
	if result.Failed != 1 || result.RetryPending != 0 {
		t.Errorf("result = %+v, want one permanent failure", result)
	}
	row, _ := store.row(key)
	if row.Status != storage.NotificationFailed {
		t.Errorf("status = %q, want failed", row.Status)
	}
}

func TestSendOnceFailsWhenTheAttemptBudgetIsSpent(t *testing.T) {
	store := newFakeStore()
	sender := &fakeSender{err: &transportError{Method: "sendMessage", Cause: "still down"}}
	notifier := newTestNotifier(t, store, sender, func(cfg *Config) { cfg.MaxAttempts = 2 })
	ctx := context.Background()

	key := DedupeKey(storage.EventRecoverySuccess, EventData{SessionID: "s1", AttemptID: 9})
	if _, _, err := notifier.Notify(ctx, Request{
		Event: storage.EventRecoverySuccess, SessionID: "s1", AttemptID: 9,
		Data: EventData{SessionID: "s1", AttemptID: 9},
	}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	store.setAttempts(key, 1) // one attempt already spent

	result, err := notifier.SendOnce(ctx)
	if err != nil {
		t.Fatalf("SendOnce: %v", err)
	}
	if result.Failed != 1 {
		t.Errorf("result = %+v, want the budget to be enforced", result)
	}
	row, _ := store.row(key)
	if row.Status != storage.NotificationFailed {
		t.Errorf("status = %q, want failed once the budget is spent", row.Status)
	}
}

func TestSendOnceReportsStoreProblems(t *testing.T) {
	ctx := context.Background()

	t.Run("list fails", func(t *testing.T) {
		store := newFakeStore()
		store.listErr = errors.New("database is locked")
		notifier := newTestNotifier(t, store, &fakeSender{}, nil)
		if _, err := notifier.SendOnce(ctx); err == nil {
			t.Fatal("SendOnce = nil error, want the listing failure")
		}
	})

	t.Run("mark sent fails", func(t *testing.T) {
		store := newFakeStore()
		store.sentErr = errors.New("write failed")
		notifier := newTestNotifier(t, store, &fakeSender{}, nil)
		if _, _, err := notifier.Notify(ctx, recoveryRequest(storage.EventRecoverySuccess)); err != nil {
			t.Fatalf("Notify: %v", err)
		}
		result, err := notifier.SendOnce(ctx)
		if err != nil {
			t.Fatalf("SendOnce: %v", err)
		}
		if result.Sent != 0 {
			t.Errorf("result = %+v, want the delivery not counted", result)
		}
	})

	t.Run("mark failed fails", func(t *testing.T) {
		store := newFakeStore()
		store.failedErr = errors.New("write failed")
		notifier := newTestNotifier(t, store, &fakeSender{err: errors.New("boom")}, nil)
		if _, _, err := notifier.Notify(ctx, recoveryRequest(storage.EventRecoveryFailure)); err != nil {
			t.Fatalf("Notify: %v", err)
		}
		if _, err := notifier.SendOnce(ctx); err != nil {
			t.Fatalf("SendOnce: %v", err)
		}
	})
}

func TestSendOnceValidatesItsContext(t *testing.T) {
	notifier := newTestNotifier(t, newFakeStore(), &fakeSender{}, nil)
	var nilCtx context.Context
	if _, err := notifier.SendOnce(nilCtx); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("nil context = %v, want ErrInvalidConfig", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := notifier.SendOnce(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled context = %v, want context.Canceled", err)
	}
}

func TestRunDrainsTheQueueAndStopsOnCancel(t *testing.T) {
	store := newFakeStore()
	sender := &fakeSender{}
	notifier := newTestNotifier(t, store, sender, nil)
	ctx, cancel := context.WithCancel(context.Background())

	if _, _, err := notifier.Notify(ctx, recoveryRequest(storage.EventRecoverySuccess)); err != nil {
		t.Fatalf("Notify: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- notifier.Run(ctx) }()

	waitFor(t, func() bool { return sender.count() == 1 })
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop when its context was cancelled")
	}
}

func TestRunTicksWithoutAWakeUp(t *testing.T) {
	store := newFakeStore()
	sender := &fakeSender{}
	clk := clock.NewFake(time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC))
	notifier := newTestNotifier(t, store, sender, func(cfg *Config) {
		cfg.Clock = clk
		cfg.Interval = time.Minute
	})
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- notifier.Run(ctx) }()

	// Queue a notification without signalling the sender, so only the ticker
	// can deliver it.
	store.mu.Lock()
	store.enqueueLocked(storage.Notification{
		Event:     storage.EventSessionCompleted,
		DedupeKey: "session_completed:s1",
		Body:      "done",
		Status:    storage.NotificationPending,
	})
	store.mu.Unlock()

	waitFor(t, func() bool { return clk.HasWaiters() })
	clk.Advance(time.Minute)
	waitFor(t, func() bool { return sender.count() == 1 })

	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop when its context was cancelled")
	}
}

func TestRunRejectsANilContext(t *testing.T) {
	notifier := newTestNotifier(t, newFakeStore(), &fakeSender{}, nil)
	var nilCtx context.Context
	if err := notifier.Run(nilCtx); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("Run(nil) = %v, want ErrInvalidConfig", err)
	}
}

func TestNewFromConfig(t *testing.T) {
	if _, err := NewFromConfig(nil, newFakeStore(), nil, discardLogger()); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("nil config = %v, want ErrInvalidConfig", err)
	}

	disabled := &config.Config{}
	notifier, err := NewFromConfig(disabled, newFakeStore(), nil, discardLogger())
	if err != nil {
		t.Fatalf("disabled: %v", err)
	}
	if notifier != nil {
		t.Error("a disabled configuration must not build a notifier")
	}

	incomplete := &config.Config{}
	incomplete.Telegram = config.Telegram{Enabled: true, ChatID: "42"}
	if _, err := NewFromConfig(incomplete, newFakeStore(), nil, discardLogger()); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("missing token = %v, want ErrInvalidConfig", err)
	}

	enabled := &config.Config{}
	enabled.Telegram = config.Telegram{
		Enabled:                true,
		BotToken:               testToken,
		ChatID:                 "42",
		ParseMode:              "HTML",
		NotifyLimitDetected:    true,
		NotifyRecoveryFailure:  true,
		NotifyPermanentFailure: true,
	}
	notifier, err = NewFromConfig(enabled, newFakeStore(), nil, discardLogger())
	if err != nil {
		t.Fatalf("enabled: %v", err)
	}
	if notifier == nil {
		t.Fatal("an enabled configuration must build a notifier")
	}
	if !notifier.Enabled(storage.EventLimitDetected) {
		t.Error("limit detection should be enabled")
	}
	if notifier.Enabled(storage.EventWaiting) {
		t.Error("waiting should be disabled")
	}
}

func TestEnabledFromConfig(t *testing.T) {
	cfg := config.Telegram{
		NotifyLimitDetected:    true,
		NotifyWaiting:          true,
		NotifyRecoveryStarted:  true,
		NotifyRecoverySuccess:  true,
		NotifyRecoveryFailure:  true,
		NotifyPermanentFailure: true,
		NotifySessionCompleted: true,
	}
	enabled := EnabledFromConfig(cfg)
	if len(enabled) != len(Events()) {
		t.Fatalf("EnabledFromConfig returned %d switches, want %d", len(enabled), len(Events()))
	}
	for _, event := range Events() {
		if !enabled[event] {
			t.Errorf("event %q should be enabled", event)
		}
	}
	if got := EnabledFromConfig(config.Telegram{}); len(got) != len(Events()) {
		t.Errorf("EnabledFromConfig returned %d switches, want one per event", len(got))
	}
}

func TestNotifierEnabledOnANilReceiver(t *testing.T) {
	var notifier *Notifier
	if notifier.Enabled(storage.EventLimitDetected) {
		t.Error("a nil notifier must report everything disabled")
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("  short  "); got != "short" {
		t.Errorf("truncate = %q, want it trimmed", got)
	}
	long := strings.Repeat("x", maxStoredError+50)
	got := truncate(long)
	if !strings.HasSuffix(got, "…(truncated)") {
		t.Error("truncate did not mark the truncation")
	}
	if len(got) > maxStoredError+32 {
		t.Errorf("truncate returned %d bytes, want it bounded", len(got))
	}
}

// waitFor blocks until condition holds, failing the test after two seconds.
func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met within two seconds")
}

// storeSeq keeps each in-memory database distinct.
var storeSeq atomic.Int64

// openTestStore opens a migrated, private in-memory store.
func openTestStore(t *testing.T) *storage.Store {
	t.Helper()
	base := time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC)
	store, err := storage.Open(context.Background(), storage.Options{
		Memory: true,
		Clock: func() time.Time {
			return base.Add(time.Duration(storeSeq.Add(1)) * time.Microsecond)
		},
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

// TestNotifierPersistsAcrossARestart proves the queue lives on disk, not in
// memory: a second notifier built on the same database sees the pending row,
// refuses to duplicate it, and delivers it.
func TestNotifierPersistsAcrossARestart(t *testing.T) {
	store := openTestStore(t)
	sender := &fakeSender{}
	ctx := context.Background()

	// notifications has foreign keys into sessions and recovery_attempts, so a
	// real store needs the parent rows before a row can be queued against them.
	request := recoveryRequest(storage.EventRecoveryStarted)
	if _, err := store.UpsertSession(ctx, storage.Session{
		ID:             request.SessionID,
		ProjectPath:    "/tmp/project",
		TranscriptPath: "/tmp/project/" + request.SessionID + ".jsonl",
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	attempt, err := store.StartAttempt(ctx, request.SessionID, 0,
		"claude -p --resume "+request.SessionID+" continue")
	if err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
	request.AttemptID = attempt.ID
	request.Data.AttemptID = attempt.ID

	first := newTestNotifier(t, store, sender, nil)
	if _, created, err := first.Notify(ctx, request); err != nil || !created {
		t.Fatalf("Notify = %v (created %v), want the notification queued", err, created)
	}

	// A new process: a new notifier on the same database, nothing in memory.
	restarted := newTestNotifier(t, store, sender, nil)
	if _, created, err := restarted.Notify(ctx, request); err != nil {
		t.Fatalf("Notify after restart: %v", err)
	} else if created {
		t.Fatal("created = true after restart, want the on-disk dedupe key to win")
	}

	result, err := restarted.SendOnce(ctx)
	if err != nil {
		t.Fatalf("SendOnce: %v", err)
	}
	if result.Sent != 1 || sender.count() != 1 {
		t.Fatalf("result = %+v with %d sends, want exactly one delivery", result, sender.count())
	}

	pending, err := store.ListPendingNotifications(ctx, 10)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("pending = %d, want none after delivery", len(pending))
	}
	row, err := store.GetNotificationByKey(ctx, DedupeKey(storage.EventRecoveryStarted, request.Data))
	if err != nil {
		t.Fatalf("get notification: %v", err)
	}
	if row.Status != storage.NotificationSent || row.SentAt.IsZero() {
		t.Errorf("row = %s sent_at %s, want a persisted delivery", row.Status, row.SentAt)
	}
}
