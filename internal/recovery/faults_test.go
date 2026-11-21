package recovery

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
)

// errInjected is what a faultStore returns for the method it was told to break.
var errInjected = errors.New("injected storage failure")

// faultStore wraps a real store and fails one method, so the recovery engine's
// behaviour when persistence betrays it can be tested without a broken database.
//
// failAt is 1-based and names which call to that method fails; 0 fails every
// call. The distinction matters: most recovery paths call the same method more
// than once (one transition per state change), and "the third transition could
// not be written" is a different situation from "no transition can be written".
type faultStore struct {
	Store
	failOn string
	failAt int
	err    error
	calls  map[string]int
}

func newFaultStore(inner Store, failOn string, failAt int) *faultStore {
	return &faultStore{Store: inner, failOn: failOn, failAt: failAt, err: errInjected, calls: map[string]int{}}
}

// fail reports the injected error when this call is the one being broken.
func (f *faultStore) fail(method string) error {
	if f.failOn != method {
		return nil
	}
	f.calls[method]++
	if f.failAt == 0 || f.calls[method] == f.failAt {
		return f.err
	}
	return nil
}

// count reports how many times a method was attempted, including failed calls.
func (f *faultStore) count(method string) int { return f.calls[method] }

func (f *faultStore) GetSession(ctx context.Context, id string) (storage.Session, error) {
	if err := f.fail("GetSession"); err != nil {
		return storage.Session{}, err
	}
	return f.Store.GetSession(ctx, id)
}

func (f *faultStore) ListSessions(ctx context.Context, filter storage.SessionFilter) ([]storage.Session, error) {
	if err := f.fail("ListSessions"); err != nil {
		return nil, err
	}
	return f.Store.ListSessions(ctx, filter)
}

func (f *faultStore) TransitionSession(ctx context.Context, id string, to storage.State, reason string, attemptID int64) (storage.Transition, error) {
	if err := f.fail("TransitionSession"); err != nil {
		return storage.Transition{}, err
	}
	return f.Store.TransitionSession(ctx, id, to, reason, attemptID)
}

func (f *faultStore) RecordTransition(ctx context.Context, transition storage.Transition) error {
	if err := f.fail("RecordTransition"); err != nil {
		return err
	}
	return f.Store.RecordTransition(ctx, transition)
}

func (f *faultStore) SetSessionError(ctx context.Context, id, message string) error {
	if err := f.fail("SetSessionError"); err != nil {
		return err
	}
	return f.Store.SetSessionError(ctx, id, message)
}

func (f *faultStore) SetSessionTranscriptMetrics(ctx context.Context, id, path string, size, lines int64) error {
	if err := f.fail("SetSessionTranscriptMetrics"); err != nil {
		return err
	}
	return f.Store.SetSessionTranscriptMetrics(ctx, id, path, size, lines)
}

func (f *faultStore) StartAttempt(ctx context.Context, sessionID string, usageLimitID int64, command string) (storage.RecoveryAttempt, error) {
	if err := f.fail("StartAttempt"); err != nil {
		return storage.RecoveryAttempt{}, err
	}
	return f.Store.StartAttempt(ctx, sessionID, usageLimitID, command)
}

func (f *faultStore) FinishAttempt(ctx context.Context, attemptID int64, result storage.AttemptResult) (storage.RecoveryAttempt, error) {
	if err := f.fail("FinishAttempt"); err != nil {
		return storage.RecoveryAttempt{}, err
	}
	return f.Store.FinishAttempt(ctx, attemptID, result)
}

func (f *faultStore) CountAttempts(ctx context.Context, sessionID string) (int, error) {
	if err := f.fail("CountAttempts"); err != nil {
		return 0, err
	}
	return f.Store.CountAttempts(ctx, sessionID)
}

func (f *faultStore) LatestAttempt(ctx context.Context, sessionID string) (storage.RecoveryAttempt, error) {
	if err := f.fail("LatestAttempt"); err != nil {
		return storage.RecoveryAttempt{}, err
	}
	return f.Store.LatestAttempt(ctx, sessionID)
}

func (f *faultStore) ListAttempts(ctx context.Context, sessionID string, limit int) ([]storage.RecoveryAttempt, error) {
	if err := f.fail("ListAttempts"); err != nil {
		return nil, err
	}
	return f.Store.ListAttempts(ctx, sessionID, limit)
}

func (f *faultStore) ListTransitions(ctx context.Context, sessionID string, limit int) ([]storage.Transition, error) {
	if err := f.fail("ListTransitions"); err != nil {
		return nil, err
	}
	return f.Store.ListTransitions(ctx, sessionID, limit)
}

// runningGrowthProvider reports the baseline first and a grown transcript on
// every later call, with a live process: the shape of a session that carried on
// by itself after the reset.
func runningGrowthProvider(before, after int64) *providerStub {
	return &providerStub{statusFn: func(call int, sessionID string) (claude.SessionStatus, error) {
		status := statusFor(sessionID, before, before*20)
		if call > 1 {
			status = statusFor(sessionID, after, after*20)
		}
		status.Running = true
		return status, nil
	}}
}

// TestRecoverRefusesToPretendAStorageFailureSucceeded is the core guarantee of
// this file: when the database rejects a write, Recover reports it. It must
// never report RESUMED, and it must never swallow the error, because an attempt
// that happened but was not recorded is exactly the state that would allow a
// duplicate resume after a restart.
func TestRecoverRefusesToPretendAStorageFailureSucceeded(t *testing.T) {
	staleAttempt := func(t *testing.T, store *storage.Store) {
		t.Helper()
		if _, err := store.StartAttempt(context.Background(), sessionA, 0, "claude -p --resume"); err != nil {
			t.Fatalf("seed a running attempt: %v", err)
		}
	}
	budgetSpent := func(t *testing.T, store *storage.Store) {
		t.Helper()
		attempt, err := store.StartAttempt(context.Background(), sessionA, 0, "claude -p --resume")
		if err != nil {
			t.Fatalf("seed attempt: %v", err)
		}
		if _, err := store.FinishAttempt(context.Background(), attempt.ID, storage.AttemptResult{
			State: StateDiscovered, Success: storage.BoolPtr(false), Error: "earlier failure",
			FinishedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("finish seeded attempt: %v", err)
		}
	}

	cases := []struct {
		name    string
		session storage.Session
		fault   [2]any // method, failAt
		prepare func(*testing.T, *storage.Store)
		mutate  func(*Config)
		resume  func(string, string) (claude.ResumeResult, error)
		wantErr string
		wantRes bool // the run must still have proven the resume
	}{
		{
			name:    "a disabled session cannot record its own decision",
			session: func() storage.Session { s := recoverableSession(sessionA); s.Enabled = false; return s }(),
			fault:   [2]any{"TransitionSession", 1},
			wantErr: "injected storage failure",
		},
		{
			name:    "an unrecoverable session cannot record why",
			session: func() storage.Session { s := recoverableSession(sessionA); s.Recoverable = false; return s }(),
			fault:   [2]any{"TransitionSession", 1},
			wantErr: "injected storage failure",
		},
		{
			name:    "the transition log cannot be read",
			session: recoverableSession(sessionA),
			fault:   [2]any{"ListTransitions", 0},
			wantErr: "injected storage failure",
		},
		{
			name:    "an exhausted budget cannot be recorded",
			session: func() storage.Session { s := recoverableSession(sessionA); s.State = StateWaitingForReset; return s }(),
			fault:   [2]any{"TransitionSession", 1},
			prepare: budgetSpent,
			mutate:  func(cfg *Config) { cfg.MaxAttempts = 1 },
			wantErr: "injected storage failure",
		},
		{
			name:    "the interrupted attempt cannot be read",
			session: func() storage.Session { s := recoverableSession(sessionA); s.State = StateWaitingForReset; return s }(),
			fault:   [2]any{"LatestAttempt", 0},
			prepare: staleAttempt,
			wantErr: "injected storage failure",
		},
		{
			// The pre-flight checks all run in the state the pass found the
			// session in, so the first transition a resume writes is RESUMING.
			name:    "the RESUMING transition cannot be written",
			session: recoverableSession(sessionA),
			fault:   [2]any{"TransitionSession", 1},
			wantErr: "injected storage failure",
		},
		{
			// RESUMING is spent immediately before the continuation is handed
			// over, so the RESUMED the resume is recorded as is the second
			// transition.
			name:    "the RESUMED transition cannot be written",
			session: recoverableSession(sessionA),
			fault:   [2]any{"TransitionSession", 2},
			wantErr: "injected storage failure",
		},
		{
			name:    "the attempt row cannot be started",
			session: recoverableSession(sessionA),
			fault:   [2]any{"StartAttempt", 0},
			wantErr: "record attempt",
		},
		{
			name:    "the successful attempt cannot be finished",
			session: recoverableSession(sessionA),
			fault:   [2]any{"FinishAttempt", 0},
			wantErr: "finish attempt",
		},
		{
			name:    "the retry wait cannot be written",
			session: recoverableSession(sessionA),
			fault:   [2]any{"TransitionSession", 2},
			resume: func(id, _ string) (claude.ResumeResult, error) {
				return claude.ResumeResult{}, errors.New("connection reset by peer")
			},
			wantErr: "injected storage failure",
		},
		{
			name:    "the permanent failure cannot be written",
			session: recoverableSession(sessionA),
			fault:   [2]any{"TransitionSession", 2},
			resume: func(string, string) (claude.ResumeResult, error) {
				return claude.ResumeResult{}, claude.ErrCLIUnavailable
			},
			wantErr: "injected storage failure",
		},
		// The three cases below must still succeed: those writes are advisory,
		// so a failure there is logged and the recovery continues.
		{
			name:    "transcript metrics after a proven resume are advisory",
			session: recoverableSession(sessionA),
			fault:   [2]any{"SetSessionTranscriptMetrics", 0},
			wantRes: true,
		},
		{
			name:    "clearing the stored error is advisory",
			session: recoverableSession(sessionA),
			fault:   [2]any{"SetSessionError", 0},
			wantRes: true,
		},
		{
			name:    "a failed attempt that cannot be finished still schedules a retry",
			session: recoverableSession(sessionA),
			fault:   [2]any{"FinishAttempt", 0},
			resume: func(string, string) (claude.ResumeResult, error) {
				return claude.ResumeResult{}, errors.New("connection reset by peer")
			},
			wantErr: "connection reset by peer",
		},
		{
			name:    "an interrupted attempt that cannot be closed",
			session: func() storage.Session { s := recoverableSession(sessionA); s.State = StateWaitingForReset; return s }(),
			fault:   [2]any{"FinishAttempt", 0},
			prepare: staleAttempt,
			wantErr: "close interrupted attempt",
		},
		{
			// The close moves the session through the walker, so the write that can fail is
			// TransitionSession — the one that persists the new state and its history
			// together — not the bare RecordTransition beside it.
			name:    "an interrupted attempt whose transition cannot be written",
			session: func() storage.Session { s := recoverableSession(sessionA); s.State = StateWaitingForReset; return s }(),
			fault:   [2]any{"TransitionSession", 0},
			prepare: staleAttempt,
			wantErr: "record interrupted attempt",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := newTestStore(t)
			seedSession(t, inner, tc.session)
			if tc.prepare != nil {
				tc.prepare(t, inner)
			}
			faulty := newFaultStore(inner, tc.fault[0].(string), tc.fault[1].(int))

			provider := growthProvider(10, 12)
			if tc.resume != nil {
				provider.resumeFn = tc.resume
			}
			manager := testManager(t, faulty, provider, tc.mutate)

			outcome, err := manager.Recover(context.Background(), sessionA)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Recover = %v, want the advisory failure to be tolerated", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Recover = %v, want an error mentioning %q", err, tc.wantErr)
			}
			if tc.wantRes && !outcome.Resumed {
				t.Errorf("Resumed = false, want true: the recovery itself succeeded")
			}
			if err != nil && outcome.State == StateResumed {
				t.Errorf("state = RESUMED after an error: a failure must never be reported as success")
			}
		})
	}
}

// TestRecoverExplainsWhyASessionIsUnrecoverable proves the fallback reasons are
// used when the storage row and the live status carry none, so the operator is
// never told only that something failed.
func TestRecoverExplainsWhyASessionIsUnrecoverable(t *testing.T) {
	store := newTestStore(t)
	session := recoverableSession(sessionA)
	session.Recoverable = false
	seedSession(t, store, session)

	outcome, err := testManager(t, store, &providerStub{}, nil).Recover(context.Background(), sessionA)
	if !errors.Is(err, ErrUnrecoverable) {
		t.Fatalf("Recover = %v, want ErrUnrecoverable", err)
	}
	transitions, listErr := store.ListTransitions(context.Background(), sessionA, 0)
	if listErr != nil {
		t.Fatalf("ListTransitions: %v", listErr)
	}
	if len(transitions) == 0 {
		t.Fatalf("no transitions recorded, want the UNRECOVERABLE entry carrying the fallback reason")
	}
	last := transitions[len(transitions)-1]
	if last.ToState != StateUnrecoverable {
		t.Errorf("last transition = %s, want UNRECOVERABLE", last.ToState)
	}
	if !strings.Contains(last.Reason, "no verified way to resume") {
		t.Errorf("reason = %q, want the documented fallback reason", last.Reason)
	}
	if outcome.State != StateUnrecoverable {
		t.Errorf("state = %s, want UNRECOVERABLE", outcome.State)
	}
}

// TestRecoverExplainsAStatusThatLostRecoverability covers the same fallback when
// the persisted session was recoverable but the live status is not.
func TestRecoverExplainsAStatusThatLostRecoverability(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	provider := &providerStub{statusFn: func(int, string) (claude.SessionStatus, error) {
		return claude.SessionStatus{Session: claude.Session{ID: sessionA, Recoverable: false}}, nil
	}}

	_, err := testManager(t, store, provider, nil).Recover(context.Background(), sessionA)
	if !errors.Is(err, ErrUnrecoverable) {
		t.Fatalf("Recover = %v, want ErrUnrecoverable", err)
	}
	if !strings.Contains(err.Error(), "no longer recoverable") {
		t.Errorf("error = %q, want the status fallback reason", err)
	}
	if provider.resumeCount() != 0 {
		t.Error("a session that is no longer recoverable must not be resumed")
	}
}

// TestRecoverSucceedsEvenIfTheMetricsWriteFails pins the advisory writes: the
// transcript metrics are a diagnostic, so the resume is issued and its verdict
// stands whether or not the row was refreshed.
func TestRecoverSucceedsEvenIfTheMetricsWriteFails(t *testing.T) {
	inner := newTestStore(t)
	seedSession(t, inner, recoverableSession(sessionA))
	faulty := newFaultStore(inner, "SetSessionTranscriptMetrics", 0)
	provider := growthProvider(10, 14)
	manager := testManager(t, faulty, provider, nil)

	outcome, err := manager.Recover(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("Recover = %v, want success despite the advisory write failing", err)
	}
	if !outcome.Resumed || outcome.State != StateResumed {
		t.Errorf("outcome = %+v, want RESUMED with Resumed=true", outcome)
	}
	if !outcome.Verification.Proven {
		t.Error("Proven = false, want true: the resume exited zero")
	}
	if provider.resumeCount() != 1 {
		t.Errorf("resume calls = %d, want 1: there is no shortcut to take, the command runs", provider.resumeCount())
	}
	if faulty.count("SetSessionTranscriptMetrics") == 0 {
		t.Error("the metrics write was never attempted, so this test proved nothing")
	}
}

// TestRecoverDoesNotResumeUntilResumingIsRecorded is the ordering guarantee: the
// RESUMING transition is written before the attempt row and the command, so a
// transition that cannot be persisted stops the pass instead of leaving a
// continuation whose state nobody recorded.
func TestRecoverDoesNotResumeUntilResumingIsRecorded(t *testing.T) {
	inner := newTestStore(t)
	seedSession(t, inner, recoverableSession(sessionA))
	// 1 = RESUMING: a WAITING_FOR_RESET session has no earlier transition.
	faulty := newFaultStore(inner, "TransitionSession", 1)
	provider := growthProvider(10, 14)
	manager := testManager(t, faulty, provider, nil)

	outcome, err := manager.Recover(context.Background(), sessionA)
	if err == nil || !strings.Contains(err.Error(), errInjected.Error()) {
		t.Fatalf("Recover = %v, want the injected storage failure", err)
	}
	if outcome.State == StateResumed {
		t.Errorf("outcome = %+v, want no RESUMED claim when the transition was not written", outcome)
	}
	if provider.resumeCount() != 0 {
		t.Errorf("resume calls = %d, want 0: nothing may run before the state says it is running",
			provider.resumeCount())
	}
}

// TestRecoverRefusesToClaimAWaitItCannotRecord covers the sibling case a step
// later: a refusal must end in WAITING_FOR_RESET, and when that transition
// cannot be written the caller must be told rather than be handed a state the
// database does not hold.
//
// The session is seeded in DISCOVERED on purpose: the park is then a real state
// change, written through TransitionSession, and the assertion below can tell a
// claimed wait apart from the state the session was already in.
func TestRecoverRefusesToClaimAWaitItCannotRecord(t *testing.T) {
	inner := newTestStore(t)
	discovered := recoverableSession(sessionA)
	discovered.State = StateDiscovered
	seedSession(t, inner, discovered)
	// The pass writes two transitions — DISCOVERED -> RESUMING, then the park
	// back to WAITING_FOR_RESET once the CLI refuses — so the second one is the
	// park, and that is the write this test breaks.
	faulty := newFaultStore(inner, "TransitionSession", 2)
	provider := &providerStub{
		resumeFn: func(string, string) (claude.ResumeResult, error) {
			return claude.ResumeResult{}, refusedResume()
		},
	}
	manager := testManager(t, faulty, provider, nil)

	outcome, err := manager.Recover(context.Background(), sessionA)
	if err == nil || !strings.Contains(err.Error(), errInjected.Error()) {
		t.Fatalf("Recover = %v, want the injected storage failure", err)
	}
	if errors.Is(err, ErrLimitNotCleared) {
		t.Error("error is ErrLimitNotCleared: the wait was never recorded, so reporting it would be a lie")
	}
	if outcome.State == StateWaitingForReset {
		t.Errorf("state = WAITING_FOR_RESET although the transition was not written")
	}
}

// TestPendingAndHistoryReportStoreFailures proves the read paths surface a
// database failure rather than returning an empty result that looks like "there
// is nothing to do".
func TestPendingAndHistoryReportStoreFailures(t *testing.T) {
	ctx := context.Background()

	pendingInner := newTestStore(t)
	seedSession(t, pendingInner, recoverableSession(sessionA))
	pending := testManager(t, newFaultStore(pendingInner, "ListSessions", 0), &providerStub{}, nil)
	if _, err := pending.Pending(ctx); !errors.Is(err, errInjected) {
		t.Errorf("Pending = %v, want the injected failure", err)
	}

	historyInner := newTestStore(t)
	seedSession(t, historyInner, recoverableSession(sessionA))
	attempts := testManager(t, newFaultStore(historyInner, "ListAttempts", 0), &providerStub{}, nil)
	if _, err := attempts.History(ctx, sessionA); !errors.Is(err, errInjected) {
		t.Errorf("History with a failing attempts read = %v, want the injected failure", err)
	}

	transitions := testManager(t, newFaultStore(historyInner, "ListTransitions", 0), &providerStub{}, nil)
	if _, err := transitions.History(ctx, sessionA); !errors.Is(err, errInjected) {
		t.Errorf("History with a failing transitions read = %v, want the injected failure", err)
	}

	guard := testManager(t, historyInner, &providerStub{}, nil)
	if _, err := guard.History(ctx, "   "); !errors.Is(err, ErrNoSession) {
		t.Errorf("History with a blank id = %v, want ErrNoSession", err)
	}
	if _, err := guard.History(ctx, sessionB); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("History for an unknown session = %v, want storage.ErrNotFound", err)
	}
}

// TestStateWalkerRefusesAnIllegalEdge proves the walker validates before it
// writes: an edge the machine does not define must be refused, and the session
// must be left exactly as it was.
func TestStateWalkerRefusesAnIllegalEdge(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, func() storage.Session {
		s := recoverableSession(sessionA)
		s.State = StateCompleted
		return s
	}())

	walker := &stateWalker{ctx: context.Background(), store: store, sessionID: sessionA, current: StateCompleted}
	err := walker.move(StateResuming, "not an edge", 0)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("move from COMPLETED to RESUMING = %v, want ErrInvalidTransition", err)
	}
	session, readErr := store.GetSession(context.Background(), sessionA)
	if readErr != nil {
		t.Fatalf("read session: %v", readErr)
	}
	if session.State != StateCompleted {
		t.Errorf("state = %s, want COMPLETED untouched", session.State)
	}
}

// TestStateWalkerReportsAPersistenceFailure proves an illegal edge is not the
// only failure the walker reports: a legal edge that the database rejects comes
// back as an error, and the walker's idea of the state does not move on.
func TestStateWalkerReportsAPersistenceFailure(t *testing.T) {
	inner := newTestStore(t)
	seedSession(t, inner, recoverableSession(sessionA))
	walker := &stateWalker{
		ctx: context.Background(), store: newFaultStore(inner, "TransitionSession", 0),
		sessionID: sessionA, current: StateWaitingForReset,
	}
	if err := walker.move(StateResuming, "resuming", 0); !errors.Is(err, errInjected) {
		t.Fatalf("move = %v, want the injected failure", err)
	}
	if walker.current != StateWaitingForReset {
		t.Errorf("walker state = %s, want it unchanged when the write failed", walker.current)
	}

	// Re-entering the current state is recorded, not skipped: "checked again and
	// still waiting" is information worth keeping.
	repeat := &stateWalker{ctx: context.Background(), store: newFaultStore(inner, "RecordTransition", 0),
		sessionID: sessionA, current: StateWaitingForReset}
	if err := repeat.move(StateWaitingForReset, "still waiting", 0); !errors.Is(err, errInjected) {
		t.Fatalf("repeated move = %v, want the injected failure", err)
	}
}

// TestClassifyTreatsAPathErrorAsTransient proves a filesystem error from the
// process layer is retried: the executable was there a moment ago, and a busy
// or briefly unavailable path is exactly what a retry is for.
func TestClassifyTreatsAPathErrorAsTransient(t *testing.T) {
	err := &fs.PathError{Op: "fork", Path: "/usr/local/bin/claude", Err: errors.New("resource temporarily unavailable")}
	if got := Classify(err); got != ClassRetryable {
		t.Errorf("Classify(%v) = %s, want RETRYABLE", err, got)
	}
	if !Retryable(err) {
		t.Error("Retryable(path error) = false, want true")
	}
}

// TestClassifyTreatsADeadlineAsTransient covers the other transient shape that
// never spells its name in the message: a wrapped context deadline. Treating a
// timeout as permanent would abandon a session that only needed longer.
func TestClassifyTreatsADeadlineAsTransient(t *testing.T) {
	err := fmt.Errorf("resume: %w", context.DeadlineExceeded)
	if got := Classify(err); got != ClassRetryable {
		t.Errorf("Classify(%v) = %s, want RETRYABLE", err, got)
	}
	if !Retryable(err) {
		t.Error("Retryable(deadline) = false, want true")
	}
}

// TestFaultStoreOnlyFailsTheRequestedCall is the self-check for the harness: if
// failAt were ignored, every case above would pass for the wrong reason.
func TestFaultStoreOnlyFailsTheRequestedCall(t *testing.T) {
	inner := newTestStore(t)
	seedSession(t, inner, recoverableSession(sessionA))
	faulty := newFaultStore(inner, "TransitionSession", 2)
	ctx := context.Background()

	if _, err := faulty.TransitionSession(ctx, sessionA, StateWaitingForReset, "first", 0); err != nil {
		t.Fatalf("first call = %v, want it to succeed", err)
	}
	if _, err := faulty.TransitionSession(ctx, sessionA, StateResuming, "second", 0); !errors.Is(err, errInjected) {
		t.Fatalf("second call = %v, want the injected failure", err)
	}
	if _, err := faulty.TransitionSession(ctx, sessionA, StateResumed, "third", 0); err != nil {
		t.Fatalf("third call = %v, want it to succeed again", err)
	}
	if got := faulty.count("TransitionSession"); got != 3 {
		t.Errorf("TransitionSession attempts = %d, want 3: every attempt is recorded so the injected failure lands on exactly one of them", got)
	}
	if err := faulty.fail("GetSession"); err != nil {
		t.Fatalf("an unrequested method returned %v", err)
	}
}

// TestFaultStoreDelegatesEverythingElse proves the harness is transparent: the
// wrapped store still answers normally, so a passing test cannot be an artefact
// of an empty double.
func TestFaultStoreDelegatesEverythingElse(t *testing.T) {
	inner := newTestStore(t)
	seeded := seedSession(t, inner, recoverableSession(sessionA))
	faulty := newFaultStore(inner, "CountAttempts", 0)
	ctx := context.Background()

	got, err := faulty.GetSession(ctx, sessionA)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.ID != seeded.ID || got.State != seeded.State {
		t.Errorf("GetSession = %+v, want the wrapped store's row", got)
	}
	if _, err := faulty.CountAttempts(ctx, sessionA); !errors.Is(err, errInjected) {
		t.Errorf("CountAttempts = %v, want the injected failure", err)
	}
	if _, err := faulty.ListSessions(ctx, storage.SessionFilter{}); err != nil {
		t.Errorf("ListSessions = %v, want it delegated", err)
	}
	if err := faulty.SetSessionError(ctx, sessionA, "note"); err != nil {
		t.Errorf("SetSessionError = %v, want it delegated", err)
	}
	if _, err := faulty.ListAttempts(ctx, sessionA, 0); err != nil {
		t.Errorf("ListAttempts = %v, want it delegated", err)
	}
	if _, err := faulty.ListTransitions(ctx, sessionA, 0); err != nil {
		t.Errorf("ListTransitions = %v, want it delegated", err)
	}
	if _, err := faulty.LatestAttempt(ctx, sessionA); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("LatestAttempt = %v, want the store's own not-found error", err)
	}
	if _, err := faulty.StartAttempt(ctx, sessionA, 0, "claude -p --resume"); err != nil {
		t.Errorf("StartAttempt = %v, want it delegated", err)
	}
	if err := faulty.SetSessionTranscriptMetrics(ctx, sessionA, "/t.jsonl", 10, 1); err != nil {
		t.Errorf("SetSessionTranscriptMetrics = %v, want it delegated", err)
	}
	if !strings.Contains(got.ID, sessionA) {
		t.Errorf("delegated row id = %q, want %q", got.ID, sessionA)
	}
}
