package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
	"github.com/jymchng/ccresu/internal/telegram"
)

// queuedEvents returns the events the harness notifier recorded, in order.
func queuedEvents(h *harness) []storage.NotificationEvent {
	events := make([]storage.NotificationEvent, 0)
	for _, req := range h.notifier.queued() {
		events = append(events, req.Event)
	}
	return events
}

// countEvent reports how many times event was queued.
func countEvent(h *harness, event storage.NotificationEvent) int {
	count := 0
	for _, queued := range queuedEvents(h) {
		if queued == event {
			count++
		}
	}
	return count
}

// readyToResume seeds a session the recovery engine may resume and a provider
// that can do it, with the transcript growing on resume so verification proves
// the continuation.
func readyToResume(t *testing.T, h *harness) {
	t.Helper()
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateResetTimeReached
		s.TranscriptSize = 10
		s.TranscriptLines = 1
	})
	resumed := claude.Session{
		ID:          canonicalSessionID,
		ProjectPath: "/work/project",
		Recoverable: true,
	}
	h.provider.SetSession(resumed)
	h.provider.Statuses[canonicalSessionID] = claude.SessionStatus{
		Session:    resumed,
		Transcript: claude.TranscriptInfo{Path: "/tmp/t.jsonl", Size: 400, Lines: 9},
	}
	h.provider.ResumeResults[canonicalSessionID] = claude.ResumeResult{
		RequestedSessionID: canonicalSessionID,
		SessionID:          canonicalSessionID,
		Verified:           true,
		Result:             "continued",
		Subtype:            "success",
	}
}

// growOnResume makes the fake provider's transcript grow when the resume is
// issued, which is what verification reads as proof of continuation.
func growOnResume(h *harness) {
	h.provider.OnResume = func(context.Context, string, string) (claude.ResumeResult, error) {
		h.provider.Statuses[canonicalSessionID] = claude.SessionStatus{
			Session:    claude.Session{ID: canonicalSessionID, Recoverable: true},
			Transcript: claude.TranscriptInfo{Path: "/tmp/t.jsonl", Size: 4096, Lines: 42},
		}
		return h.provider.ResumeResults[canonicalSessionID], nil
	}
}

func TestRecoverQueuesStartedThenSuccess(t *testing.T) {
	h := newHarness(t)
	readyToResume(t, h)
	growOnResume(h)

	code, _, stderr := h.exec(t, "recover", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %s)", code, stderr)
	}

	events := queuedEvents(h)
	if len(events) != 2 {
		t.Fatalf("queued = %v, want exactly [recovery_started recovery_success]", events)
	}
	if events[0] != storage.EventRecoveryStarted || events[1] != storage.EventRecoverySuccess {
		t.Errorf("queued = %v, want [recovery_started recovery_success]", events)
	}
	if got := countEvent(h, storage.EventRecoveryStarted); got != 1 {
		t.Errorf("recovery_started queued %d times, want 1", got)
	}
	if got := countEvent(h, storage.EventRecoverySuccess); got != 1 {
		t.Errorf("recovery_success queued %d times, want 1", got)
	}

	// The message has to be able to say which attempt succeeded, so the attempt
	// row and the budget are carried through rather than left to be guessed.
	for _, req := range h.notifier.queued() {
		if req.Event != storage.EventRecoverySuccess {
			continue
		}
		if req.AttemptID == 0 {
			t.Error("recovery_success carried no attempt id, so its dedupe key cannot identify the attempt")
		}
		if req.Data.Attempt != 1 || req.Data.MaxAttempts != 3 {
			t.Errorf("attempt = %d/%d, want 1/3", req.Data.Attempt, req.Data.MaxAttempts)
		}
		if req.Data.State != string(storage.StateResumed) {
			t.Errorf("state = %q, want %q", req.Data.State, storage.StateResumed)
		}
	}
}

func TestRetryableFailureQueuesRecoveryFailure(t *testing.T) {
	h := newHarness(t)
	readyToResume(t, h)
	h.provider.OnResume = func(context.Context, string, string) (claude.ResumeResult, error) {
		return claude.ResumeResult{}, errors.New("connection reset by peer")
	}

	if code, _, _ := h.exec(t, "recover", canonicalSessionID); code == ExitOK {
		t.Fatal("recover of a failed attempt reported success")
	}

	if got := countEvent(h, storage.EventRecoveryFailure); got != 1 {
		t.Errorf("recovery_failure queued %d times, want 1 (%v)", got, queuedEvents(h))
	}
	if got := countEvent(h, storage.EventPermanentFailure); got != 0 {
		t.Errorf("permanent_failure queued %d times for a retryable failure, want 0", got)
	}
	// The retry delay is the point of the message: the session is waiting and
	// ccresu will try again by itself.
	for _, req := range h.notifier.queued() {
		if req.Event == storage.EventRecoveryFailure && req.Data.Delay <= 0 {
			t.Errorf("recovery_failure delay = %s, want the backoff it will observe", req.Data.Delay)
		}
	}
}

func TestPermanentFailureQueuesPermanentFailureOnly(t *testing.T) {
	h := newHarness(t)
	readyToResume(t, h)
	h.provider.OnResume = func(context.Context, string, string) (claude.ResumeResult, error) {
		return claude.ResumeResult{}, claude.ErrCLIUnavailable
	}

	h.exec(t, "recover", canonicalSessionID)

	if got := countEvent(h, storage.EventPermanentFailure); got != 1 {
		t.Errorf("permanent_failure queued %d times, want 1 (%v)", got, queuedEvents(h))
	}
	// A permanent failure is not a retryable one: announcing both would tell the
	// operator ccresu is still working on it while it has stopped.
	if got := countEvent(h, storage.EventRecoveryFailure); got != 0 {
		t.Errorf("recovery_failure queued %d times for a permanent failure, want 0", got)
	}
}

// TestSpentAttemptBudgetQueuesNothing proves the negative the goal calls out: a
// spent budget returns the session to DISCOVERED and starts a fresh cycle, so it
// is a pause rather than a verdict and must not raise "automatic recovery
// stopped".
func TestSpentAttemptBudgetQueuesNothing(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateRetryWait
	})
	ctx := context.Background()
	// Spend the whole budget (the harness manager allows three) without going
	// through the engine, so the request below is refused before it starts.
	for i := 0; i < 3; i++ {
		if _, err := h.store.StartAttempt(ctx, canonicalSessionID, 0, "claude -p --resume <id>"); err != nil {
			t.Fatalf("seed attempt %d: %v", i+1, err)
		}
	}

	h.exec(t, "recover", canonicalSessionID)

	if events := queuedEvents(h); len(events) != 0 {
		t.Errorf("queued = %v, want nothing: a spent budget announces no failure", events)
	}
	stored, err := h.store.GetSession(ctx, canonicalSessionID)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if stored.State != storage.StateDiscovered {
		t.Errorf("state = %s, want %s", stored.State, storage.StateDiscovered)
	}
}

// TestSessionCompletedIsQueuedOncePerSession proves the fifth producer and the
// dedupe key that makes it once per session, using the real notifier over the
// in-memory store rather than the recorder: the guarantee is the database's
// ON CONFLICT, so a fake that only counts calls cannot demonstrate it.
func TestSessionCompletedIsQueuedOncePerSession(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateCompleted
	})

	notifier, err := telegram.New(telegram.Config{
		Store:  h.store,
		Sender: &recordingSender{},
		Clock:  h.clock,
		Enabled: map[storage.NotificationEvent]bool{
			storage.EventSessionCompleted: true,
		},
	})
	if err != nil {
		t.Fatalf("notifier: %v", err)
	}
	h.rt.Notifier = notifier

	ctx := context.Background()
	daemon := NewDaemon(h.rt, time.Minute)
	// Two passes: the second must find the row already queued and add nothing.
	for pass := 0; pass < 2; pass++ {
		if err := daemon.Step(ctx); err != nil {
			t.Fatalf("pass %d: %v", pass+1, err)
		}
	}

	stored, err := h.store.ListNotificationsForSession(ctx, canonicalSessionID, 0)
	if err != nil {
		t.Fatalf("list notifications: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("notifications = %d, want 1 after two passes", len(stored))
	}
	if stored[0].Event != storage.EventSessionCompleted {
		t.Errorf("event = %q, want %q", stored[0].Event, storage.EventSessionCompleted)
	}
	if stored[0].DedupeKey != "session_completed:"+canonicalSessionID {
		t.Errorf("dedupe key = %q", stored[0].DedupeKey)
	}
	if stored[0].SessionID != canonicalSessionID {
		t.Errorf("session = %q, want %q", stored[0].SessionID, canonicalSessionID)
	}
}

// TestNotificationFailureDoesNotFailTheRecovery proves the queue is off the
// recovery path: a notifier that cannot queue anything must not turn a proven
// resume into a failure.
func TestNotificationFailureDoesNotFailTheRecovery(t *testing.T) {
	h := newHarness(t)
	readyToResume(t, h)
	growOnResume(h)
	h.notifier.notifyErr = errors.New("telegram is unreachable")

	code, stdout, stderr := h.exec(t, "recover", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d: a broken notifier must not fail a resume (stderr %s)",
			code, ExitOK, stderr)
	}
	if !strings.Contains(stdout, "RESUMED") {
		t.Errorf("stdout = %q, want the session resumed", stdout)
	}
}

// recordingSender is a telegram.Sender that accepts everything, so a delivery
// pass in a test never reaches the network.
type recordingSender struct {
	bodies []string
}

// SendMessage implements telegram.Sender.
func (s *recordingSender) SendMessage(_ context.Context, text string) (telegram.Message, error) {
	s.bodies = append(s.bodies, text)
	return telegram.Message{MessageID: int64(len(s.bodies)), ChatID: -1001234567890}, nil
}
