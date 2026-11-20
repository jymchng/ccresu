package app

// PRD 2 / R13 at the operator surface: `ccresu inspect` must be able to say what
// the supervisor published, not only what was inferred from a file size.
//
// The rendering is asserted rather than the row, because the row was already
// correct: the defect R13 names is that the fact was *invisible* afterwards, so
// an operator could not re-examine the decision the attempt rested on. The
// second test is the one that keeps "we did not look" from reading as "nothing
// was there" (R8).

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/storage"
)

const observedSessionID = "44444444-4444-4444-8444-444444444444"

func TestInspectRendersTheObservedState(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, observedSessionID, func(s *storage.Session) {
		s.State = storage.StateDiscovered
	})

	attempt, err := h.store.StartAttempt(context.Background(), observedSessionID, 0, "claude --bg --resume "+observedSessionID)
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	listed := true
	if _, err := h.store.FinishAttempt(context.Background(), attempt.ID, storage.AttemptResult{
		State:   storage.StateDiscovered,
		Success: storage.BoolPtr(false),
		Error:   "the session is blocked",
		Observation: storage.ObservationRecord{
			State:      "blocked",
			Status:     "idle",
			WaitingFor: "permission prompt",
			ObservedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
			Listed:     &listed,
		},
	}); err != nil {
		t.Fatalf("FinishAttempt: %v", err)
	}

	code, out, stderr := h.exec(t, "inspect", observedSessionID)
	if code != ExitOK {
		t.Fatalf("inspect exit = %d, want %d (stderr %q)", code, ExitOK, stderr)
	}

	// R13: the state, the corroborating status, the reason and the fact it was
	// in the listing are all present, verbatim.
	for _, field := range []struct{ name, want string }{
		{"state", "blocked"},
		{"status", "idle"},
		{"waiting for", "permission prompt"},
		{"in listing", "yes"},
	} {
		got, ok := inspectField(out, "Observed", field.name)
		if !ok {
			t.Errorf("inspect printed no Observed %q field:\n%s", field.name, out)
			continue
		}
		if got != field.want {
			t.Errorf("Observed %s = %q, want %q:\n%s", field.name, got, field.want, out)
		}
	}
	if !strings.Contains(out, "Observed") {
		t.Errorf("inspect printed no Observed block:\n%s", out)
	}
}

func TestInspectRendersNotRecordedForAnAttemptWithNoObservation(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, observedSessionID, func(s *storage.Session) {
		s.State = storage.StateDiscovered
	})

	// An attempt from before the observation columns existed: it carries the
	// zero record, which must render as "not recorded" rather than as an empty
	// state. The two are different facts (R8).
	attempt, err := h.store.StartAttempt(context.Background(), observedSessionID, 0, "claude --bg --resume "+observedSessionID)
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}
	if _, err := h.store.FinishAttempt(context.Background(), attempt.ID, storage.AttemptResult{
		State:   storage.StateDiscovered,
		Success: storage.BoolPtr(false),
		Error:   "the transcript check failed",
	}); err != nil {
		t.Fatalf("FinishAttempt: %v", err)
	}

	code, out, stderr := h.exec(t, "inspect", observedSessionID)
	if code != ExitOK {
		t.Fatalf("inspect exit = %d, want %d (stderr %q)", code, ExitOK, stderr)
	}

	got, ok := inspectField(out, "Observed", "state")
	if !ok {
		t.Fatalf("inspect printed no Observed block:\n%s", out)
	}
	if got != "not recorded" {
		t.Errorf("Observed state = %q, want %q: a missing observation is not an empty state", got, "not recorded")
	}
	if strings.Contains(out, "in listing") {
		t.Errorf("inspect reported a listing fact for an attempt that never observed:\n%s", out)
	}
}

func TestTheProvenMessageNamesTheProofItActuallyHas(t *testing.T) {
	t.Parallel()

	// R3 at the operator surface: growth was demoted to a diagnostic, so a
	// proven resume must not be described as growth. The sentence is what an
	// operator reads after `ccresu recover`, and a fixed one taught the wrong
	// mental model: a handoff never grows a transcript under ccresu's process.
	cases := []struct {
		name         string
		verification recovery.Verification
		wantContains string
	}{
		{
			name:         "a handoff is proven by acceptance",
			verification: recovery.Verification{Proven: true, HandoffAccepted: true, HandoffStateID: "ab12cd34", HandoffName: "ccresu-11111111"},
			wantContains: "handed to the background supervisor",
		},
		{
			name:         "published state is proven by the state it published",
			verification: recovery.Verification{Proven: true, SessionIDPreserved: true, PublishedState: "working", ObservationRecorded: true},
			wantContains: `published state "working"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := describeVerification(recovery.Outcome{Verification: tc.verification})
			if !strings.HasPrefix(got, "proven: ") {
				t.Fatalf("describeVerification() = %q, want a proven message", got)
			}
			if !strings.Contains(got, tc.wantContains) {
				t.Errorf("describeVerification() = %q, want it to contain %q", got, tc.wantContains)
			}
			if strings.Contains(got, "transcript grew") {
				t.Errorf("describeVerification() = %q, want growth not named as the proof", got)
			}
		})
	}
}
