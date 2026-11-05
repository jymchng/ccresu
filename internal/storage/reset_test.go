package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jymchng/ccresu/internal/storage"
)

// seedSession inserts one session in the requested state.
func seedSession(t *testing.T, store *storage.Store, id string, state storage.State) {
	t.Helper()
	if _, err := store.UpsertSession(context.Background(), storage.Session{
		ID: id, ProjectPath: "/work", Type: storage.SessionTypeInteractive,
		State: state, Enabled: true, Recoverable: true,
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// TestResetFailedSessionIsTheOnlyLegalSource pins the store-side guard: the CLI
// checks the state too, but the store is the last place that can see what it is
// about to overwrite, so it must refuse the same states.
func TestResetFailedSessionIsTheOnlyLegalSource(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	forbidden := []storage.State{
		storage.StateDiscovered,
		storage.StateRunning,
		storage.StateWaitingForReset,
		storage.StateRetryWait,
		storage.StateResumed,
		storage.StateCompleted,
		storage.StateUserTerminated,
		storage.StateDisabled,
		storage.StateUnrecoverable,
	}
	for _, state := range forbidden {
		t.Run(string(state), func(t *testing.T) {
			id := "s-" + string(state)
			seedSession(t, store, id, state)
			if _, err := store.ResetFailedSession(ctx, id, "operator reset"); !errors.Is(err, storage.ErrInvalid) {
				t.Fatalf("ResetFailedSession(%s) error = %v, want ErrInvalid", state, err)
			}
			session, err := store.GetSession(ctx, id)
			if err != nil {
				t.Fatalf("get session: %v", err)
			}
			if session.State != state {
				t.Errorf("state = %s after a refused reset, want %s", session.State, state)
			}
			if transitions, err := store.ListTransitions(ctx, id, 0); err != nil {
				t.Fatalf("list transitions: %v", err)
			} else if len(transitions) != 0 {
				t.Errorf("transitions = %+v, want none: a refusal must not be recorded as one", transitions)
			}
		})
	}

	if _, err := store.ResetFailedSession(ctx, "99999999-9999-4999-8999-999999999999", "operator reset"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("unknown session error = %v, want ErrNotFound", err)
	}
}

// TestResetFailedSessionClearsTheVerdictOnly proves the write touches the
// verdict, the error and the retry instant, and nothing else: attempts are the
// record of what happened, so they survive.
func TestResetFailedSessionClearsTheVerdictOnly(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	const id = "reset-me"
	seedSession(t, store, id, storage.StatePermanentlyFailed)

	attempt, err := store.StartAttempt(ctx, id, 0, "claude -p --resume "+id)
	if err != nil {
		t.Fatalf("start attempt: %v", err)
	}
	if _, err := store.FinishAttempt(ctx, attempt.ID, storage.AttemptResult{
		State: storage.StatePermanentlyFailed, Error: "gave up",
	}); err != nil {
		t.Fatalf("finish attempt: %v", err)
	}
	if err := store.SetSessionError(ctx, id, "gave up"); err != nil {
		t.Fatalf("set session error: %v", err)
	}

	transition, err := store.ResetFailedSession(ctx, id, "operator reset a permanently failed session")
	if err != nil {
		t.Fatalf("ResetFailedSession: %v", err)
	}
	if transition.FromState != storage.StatePermanentlyFailed || transition.ToState != storage.StateDisabled {
		t.Errorf("transition = %s -> %s, want PERMANENTLY_FAILED -> DISABLED", transition.FromState, transition.ToState)
	}

	session, err := store.GetSession(ctx, id)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.State != storage.StateDisabled {
		t.Errorf("state = %s, want DISABLED", session.State)
	}
	if session.Enabled {
		t.Error("enabled = true, want false: a reset means ccresu stops trying")
	}
	if session.LastError != "" {
		t.Errorf("LastError = %q, want it cleared", session.LastError)
	}
	if session.RetryAt != nil {
		t.Errorf("RetryAt = %v, want nil", session.RetryAt)
	}

	count, err := store.CountAttempts(ctx, id)
	if err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if count != 1 {
		t.Errorf("attempts = %d, want the seeded 1 kept", count)
	}

	transitions, err := store.ListTransitions(ctx, id, 0)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	if len(transitions) != 1 {
		t.Fatalf("transitions = %+v, want exactly one", transitions)
	}
	if transitions[0].Reason != "operator reset a permanently failed session" {
		t.Errorf("reason = %q, want the human explanation", transitions[0].Reason)
	}
}
