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
		StateCompleted,
		StateUserTerminated,
		StateDisabled,
		StateUnrecoverable,
		StateWaitingForInput,
		StateSpendCapReached,
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

// TestARefusedResumeReturnsToTheWait pins the refusal edge: a resume the CLI
// refuses because the limit is still in force goes back to WAITING_FOR_RESET.
// Before the edge existed the machine had nowhere to record the refusal, so the
// manager fell back to RETRY_WAIT and asked the same question again on a backoff.
func TestARefusedResumeReturnsToTheWait(t *testing.T) {
	if !CanTransition(StateResuming, StateWaitingForReset) {
		t.Fatal("RESUMING -> WAITING_FOR_RESET is not legal; a refused resume has nowhere to go")
	}
	if err := ValidateTransition(StateResuming, StateWaitingForReset); err != nil {
		t.Fatalf("ValidateTransition(RESUMING, WAITING_FOR_RESET) = %v, want nil", err)
	}
	if Terminal(StateResuming) {
		t.Error("RESUMING must not be terminal")
	}
	if !Pending(StateWaitingForReset) {
		t.Error("WAITING_FOR_RESET must stay pending, so a restarted daemon picks the wait up")
	}
	if !Pending(StateResuming) {
		t.Error("RESUMING must stay pending")
	}
}

func TestDiscoveryIsReachableFromEveryLiveState(t *testing.T) {
	// A failed attempt and a spent attempt budget both return the session to
	// DISCOVERED, and the budget guard runs before any check, so it can find the
	// session in any state that still permits automatic work. Every one of those
	// needs the edge, otherwise the session is stranded: the error is reported
	// but the state it asks for cannot be written.
	live := append([]State{StateDiscovered, StateRunning}, PendingStates()...)
	for _, from := range live {
		if !CanTransition(from, StateDiscovered) {
			t.Errorf("%s has no edge to DISCOVERED; a failure there cannot be recorded", from)
		}
	}
	// Settled and terminal states keep their edges: recovery never runs on them,
	// so nothing may claim a rediscovery from there.
	for _, from := range []State{StateResumed, StateCompleted, StateUserTerminated, StateUnrecoverable} {
		if CanTransition(from, StateDiscovered) {
			t.Errorf("%s must not reach DISCOVERED", from)
		}
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

// TestRescanEdgesParkTheSession pins the edges the automatic rescan and
// `ccresu search-limit` rely on.
//
// A scan reads a transcript and, when it recognises a banner, parks the session on
// the wait the banner names. It never resumes and never spends, so the machine
// needs one edge into USAGE_LIMIT_DETECTED from every state a scan starts from,
// the existing edge on to WAITING_FOR_RESET, and no path to RESUMING.
func TestRescanEdgesParkTheSession(t *testing.T) {
	// The three states the scan was extended to: RESUMED is Settled (no pass reads
	// it), RETRY_WAIT is counting down a backoff, and WAITING_FOR_INPUT is parked on
	// a human. A limit landing in any of them must be recordable.
	sources := []State{StateResumed, StateRetryWait, StateWaitingForInput}
	for _, from := range sources {
		if !CanTransition(from, StateUsageLimitDetected) {
			t.Errorf("%s has no edge to USAGE_LIMIT_DETECTED: a limit found in its transcript could not be recorded", from)
			continue
		}
		if err := ValidateTransition(from, StateUsageLimitDetected); err != nil {
			t.Errorf("ValidateTransition(%s, USAGE_LIMIT_DETECTED) = %v, want nil", from, err)
		}
		// The observation has to be written before the wait: the audit trail is what
		// explains why a session that was already settled is parked again.
		if !CanTransition(StateUsageLimitDetected, StateWaitingForReset) {
			t.Errorf("USAGE_LIMIT_DETECTED has no edge to WAITING_FOR_RESET: %s would be stranded after the scan", from)
		}
		if err := ValidateTransition(StateUsageLimitDetected, StateWaitingForReset); err != nil {
			t.Errorf("ValidateTransition(USAGE_LIMIT_DETECTED, WAITING_FOR_RESET) = %v, want nil", err)
		}
		// A scan may never spend: no edge from a scanned state to RESUMING, and none
		// from the state the scan writes either.
		if CanTransition(from, StateResuming) {
			t.Errorf("%s -> RESUMING is legal; a scan must never spend", from)
		}
		if CanTransition(StateUsageLimitDetected, StateResuming) {
			t.Error("USAGE_LIMIT_DETECTED -> RESUMING is legal; a scan must never spend")
		}
	}

	// RESUMED must not reach the wait directly. The edge exists so the audit trail
	// records the observation; skipping it would hide why the session moved.
	if CanTransition(StateResumed, StateWaitingForReset) {
		t.Error("RESUMED -> WAITING_FOR_RESET is legal; the observation USAGE_LIMIT_DETECTED must be recorded first")
	}
	if err := ValidateTransition(StateResumed, StateWaitingForReset); !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("ValidateTransition(RESUMED, WAITING_FOR_RESET) = %v, want ErrInvalidTransition", err)
	}

	// The path a search actually walks, end to end.
	chain := []State{StateResumed, StateUsageLimitDetected, StateWaitingForReset}
	for i := 0; i+1 < len(chain); i++ {
		if err := ValidateTransition(chain[i], chain[i+1]); err != nil {
			t.Errorf("the search's path is broken at %s -> %s: %v", chain[i], chain[i+1], err)
		}
	}

	// RESUMED stays Settled: that is the reason the rescan exists. No daemon pass
	// re-observes it, so without the scan a limit landing in its transcript is
	// invisible for ever.
	if !Settled(StateResumed) {
		t.Error("RESUMED is no longer Settled; a daemon pass would re-observe a finished conversation")
	}
	if Pending(StateResumed) {
		t.Error("RESUMED is Pending; the rescan would be unnecessary and every pass would re-read a finished conversation")
	}
}
