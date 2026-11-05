package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
)

// seedFailedSession puts a session in PERMANENTLY_FAILED with a stored error and
// one spent attempt, i.e. exactly the situation `ccresu reset` exists for.
func seedFailedSession(t *testing.T, h *harness, id string) storage.Session {
	t.Helper()
	ctx := context.Background()
	session := h.seedSession(t, id, func(s *storage.Session) {
		s.State = storage.StatePermanentlyFailed
		s.LastError = "claude executable unavailable: claude -p --resume failed (exit 1)"
	})
	attempt, err := h.store.StartAttempt(ctx, id, 0, "claude -p --resume "+id+" --output-format json <6-byte continuation message>")
	if err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
	if _, err := h.store.FinishAttempt(ctx, attempt.ID, storage.AttemptResult{
		State:  storage.StatePermanentlyFailed,
		Error:  session.LastError,
		Output: "the attempt gave up",
	}); err != nil {
		t.Fatalf("finish attempt: %v", err)
	}
	if err := h.store.SetSessionError(ctx, id, session.LastError); err != nil {
		t.Fatalf("set session error: %v", err)
	}
	return session
}

// TestResetClearsAPermanentlyFailedSession is the whole point of the command:
// the verdict becomes DISABLED, the error goes away, and the attempt history —
// including the counter — is left exactly as it was.
func TestResetClearsAPermanentlyFailedSession(t *testing.T) {
	h := newHarness(t)
	seedFailedSession(t, h, canonicalSessionID)
	ctx := context.Background()

	before, err := h.store.CountAttempts(ctx, canonicalSessionID)
	if err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if before == 0 {
		t.Fatal("the fixture has no attempts; the reset could not prove it keeps them")
	}

	code, stdout, stderr := h.exec(t, "reset", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("reset exit = %d, want %d (stderr %q)", code, ExitOK, stderr)
	}
	if !strings.Contains(stdout, string(storage.StateDisabled)) {
		t.Errorf("stdout = %q, want it to report the new state", stdout)
	}
	if !strings.Contains(stdout, "attempts") {
		t.Errorf("stdout = %q, want it to say the attempts were kept", stdout)
	}

	session, err := h.store.GetSession(ctx, canonicalSessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if session.State != storage.StateDisabled {
		t.Errorf("state = %s, want %s", session.State, storage.StateDisabled)
	}
	if session.Enabled {
		t.Error("the session is still enabled after reset: a reset means \"stop trying\"")
	}
	if session.LastError != "" {
		t.Errorf("LastError = %q, want it cleared", session.LastError)
	}

	after, err := h.store.CountAttempts(ctx, canonicalSessionID)
	if err != nil {
		t.Fatalf("count attempts after reset: %v", err)
	}
	if after != before {
		t.Errorf("attempts = %d after reset, want %d: the budget belongs to `ccresu retry`", after, before)
	}

	transition, err := h.store.ListTransitions(ctx, canonicalSessionID, 0)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	if len(transition) != 1 {
		t.Fatalf("transitions = %+v, want exactly the one reset row", transition)
	}
	if transition[0].FromState != storage.StatePermanentlyFailed || transition[0].ToState != storage.StateDisabled {
		t.Errorf("transition = %s -> %s, want %s -> %s",
			transition[0].FromState, transition[0].ToState,
			storage.StatePermanentlyFailed, storage.StateDisabled)
	}
	if !strings.Contains(transition[0].Reason, "reset") {
		t.Errorf("transition reason = %q, want a human explanation", transition[0].Reason)
	}

	// A reset never resumes anything.
	for _, call := range h.provider.Calls() {
		if call.Name == "ResumeSession" {
			t.Errorf("reset called ResumeSession: %+v", call)
		}
	}
}

// TestResetJSONMirrorsTheSiblingCommands keeps the machine-readable form usable.
func TestResetJSONMirrorsTheSiblingCommands(t *testing.T) {
	h := newHarness(t)
	seedFailedSession(t, h, canonicalSessionID)

	code, stdout, stderr := h.exec(t, "--json", "reset", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitOK, stderr)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("reset --json output is not JSON: %v (%q)", err, stdout)
	}
	if report["session_id"] != canonicalSessionID {
		t.Errorf("session_id = %v", report["session_id"])
	}
	if report["state"] != string(storage.StateDisabled) {
		t.Errorf("state = %v, want %s", report["state"], storage.StateDisabled)
	}
	if report["previous_state"] != string(storage.StatePermanentlyFailed) {
		t.Errorf("previous_state = %v", report["previous_state"])
	}
	if report["enabled"] != false {
		t.Errorf("enabled = %v, want false", report["enabled"])
	}
}

// TestResetRefusesEveryOtherState pins the one legal edge: reset only clears a
// session that has given up, and says what to do instead when it has not.
func TestResetRefusesEveryOtherState(t *testing.T) {
	cases := []struct {
		state storage.State
		// mentions is the text the message must carry so the operator knows
		// which situation they are in.
		mentions []string
	}{
		{state: storage.StateUnrecoverable, mentions: []string{"UNRECOVERABLE", "cannot be resumed"}},
		{state: storage.StateCompleted, mentions: []string{"COMPLETED", "finished normally"}},
		{state: storage.StateUserTerminated, mentions: []string{"USER_TERMINATED", "ended"}},
		{state: storage.StateDisabled, mentions: []string{"DISABLED", "ccresu enable"}},
		{state: storage.StateRunning, mentions: []string{"RUNNING", "ccresu recover"}},
		{state: storage.StateWaitingForReset, mentions: []string{"WAITING_FOR_RESET", "ccresu retry"}},
		{state: storage.StateResumed, mentions: []string{"RESUMED", "ccresu recover"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			h := newHarness(t)
			h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
				s.State = tc.state
				s.LastError = "stale error"
			})

			code, _, stderr := h.exec(t, "reset", canonicalSessionID)
			if code != ExitNotEligible {
				t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitNotEligible, stderr)
			}
			for _, want := range tc.mentions {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr = %q, want it to mention %q", stderr, want)
				}
			}
			if !strings.Contains(stderr, canonicalSessionID) {
				t.Errorf("stderr = %q, want it to name the session", stderr)
			}

			// Nothing changed: a refusal must not be a partial write.
			session, err := h.store.GetSession(context.Background(), canonicalSessionID)
			if err != nil {
				t.Fatalf("get session: %v", err)
			}
			if session.State != tc.state {
				t.Errorf("state = %s after a refused reset, want %s", session.State, tc.state)
			}
			if session.LastError != "stale error" {
				t.Errorf("LastError = %q, want it untouched", session.LastError)
			}
		})
	}
}

// TestResetArgumentAndLookupErrors covers the two other exit codes.
func TestResetArgumentAndLookupErrors(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StatePermanentlyFailed
	})

	if code, _, stderr := h.exec(t, "reset"); code != ExitUsage {
		t.Errorf("missing id exit = %d, want %d (stderr %q)", code, ExitUsage, stderr)
	}
	if code, _, stderr := h.exec(t, "reset", canonicalSessionID, "extra"); code != ExitUsage {
		t.Errorf("extra argument exit = %d, want %d (stderr %q)", code, ExitUsage, stderr)
	}
	if code, _, stderr := h.exec(t, "reset", "99999999-9999-4999-8999-999999999999"); code != ExitNotFound {
		t.Errorf("unknown session exit = %d, want %d (stderr %q)", code, ExitNotFound, stderr)
	}
}

// TestResetThenEnableLeavesTheSessionRecoverable is the sequence the docs
// recommend: reset to stop the verdict, enable to hand it back to ccresu.
func TestResetThenEnableLeavesTheSessionRecoverable(t *testing.T) {
	h := newHarness(t)
	seedFailedSession(t, h, canonicalSessionID)
	ctx := context.Background()

	if code, _, stderr := h.exec(t, "reset", canonicalSessionID); code != ExitOK {
		t.Fatalf("reset exit = %d (stderr %q)", code, stderr)
	}
	if code, _, stderr := h.exec(t, "enable", canonicalSessionID); code != ExitOK {
		t.Fatalf("enable exit = %d (stderr %q)", code, stderr)
	}
	session, err := h.store.GetSession(ctx, canonicalSessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if !session.Enabled {
		t.Error("the session is not enabled after enable")
	}
	if session.State != storage.StateDiscovered {
		t.Errorf("state = %s, want %s so the scheduler re-evaluates it", session.State, storage.StateDiscovered)
	}
}

// TestResetHelpIsDiscoverable checks the command owns its own usage page.
func TestResetHelpIsDiscoverable(t *testing.T) {
	h := newHarness(t)

	code, _, stderr := h.exec(t, "reset", "--help")
	if code != ExitOK {
		t.Fatalf("reset --help exit = %d, want %d", code, ExitOK)
	}
	for _, want := range []string{"Usage: ccresu reset <session-id>", "json"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("reset --help = %q, want it to mention %q", stderr, want)
		}
	}
}

// TestProviderIsNotConsultedForReset keeps the command honest: it is bookkeeping
// over the store, so a broken CLI cannot make it fail.
func TestProviderIsNotConsultedForReset(t *testing.T) {
	h := newHarness(t)
	seedFailedSession(t, h, canonicalSessionID)
	h.provider.ResumeErr = claude.ErrCLIUnavailable
	h.provider.DiscoverErr = claude.ErrCLIUnavailable
	h.provider.StatusErr = claude.ErrCLIUnavailable

	if code, _, stderr := h.exec(t, "reset", canonicalSessionID); code != ExitOK {
		t.Fatalf("reset exit = %d, want %d even with a broken CLI (stderr %q)", code, ExitOK, stderr)
	}
	if calls := h.provider.Calls(); len(calls) != 0 {
		t.Errorf("reset touched the claude provider: %+v", calls)
	}
}
