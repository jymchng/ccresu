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

func TestSearchLimitRefusesASessionThatIsNotIdle(t *testing.T) {
	for _, state := range []State{
		StateCompleted, StateDisabled, StateUnrecoverable, StateWaitingForReset,
		StateResuming,
	} {
		t.Run(string(state), func(t *testing.T) {
			store := newTestStore(t)
			seedSession(t, store, storage.Session{
				ID:          sessionA,
				State:       state,
				Enabled:     true,
				Recoverable: true,
			})
			var calls []string
			manager := searchManager(t, store, parkedWait(t, &calls, time.Time{}))

			_, err := manager.SearchLimit(context.Background(), sessionA,
				func(context.Context, string) (string, bool, error) {
					return "banner", true, nil
				})
			if !errors.Is(err, ErrNotEligible) {
				t.Fatalf("error = %v, want it to wrap ErrNotEligible", err)
			}
			if len(calls) != 0 {
				t.Errorf("parker calls = %d, want 0: an ineligible search must not park", len(calls))
			}
			stored, err := store.GetSession(context.Background(), sessionA)
			if err != nil {
				t.Fatalf("read session: %v", err)
			}
			if stored.State != state {
				t.Errorf("state = %s, want it unchanged at %s", stored.State, state)
			}
		})
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
