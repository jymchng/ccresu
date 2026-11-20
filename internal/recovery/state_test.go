package recovery

import (
	"errors"
	"strings"
	"testing"
)

// machineStates is every state the machine knows: the four beats of one lap,
// then the three verdicts that stop it.
func machineStates() []State {
	return []State{
		StateDiscovered,
		StateWaitingForReset,
		StateResuming,
		StateResumed,
		StateCompleted,
		StateDisabled,
		StateUnrecoverable,
	}
}

// removedStates are the nine states the machine used to have, as the strings
// that are still sitting in existing databases. Each one must now be rejected
// everywhere: as a Go constant it no longer exists, and as a value it must fail
// State.Valid so no row and no call can reintroduce it.
func removedStates() []State {
	return []State{
		"USAGE_LIMIT_DETECTED",
		"RESET_TIME_REACHED",
		"CHECKING_SESSION",
		"CHECKING_LIMIT",
		"RETRY_WAIT",
		"RUNNING",
		"USER_TERMINATED",
		"WAITING_FOR_INPUT",
		"SPEND_CAP_REACHED",
	}
}

// TestTheMachineHasExactlySevenStates pins the size of the machine. Every state
// in the table must be a known storage state and vice versa, so a state cannot
// be added to one enumeration and forgotten in the other.
func TestTheMachineHasExactlySevenStates(t *testing.T) {
	states := machineStates()
	if len(states) != 7 {
		t.Fatalf("the machine has %d states, want 7", len(states))
	}
	if len(transitions) != len(states) {
		t.Fatalf("transitions has %d entries, want %d", len(transitions), len(states))
	}
	known := make(map[State]bool, len(states))
	for _, state := range states {
		if !state.Valid() {
			t.Errorf("state %q is not a known storage state", state)
		}
		if _, ok := transitions[state]; !ok {
			t.Errorf("state %q has no entry in the transition table", state)
		}
		known[state] = true
	}
	for state := range transitions {
		if !known[state] {
			t.Errorf("transitions has an entry for %q, which is not one of the seven states", state)
		}
	}
}

// TestRemovedStatesAreRejected is the other half of the collapse: the nine states
// the machine no longer has must be unreachable from every direction, so an old
// database row or a stale caller cannot walk back into one.
func TestRemovedStatesAreRejected(t *testing.T) {
	for _, gone := range removedStates() {
		if gone.Valid() {
			t.Errorf("%s is still a known storage state", gone)
		}
		if _, ok := transitions[gone]; ok {
			t.Errorf("%s still has an entry in the transition table", gone)
		}
		if got := TransitionsFrom(gone); len(got) != 0 {
			t.Errorf("TransitionsFrom(%s) = %v, want empty", gone, got)
		}
		if Terminal(gone) || Settled(gone) || Pending(gone) || Failed(gone) {
			t.Errorf("%s is terminal/settled/pending/failed, want none of them", gone)
		}
		for _, live := range machineStates() {
			if CanTransition(gone, live) {
				t.Errorf("%s -> %s is legal; a removed state must not transition out", gone, live)
			}
			if CanTransition(live, gone) {
				t.Errorf("%s -> %s is legal; a removed state must not be a destination", live, gone)
			}
		}
		err := ValidateTransition(StateDiscovered, gone)
		if !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("ValidateTransition(DISCOVERED, %s) = %v, want ErrInvalidTransition", gone, err)
		}
		if err != nil && !strings.Contains(err.Error(), "not a known state") {
			t.Errorf("error %q does not say %s is not a known state", err, gone)
		}
	}
}

// TestHappyPathLoopIsLegal pins the four beats of one lap and the edge that
// closes it, so the loop is a cycle rather than a line that ends at RESUMED.
func TestHappyPathLoopIsLegal(t *testing.T) {
	chain := []State{
		StateDiscovered,
		StateWaitingForReset,
		StateResuming,
		StateResumed,
		StateDiscovered,
	}
	for i := 0; i+1 < len(chain); i++ {
		if !CanTransition(chain[i], chain[i+1]) {
			t.Errorf("loop edge %s -> %s is not legal", chain[i], chain[i+1])
		}
		if err := ValidateTransition(chain[i], chain[i+1]); err != nil {
			t.Errorf("ValidateTransition(%s, %s) = %v, want nil", chain[i], chain[i+1], err)
		}
	}
	if !CanTransition(StateResumed, StateDiscovered) {
		t.Fatal("RESUMED -> DISCOVERED is not legal; the loop would stop after one resume")
	}
}

// TestTheLoopRepeats walks three laps to prove the cycle closes every time: this
// is the behaviour the whole machine exists for — parse the reset, resume, go
// back to watching.
func TestTheLoopRepeats(t *testing.T) {
	lap := []State{StateDiscovered, StateWaitingForReset, StateResuming, StateResumed}
	current := StateDiscovered
	for lapNumber := 1; lapNumber <= 3; lapNumber++ {
		for _, next := range lap {
			if err := ValidateTransition(current, next); err != nil {
				t.Fatalf("lap %d: %s -> %s: %v", lapNumber, current, next, err)
			}
			current = next
		}
	}
	if current != StateResumed {
		t.Fatalf("after three laps the session is in %s, want RESUMED", current)
	}
}

// TestParkedSessionRepollsItself pins the re-entry: a banner that named no reset
// time is polled, and every poll re-parks the session.
func TestParkedSessionRepollsItself(t *testing.T) {
	if !CanTransition(StateWaitingForReset, StateWaitingForReset) {
		t.Fatal("WAITING_FOR_RESET -> WAITING_FOR_RESET is not legal; an unknown reset could not be re-parked")
	}
	if err := ValidateTransition(StateWaitingForReset, StateWaitingForReset); err != nil {
		t.Fatalf("ValidateTransition(WAITING_FOR_RESET, WAITING_FOR_RESET) = %v, want nil", err)
	}
}

// TestRefusedResumeReturnsToTheWait pins the refusal edge: the CLI ran, said the
// limit is still in force and named the next reset, so the session returns to the
// wait it was already in rather than to a backoff that would ask again a minute
// later.
func TestRefusedResumeReturnsToTheWait(t *testing.T) {
	if !CanTransition(StateResuming, StateWaitingForReset) {
		t.Fatal("RESUMING -> WAITING_FOR_RESET is not legal; a refused resume has nowhere to go")
	}
	if err := ValidateTransition(StateResuming, StateWaitingForReset); err != nil {
		t.Fatalf("ValidateTransition(RESUMING, WAITING_FOR_RESET) = %v, want nil", err)
	}
	if Terminal(StateResuming) {
		t.Error("RESUMING must not be terminal")
	}
	if !Pending(StateResuming) {
		t.Error("RESUMING must stay pending, so a restarted daemon picks the attempt up")
	}
}

// TestFailedResumeReturnsToDiscovery pins the recovery edge: a failed attempt and
// a spent attempt budget both put the session back at the top of the loop, which
// is why a session is retried as often as its transcript still shows a limit
// without an operator running anything.
func TestFailedResumeReturnsToDiscovery(t *testing.T) {
	for _, from := range []State{StateResuming, StateWaitingForReset} {
		if !CanTransition(from, StateDiscovered) {
			t.Errorf("%s has no edge to DISCOVERED; a failure there could not be recorded", from)
		}
	}
}

// TestNoDoubleResume pins the reason RESUMED exists. A just-resumed session
// still has the banner that parked it inside the transcript tail the detector
// reads, so the machine must refuse to park or resume it again until a scan has
// observed it once more.
func TestNoDoubleResume(t *testing.T) {
	if CanTransition(StateResumed, StateResuming) {
		t.Error("RESUMED -> RESUMING is legal; a session could be resumed off its own stale banner")
	}
	if CanTransition(StateResumed, StateWaitingForReset) {
		t.Error("RESUMED -> WAITING_FOR_RESET is legal; the next limit must be observed before it is parked")
	}
	// The two observing edges into RESUMED are legal, and they are the whole
	// reason a pass can recognise a session that continued on its own once the
	// window reset instead of handing it a second continuation. They are
	// observations from the published state, not resumes, so they spend no
	// attempt and they do not weaken the guard above.
	for _, from := range []State{StateDiscovered, StateWaitingForReset} {
		if !CanTransition(from, StateResumed) {
			t.Errorf("%s -> RESUMED is illegal; a session that resumed itself could not be recorded", from)
		}
	}
	err := ValidateTransition(StateResumed, StateResuming)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("ValidateTransition(RESUMED, RESUMING) = %v, want ErrInvalidTransition", err)
	}
	if !strings.Contains(err.Error(), "RESUMED -> RESUMING") {
		t.Errorf("error %q does not name the edge", err)
	}
	if !strings.Contains(err.Error(), "legal targets:") {
		t.Errorf("error %q does not list the legal targets", err)
	}
}

// TestEveryLiveStateHasTheSameFourExits pins the invariant that keeps the machine
// small: whatever a live session is doing, it can always go back to the top of
// the loop, and it can always be recorded as finished, disabled or unresumable.
// No beat can strand a session.
func TestEveryLiveStateHasTheSameFourExits(t *testing.T) {
	live := []State{StateDiscovered, StateWaitingForReset, StateResuming, StateResumed}
	exits := []State{StateDiscovered, StateCompleted, StateDisabled, StateUnrecoverable}
	for _, from := range live {
		if Terminal(from) {
			t.Errorf("%s is terminal; a live state must keep moving", from)
		}
		for _, to := range exits {
			if !CanTransition(from, to) {
				t.Errorf("%s -> %s is not legal; a session there would be stranded", from, to)
			}
		}
	}
}

// TestOperatorEntriesIntoTheLoop pins the two edges a human uses: resuming now
// instead of waiting for a parsed instant, and turning a disabled session back
// on.
func TestOperatorEntriesIntoTheLoop(t *testing.T) {
	if !CanTransition(StateDiscovered, StateResuming) {
		t.Error("DISCOVERED -> RESUMING is not legal; `ccresu recover` could not resume a discovered session")
	}
	if !CanTransition(StateDisabled, StateDiscovered) {
		t.Error("DISABLED -> DISCOVERED is not legal; `ccresu enable` could not turn the loop back on")
	}
	if got := TransitionsFrom(StateDisabled); len(got) != 1 || got[0] != StateDiscovered {
		t.Errorf("TransitionsFrom(DISABLED) = %v, want exactly [DISCOVERED]: re-enabling is the only way out", got)
	}
}

// TestTerminalVerdicts pins the three states the loop stops in.
func TestTerminalVerdicts(t *testing.T) {
	for _, state := range []State{StateCompleted, StateUnrecoverable} {
		if !Terminal(state) {
			t.Errorf("Terminal(%s) = false, want true", state)
		}
		if got := TransitionsFrom(state); len(got) != 0 {
			t.Errorf("TransitionsFrom(%s) = %v, want empty", state, got)
		}
	}
	if !Terminal(StateDisabled) {
		t.Error("Terminal(DISABLED) = false, want true: nothing automatic runs from a disabled session")
	}
	for _, state := range []State{StateDiscovered, StateWaitingForReset, StateResuming, StateResumed} {
		if Terminal(state) {
			t.Errorf("Terminal(%s) = true, want false", state)
		}
	}
	err := ValidateTransition(StateUnrecoverable, StateDiscovered)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("ValidateTransition(UNRECOVERABLE, DISCOVERED) = %v, want ErrInvalidTransition", err)
	}
	if !strings.Contains(err.Error(), "is terminal") {
		t.Errorf("error %q does not explain the terminal state", err)
	}
}

func TestTerminalSettledPendingFailed(t *testing.T) {
	cases := []struct {
		state    State
		terminal bool
		settled  bool
		pending  bool
		failed   bool
	}{
		{StateDiscovered, false, false, false, false},
		{StateWaitingForReset, false, false, true, false},
		{StateResuming, false, false, true, false},
		{StateResumed, false, true, false, false},
		{StateCompleted, true, true, false, false},
		{StateDisabled, true, true, false, false},
		{StateUnrecoverable, true, true, false, true},
	}
	if len(cases) != len(machineStates()) {
		t.Fatalf("the table covers %d states, want %d", len(cases), len(machineStates()))
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

// TestPendingStatesAreTheTwoBeats pins PendingStates to the two states a
// restarted daemon must pick up. DISCOVERED is deliberately absent: the scan
// reads it on its own, and treating it as pending would make the recovery pass
// resume sessions that never showed a limit.
func TestPendingStatesAreTheTwoBeats(t *testing.T) {
	want := []State{StateWaitingForReset, StateResuming}
	got := PendingStates()
	if len(got) != len(want) {
		t.Fatalf("PendingStates() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("PendingStates() = %v, want %v", got, want)
		}
	}
	if Pending(StateDiscovered) {
		t.Error("DISCOVERED is pending; every pass would resume a session that never hit a limit")
	}
	for _, state := range machineStates() {
		inList := false
		for _, candidate := range want {
			if candidate == state {
				inList = true
			}
		}
		if Pending(state) != inList {
			t.Errorf("Pending(%s) = %v but its presence in PendingStates is %v", state, Pending(state), inList)
		}
	}
}

func TestCanTransitionMatchesTable(t *testing.T) {
	for _, from := range machineStates() {
		allowed := TransitionsFrom(from)
		for _, to := range machineStates() {
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
	for _, from := range machineStates() {
		allowed := TransitionsFrom(from)
		for i := 1; i < len(allowed); i++ {
			if allowed[i-1] > allowed[i] {
				t.Errorf("TransitionsFrom(%s) is not sorted: %v", from, allowed)
			}
		}
	}
}

func TestCanTransitionRejectsUnknownStates(t *testing.T) {
	unknown := State("BOGUS")
	if CanTransition(unknown, StateDiscovered) {
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
	// RESUMED -> RESUMING is the stale-banner edge the machine refuses: a
	// session that was just resumed must be observed again before it can be
	// resumed a second time.
	err := ValidateTransition(StateResumed, StateResuming)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("error = %v, want ErrInvalidTransition", err)
	}
	if !strings.Contains(err.Error(), "RESUMED -> RESUMING") {
		t.Errorf("error %q does not name the edge", err)
	}
	if !strings.Contains(err.Error(), "legal targets:") {
		t.Errorf("error %q does not list the legal targets", err)
	}

	err = ValidateTransition(State("NOPE"), StateDiscovered)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("error = %v, want ErrInvalidTransition", err)
	}
	if !strings.Contains(err.Error(), "not a known state") {
		t.Errorf("error %q does not explain the unknown state", err)
	}
}
