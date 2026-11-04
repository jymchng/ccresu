package recovery

import (
	"errors"
	"strings"
	"testing"
)

// allStates is every state the machine knows, in the documented order.
func allStates() []State {
	return []State{
		StateDiscovered,
		StateRunning,
		StateUsageLimitDetected,
		StateWaitingForReset,
		StateResetTimeReached,
		StateCheckingSession,
		StateCheckingLimit,
		StateResuming,
		StateResumed,
		StateRetryWait,
		StatePermanentlyFailed,
		StateCompleted,
		StateUserTerminated,
		StateDisabled,
		StateUnrecoverable,
	}
}

func TestStatesAreKnownStorageStates(t *testing.T) {
	for _, state := range allStates() {
		if !state.Valid() {
			t.Errorf("state %q is not a known storage state", state)
		}
		if _, ok := transitions[state]; !ok {
			t.Errorf("state %q has no entry in the transition table", state)
		}
	}
}

func TestHappyPathEdgesAreLegal(t *testing.T) {
	chain := []State{
		StateDiscovered,
		StateRunning,
		StateUsageLimitDetected,
		StateWaitingForReset,
		StateResetTimeReached,
		StateCheckingSession,
		StateCheckingLimit,
		StateResuming,
		StateResumed,
	}
	for i := 0; i+1 < len(chain); i++ {
		if !CanTransition(chain[i], chain[i+1]) {
			t.Errorf("happy path edge %s -> %s is not legal", chain[i], chain[i+1])
		}
		if err := ValidateTransition(chain[i], chain[i+1]); err != nil {
			t.Errorf("ValidateTransition(%s, %s) = %v, want nil", chain[i], chain[i+1], err)
		}
	}
	if !CanTransition(StateRetryWait, StateCheckingSession) {
		t.Error("RETRY_WAIT must loop back to CHECKING_SESSION")
	}
}

func TestCanTransitionMatchesTable(t *testing.T) {
	for _, from := range allStates() {
		allowed := TransitionsFrom(from)
		for _, to := range allStates() {
			want := false
			for _, candidate := range allowed {
				if candidate == to {
					want = true
				}
			}
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestTransitionsFromIsSorted(t *testing.T) {
	for _, from := range allStates() {
		allowed := TransitionsFrom(from)
		for i := 1; i < len(allowed); i++ {
			if allowed[i-1] > allowed[i] {
				t.Errorf("TransitionsFrom(%s) is not sorted: %v", from, allowed)
			}
		}
	}
	if got := TransitionsFrom(StateUnrecoverable); len(got) != 0 {
		t.Errorf("TransitionsFrom(UNRECOVERABLE) = %v, want empty", got)
	}
}

func TestCanTransitionRejectsUnknownStates(t *testing.T) {
	unknown := State("BOGUS")
	if CanTransition(unknown, StateRunning) {
		t.Error("an unknown source state must not transition")
	}
	if CanTransition(StateDiscovered, unknown) {
		t.Error("an unknown target state must not be reached")
	}
	if Terminal(unknown) {
		t.Error("an unknown state is not terminal")
	}
}

func TestValidateTransitionReportsIllegalEdges(t *testing.T) {
	err := ValidateTransition(StateDiscovered, StateResumed)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("error = %v, want ErrInvalidTransition", err)
	}
	if !strings.Contains(err.Error(), "DISCOVERED -> RESUMED") {
		t.Errorf("error %q does not name the edge", err)
	}
	if !strings.Contains(err.Error(), "legal targets:") {
		t.Errorf("error %q does not list the legal targets", err)
	}

	err = ValidateTransition(StateUnrecoverable, StateRunning)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("error = %v, want ErrInvalidTransition", err)
	}
	if !strings.Contains(err.Error(), "is terminal") {
		t.Errorf("error %q does not explain the terminal state", err)
	}

	err = ValidateTransition(State("NOPE"), StateRunning)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("error = %v, want ErrInvalidTransition", err)
	}
	if !strings.Contains(err.Error(), "not a known state") {
		t.Errorf("error %q does not explain the unknown state", err)
	}
}

func TestTerminalSettledPendingFailed(t *testing.T) {
	type want struct {
		state    State
		terminal bool
		settled  bool
		pending  bool
		failed   bool
	}
	cases := []want{
		{StateDiscovered, false, false, false, false},
		{StateRunning, false, false, false, false},
		{StateUsageLimitDetected, false, false, true, false},
		{StateWaitingForReset, false, false, true, false},
		{StateResetTimeReached, false, false, true, false},
		{StateCheckingSession, false, false, true, false},
		{StateCheckingLimit, false, false, true, false},
		{StateResuming, false, false, true, false},
		{StateResumed, false, true, false, false},
		{StateRetryWait, false, false, true, false},
		{StatePermanentlyFailed, false, false, false, true},
		{StateCompleted, true, true, false, false},
		{StateUserTerminated, true, true, false, false},
		{StateDisabled, false, true, false, false},
		{StateUnrecoverable, true, true, false, true},
	}
	for _, tc := range cases {
		if got := Terminal(tc.state); got != tc.terminal {
			t.Errorf("Terminal(%s) = %v, want %v", tc.state, got, tc.terminal)
		}
		if got := Settled(tc.state); got != tc.settled {
			t.Errorf("Settled(%s) = %v, want %v", tc.state, got, tc.settled)
		}
		if got := Pending(tc.state); got != tc.pending {
			t.Errorf("Pending(%s) = %v, want %v", tc.state, got, tc.pending)
		}
		if got := Failed(tc.state); got != tc.failed {
			t.Errorf("Failed(%s) = %v, want %v", tc.state, got, tc.failed)
		}
	}
	if Settled(State("BOGUS")) || Pending(State("BOGUS")) || Failed(State("BOGUS")) {
		t.Error("an unknown state must be neither settled, pending nor failed")
	}
}

func TestPendingStatesAreAllPending(t *testing.T) {
	states := PendingStates()
	if len(states) == 0 {
		t.Fatal("PendingStates is empty")
	}
	for _, state := range states {
		if !Pending(state) {
			t.Errorf("PendingStates includes %s, which Pending rejects", state)
		}
	}
	for _, state := range allStates() {
		if Pending(state) {
			found := false
			for _, candidate := range states {
				if candidate == state {
					found = true
				}
			}
			if !found {
				t.Errorf("Pending(%s) is true but the state is not in PendingStates", state)
			}
		}
	}
}
