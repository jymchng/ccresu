package recovery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/storage"
)

// parkedWait is a LimitPark that records what it was called with and reports a
// wait ending at a fixed instant, so a test can assert the park without a
// scheduler.
func parkedWait(t *testing.T, calls *[]string, at time.Time) LimitPark {
	t.Helper()
	return func(_ context.Context, sessionID, banner string) (Wait, error) {
		if calls != nil {
			*calls = append(*calls, sessionID+"\x00"+banner)
		}
		return Wait{At: at, Known: !at.IsZero(), Delay: time.Hour, LimitID: 7}, nil
	}
}

// searchManager builds a manager whose parker is the stub above.
func searchManager(t *testing.T, store Store, park LimitPark) *Manager {
	t.Helper()
	return testManager(t, store, staticProvider(10, 200), func(cfg *Config) {
		cfg.LimitPark = park
	})
}

func TestSearchLimitParksAResumedSessionUntilTheReset(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, storage.Session{
		ID:          sessionA,
		State:       StateResumed,
		Enabled:     true,
		Recoverable: true,
	})
	reset := time.Date(2026, 1, 1, 18, 30, 0, 0, time.UTC)
	var calls []string
	manager := searchManager(t, store, parkedWait(t, &calls, reset))

	const banner = "You've hit your 5-hour limit"
	result, err := manager.SearchLimit(context.Background(), sessionA, func(context.Context, string) (string, bool, error) {
		return banner, true, nil
	})
	if err != nil {
		t.Fatalf("SearchLimit: %v", err)
	}
	if !result.Found {
		t.Fatal("found = false, want true")
	}
	if result.State != StateWaitingForReset {
		t.Errorf("state = %s, want %s", result.State, StateWaitingForReset)
	}
	if len(calls) != 1 {
		t.Fatalf("parker calls = %d, want 1", len(calls))
	}
	if got := strings.SplitN(calls[0], "\x00", 2)[1]; got != banner {
		t.Errorf("parker banner = %q, want %q", got, banner)
	}

	stored, err := store.GetSession(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if stored.State != StateWaitingForReset {
		t.Errorf("persisted state = %s, want %s", stored.State, StateWaitingForReset)
	}

	// The audit trail must show the re-observation that produced the wait, not a
	// jump straight from RESUMED to WAITING_FOR_RESET.
	transitions, err := store.ListTransitions(context.Background(), sessionA, 0)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	var sawObserved bool
	for _, transition := range transitions {
		if transition.FromState == StateResumed && transition.ToState == StateDiscovered {
			sawObserved = true
		}
	}
	if !sawObserved {
		t.Errorf("no RESUMED -> DISCOVERED transition; got %+v", transitions)
	}
	if !strings.Contains(result.Reason, reset.Format(time.RFC3339)) {
		t.Errorf("reason = %q, want it to name the reset instant", result.Reason)
	}
}

func TestSearchLimitWritesNothingWhenNoLimitIsFound(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, storage.Session{
		ID:          sessionA,
		State:       StateResumed,
		Enabled:     true,
		Recoverable: true,
	})
	var calls []string
	manager := searchManager(t, store, parkedWait(t, &calls, time.Time{}))

	result, err := manager.SearchLimit(context.Background(), sessionA, func(context.Context, string) (string, bool, error) {
		return "", false, nil
	})
	if err != nil {
		t.Fatalf("SearchLimit: %v", err)
	}
	if result.Found {
		t.Error("found = true, want false")
	}
	if result.State != StateResumed {
		t.Errorf("state = %s, want it unchanged at %s", result.State, StateResumed)
	}
	if len(calls) != 0 {
		t.Errorf("parker calls = %d, want 0: nothing was found, so nothing may be parked", len(calls))
	}

	stored, err := store.GetSession(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	if stored.State != StateResumed {
		t.Errorf("persisted state = %s, want %s", stored.State, StateResumed)
	}
	transitions, err := store.ListTransitions(context.Background(), sessionA, 0)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	if len(transitions) != 0 {
		t.Errorf("transitions = %+v, want none: a search that finds nothing is not an event", transitions)
	}
}

// TestSearchLimitSearchesEveryState pins the contract the command exists for: the
// transcript is read whatever state the session is in.
//
// Parsing a banner answers "when does this limit reset", and refusing to look
// because of the state a session happens to be in refuses that question — most
// sharply for a session already parked in WAITING_FOR_RESET, which is exactly the
// session whose stored reset an operator may need re-parsed. What the state
// decides is whether the search may write, which
// TestSearchLimitOnlyWritesWhereTheStateTakesAPark covers.
func TestSearchLimitSearchesEveryState(t *testing.T) {
	for _, state := range []State{
		StateDiscovered, StateResumed, StateWaitingForReset,
		StateResuming, StateCompleted, StateDisabled, StateUnrecoverable,
	} {
		t.Run(string(state), func(t *testing.T) {
			store := newTestStore(t)
			seedSession(t, store, storage.Session{
				ID:          sessionA,
				State:       state,
				Enabled:     true,
				Recoverable: true,
			})
			manager := searchManager(t, store, parkedWait(t, nil, time.Time{}))

			scans := 0
			result, err := manager.SearchLimit(context.Background(), sessionA,
				func(context.Context, string) (string, bool, error) {
					scans++
					return "banner", true, nil
				})
			if err != nil {
				t.Fatalf("SearchLimit on a %s session: %v", state, err)
			}
			if scans != 1 {
				t.Errorf("scans = %d, want 1: every state is searched", scans)
			}
			if !result.Found {
				t.Error("found = false, want true: the transcript said there was a banner")
			}
			if result.Banner != "banner" {
				t.Errorf("banner = %q, want the transcript text handed back to the operator", result.Banner)
			}
			// The state reported is the state the search ended in: the two idle
			// states take the park, so they end parked; every other state is read
			// and left where it was. Whether a state may be written to is
			// TestSearchLimitOnlyWritesWhereTheStateTakesAPark's subject.
			wantEnd := state
			if state == StateDiscovered || state == StateResumed {
				wantEnd = StateWaitingForReset
			}
			if result.State != wantEnd {
				t.Errorf("state = %s, want it reported as %s", result.State, wantEnd)
			}
		})
	}
}

// TestSearchLimitOnlyWritesWhereTheStateTakesAPark pins the other half: reading is
// unconditional, writing is not.
//
// RESUMING already has a pass that reads the transcript, and COMPLETED, DISABLED
// and UNRECOVERABLE are verdicts ccresu must not walk back. Those searches answer
// and leave the session alone, which is why Applied says so instead of the call
// failing.
func TestSearchLimitOnlyWritesWhereTheStateTakesAPark(t *testing.T) {
	cases := []struct {
		state       State
		enabled     bool
		wantApplied bool
	}{
		{state: StateDiscovered, enabled: true, wantApplied: true},
		{state: StateResumed, enabled: true, wantApplied: true},
		{state: StateWaitingForReset, enabled: true, wantApplied: true},
		{state: StateResuming, wantApplied: false},
		{state: StateCompleted, wantApplied: false},
		{state: StateUnrecoverable, wantApplied: false},
		{state: StateDisabled, wantApplied: false},
		// A park is a re-plan of work the operator turned off, so an otherwise
		// parkable state is read-only while the session is disabled.
		{state: StateDiscovered, enabled: false, wantApplied: false},
	}
	for _, tc := range cases {
		name := string(tc.state) + "/enabled=" + map[bool]string{true: "true", false: "false"}[tc.enabled]
		t.Run(name, func(t *testing.T) {
			store := newTestStore(t)
			seedSession(t, store, storage.Session{
				ID:          sessionA,
				State:       tc.state,
				Enabled:     tc.enabled,
				Recoverable: true,
			})
			var calls []string
			manager := searchManager(t, store, parkedWait(t, &calls, time.Time{}))

			result, err := manager.SearchLimit(context.Background(), sessionA,
				func(context.Context, string) (string, bool, error) { return "banner", true, nil })
			if err != nil {
				t.Fatalf("SearchLimit: %v", err)
			}
			if result.Applied != tc.wantApplied {
				t.Errorf("applied = %v, want %v", result.Applied, tc.wantApplied)
			}
			if result.Reason == "" && result.Found {
				t.Error("reason is empty: the operator is owed an explanation for whatever happened")
			}
			if got, want := len(calls), map[bool]int{true: 1, false: 0}[tc.wantApplied]; got != want {
				t.Errorf("parker calls = %d, want %d", got, want)
			}

			stored, err := store.GetSession(context.Background(), sessionA)
			if err != nil {
				t.Fatalf("read session: %v", err)
			}
			wantState := tc.state
			if tc.wantApplied {
				wantState = StateWaitingForReset
			}
			if stored.State != wantState {
				t.Errorf("state = %s, want %s", stored.State, wantState)
			}

			transitions, err := store.ListTransitions(context.Background(), sessionA, 0)
			if err != nil {
				t.Fatalf("list transitions: %v", err)
			}
			if !tc.wantApplied && len(transitions) != 0 {
				t.Errorf("transitions = %+v, want none: a read-only search writes nothing", transitions)
			}
			if tc.wantApplied && len(transitions) == 0 {
				t.Error("no transitions: an applied search records what it did")
			}
		})
	}
}

// TestSearchLimitReparsesTheWaitOfAParkedSession is the regression this contract
// was rewritten for.
//
// A session already in WAITING_FOR_RESET is the one whose reset time an operator
// most likely needs re-parsed — the stored instant can be stale, parsed from an
// older banner, or a poll of an unknown reset the transcript has since named. The
// search used to refuse that session outright, which left no way to correct a
// wrong wait except waiting it out.
func TestSearchLimitReparsesTheWaitOfAParkedSession(t *testing.T) {
	stale := time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC)
	correct := time.Date(2026, 1, 2, 4, 30, 0, 0, time.UTC)

	seedParked := func(t *testing.T, store *storage.Store, at time.Time) {
		t.Helper()
		if _, err := store.SaveSchedule(context.Background(), storage.Schedule{
			SessionID: sessionA,
			ResetAt:   at,
			ResetRaw:  "the instant the row already carried",
		}); err != nil {
			t.Fatalf("save schedule: %v", err)
		}
	}

	t.Run("a changed instant is recorded", func(t *testing.T) {
		store := newTestStore(t)
		seedSession(t, store, storage.Session{
			ID: sessionA, State: StateWaitingForReset, Enabled: true, Recoverable: true,
		})
		seedParked(t, store, stale)
		var calls []string
		manager := searchManager(t, store, parkedWait(t, &calls, correct))

		result, err := manager.SearchLimit(context.Background(), sessionA,
			func(context.Context, string) (string, bool, error) { return "banner", true, nil })
		if err != nil {
			t.Fatalf("SearchLimit on a parked session: %v", err)
		}
		if !result.Applied {
			t.Error("applied = false, want true: a parked session takes a re-planned wait")
		}
		if result.State != StateWaitingForReset {
			t.Errorf("state = %s, want %s", result.State, StateWaitingForReset)
		}
		if !result.Wait.At.Equal(correct) {
			t.Errorf("wait = %s, want the re-parsed instant %s", result.Wait.At, correct)
		}
		if len(calls) != 1 {
			t.Fatalf("parker calls = %d, want 1: the banner must be re-planned", len(calls))
		}
		if !strings.Contains(result.Reason, correct.Format(time.RFC3339)) {
			t.Errorf("reason = %q, want it to name the new instant", result.Reason)
		}
		if !strings.Contains(result.Reason, "re-parsed") {
			t.Errorf("reason = %q, want it to say the reset time was re-parsed", result.Reason)
		}

		transitions, err := store.ListTransitions(context.Background(), sessionA, 0)
		if err != nil {
			t.Fatalf("list transitions: %v", err)
		}
		var sawReparse bool
		for _, transition := range transitions {
			if transition.FromState == StateWaitingForReset && transition.ToState == StateWaitingForReset {
				sawReparse = true
			}
		}
		if !sawReparse {
			t.Errorf("transitions = %+v, want a WAITING_FOR_RESET -> WAITING_FOR_RESET row recording the re-parse", transitions)
		}
	})

	t.Run("an unchanged instant is not recorded", func(t *testing.T) {
		store := newTestStore(t)
		seedSession(t, store, storage.Session{
			ID: sessionA, State: StateWaitingForReset, Enabled: true, Recoverable: true,
		})
		seedParked(t, store, correct)
		manager := searchManager(t, store, parkedWait(t, nil, correct))

		result, err := manager.SearchLimit(context.Background(), sessionA,
			func(context.Context, string) (string, bool, error) { return "banner", true, nil })
		if err != nil {
			t.Fatalf("SearchLimit: %v", err)
		}
		if !result.Applied {
			t.Error("applied = false, want true: the wait was checked and left correct")
		}
		if !strings.Contains(result.Reason, "already matches") {
			t.Errorf("reason = %q, want it to say the stored reset already matched", result.Reason)
		}
		transitions, err := store.ListTransitions(context.Background(), sessionA, 0)
		if err != nil {
			t.Fatalf("list transitions: %v", err)
		}
		if len(transitions) != 0 {
			t.Errorf("transitions = %+v, want none: pressing the key twice is not two events", transitions)
		}
	})

	t.Run("no stored schedule counts as a change", func(t *testing.T) {
		store := newTestStore(t)
		seedSession(t, store, storage.Session{
			ID: sessionA, State: StateWaitingForReset, Enabled: true, Recoverable: true,
		})
		manager := searchManager(t, store, parkedWait(t, nil, correct))

		if _, err := manager.SearchLimit(context.Background(), sessionA,
			func(context.Context, string) (string, bool, error) { return "banner", true, nil }); err != nil {
			t.Fatalf("SearchLimit: %v", err)
		}
		transitions, err := store.ListTransitions(context.Background(), sessionA, 0)
		if err != nil {
			t.Fatalf("list transitions: %v", err)
		}
		if len(transitions) == 0 {
			t.Error("no transitions: a wait planned where none was stored is a change")
		}
	})
}

// TestSearchLimitReadsWithoutAParker pins that a read-only search needs no
// parker: the hook exists to plan a wait, and a search that cannot write one has
// nothing to plan.
func TestSearchLimitReadsWithoutAParker(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, storage.Session{
		ID: sessionA, State: StateCompleted, Enabled: true, Recoverable: true,
	})
	manager := searchManager(t, store, nil)

	result, err := manager.SearchLimit(context.Background(), sessionA,
		func(context.Context, string) (string, bool, error) { return "banner", true, nil })
	if err != nil {
		t.Fatalf("SearchLimit: %v", err)
	}
	if result.Applied || !result.Found {
		t.Errorf("found = %v, applied = %v; want the banner read and nothing written", result.Found, result.Applied)
	}
}

func TestSearchLimitOnADiscoveredSessionIsAccepted(t *testing.T) {
	// DISCOVERED is not settled either, so a search is allowed there too: the
	// operator's assertion that the session is idle is the authority, and
	// refusing the state the daemon also reads would make the command depend on
	// the daemon's timing.
	store := newTestStore(t)
	seedSession(t, store, storage.Session{
		ID:          sessionA,
		State:       StateDiscovered,
		Enabled:     true,
		Recoverable: true,
	})
	var calls []string
	manager := searchManager(t, store, parkedWait(t, &calls, time.Time{}))

	result, err := manager.SearchLimit(context.Background(), sessionA,
		func(context.Context, string) (string, bool, error) { return "banner", true, nil })
	if err != nil {
		t.Fatalf("SearchLimit: %v", err)
	}
	if result.State != StateWaitingForReset {
		t.Errorf("state = %s, want %s", result.State, StateWaitingForReset)
	}
}

func TestSearchLimitWithoutAParkerIsRefused(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, storage.Session{
		ID:          sessionA,
		State:       StateResumed,
		Enabled:     true,
		Recoverable: true,
	})
	manager := searchManager(t, store, nil)

	_, err := manager.SearchLimit(context.Background(), sessionA,
		func(context.Context, string) (string, bool, error) { return "banner", true, nil })
	if !errors.Is(err, ErrNoParker) {
		t.Fatalf("error = %v, want it to wrap ErrNoParker", err)
	}
	// The state is untouched: a wait with no schedule is not a wait.
	stored, readErr := store.GetSession(context.Background(), sessionA)
	if readErr != nil {
		t.Fatalf("read session: %v", readErr)
	}
	if stored.State != StateResumed {
		t.Errorf("state = %s, want it unchanged at %s", stored.State, StateResumed)
	}
}

func TestSearchLimitRejectsAnUnknownSession(t *testing.T) {
	store := newTestStore(t)
	manager := searchManager(t, store, parkedWait(t, nil, time.Time{}))

	_, err := manager.SearchLimit(context.Background(), sessionA,
		func(context.Context, string) (string, bool, error) { return "", false, nil })
	if !errors.Is(err, ErrNotEligible) {
		t.Fatalf("error = %v, want it to wrap ErrNotEligible", err)
	}
}

// TestARescanCanNeverResume is the invariant the automatic rescan rests on.
//
// TestARescanParksOnlyAfterObserving pins the machine's scan contract: a scan
// that parses a banner parks the session on the reset, and a resumed session is
// re-observed before it may be parked again.
//
// The second half is what keeps a stale banner from driving a park per pass: the
// banner that parked a session is still inside the transcript tail the detector
// reads, so RESUMED must reach the park only through DISCOVERED.
func TestARescanParksOnlyAfterObserving(t *testing.T) {
	if !CanTransition(StateDiscovered, StateWaitingForReset) {
		t.Error("DISCOVERED -> WAITING_FOR_RESET is not legal, so a scan could not park a session")
	}
	if CanTransition(StateResumed, StateWaitingForReset) {
		t.Error("RESUMED -> WAITING_FOR_RESET is legal, so the observation behind the wait could be skipped")
	}
	if !CanTransition(StateResumed, StateDiscovered) {
		t.Error("RESUMED -> DISCOVERED is not legal, so a resumed session could never be re-observed")
	}
	// A scan never hands a continuation over. The only other edge into RESUMING
	// is DISCOVERED -> RESUMING, and that one is the operator's (`ccresu
	// recover`, `ccresu retry`), never a transcript read.
	if CanTransition(StateResumed, StateResuming) {
		t.Error("RESUMED -> RESUMING is legal, so a settled session could be resumed again")
	}
	if !CanTransition(StateWaitingForReset, StateResuming) {
		t.Error("WAITING_FOR_RESET -> RESUMING is not legal, so a parked session could never be resumed")
	}
}
