package recovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
)

// LimitScan looks for a usage-limit banner in one session's transcript.
//
// It is injected for the same reason LimitRecheck and LimitPark are: reading a
// transcript is the provider's job and recognising a banner in it is the
// detector's, so the recovery engine asks whether one is there instead of
// growing a second copy of either. A scan that finds nothing reports
// found == false, which is an answer and not a failure.
type LimitScan func(ctx context.Context, sessionID string) (banner string, found bool, err error)

// LimitSearch is what one operator-initiated search found and did.
//
// It is a value rather than an Outcome because a search that finds nothing
// changes nothing: there is no attempt, no transition and no schedule to
// report, and saying so is the whole result.
type LimitSearch struct {
	// SessionID is the session that was searched.
	SessionID string
	// Found reports whether a usage-limit banner was recognised.
	Found bool
	// State is the session's state when the search ended: WAITING_FOR_RESET
	// when a limit was found and parked, otherwise the state it started in.
	State State
	// Banner is the transcript text the detector recognised, verbatim. Empty
	// when nothing was found.
	Banner string
	// Wait is the wait the session was parked on. Zero when nothing was found.
	Wait Wait
	// Reason is what the session was told, for `ccresu inspect` and for the
	// operator's own output.
	Reason string
}

// ErrNoParker reports a search that found a limit on a manager built without a
// LimitPark hook, so the wait it found could not be planned.
var ErrNoParker = errors.New("recovery: no limit parker is installed")

// SearchLimit looks for a usage limit in a session the operator has established
// is idle, and parks the session until the reset when one is found.
//
// It exists because RESUMED is Settled (see state.go): a resumed session is
// never re-observed by the daemon, so a limit that lands in its transcript
// after the resume goes unnoticed and the session never reaches
// WAITING_FOR_RESET. The daemon cannot fix that by looking harder — not looking
// is what keeps a finished conversation from being resumed twice — so the read
// is an operator act. `ccresu search-limit` is that act, and it is the only
// caller.
//
// The walk is RESUMED -> DISCOVERED -> WAITING_FOR_RESET, or DISCOVERED ->
// WAITING_FOR_RESET for a session that is already there: the return to discovery
// is the re-observation the machine requires before a settled session may be
// parked again, and the second edge is the one the daemon takes when its own scan
// parses a banner. A search that finds nothing writes nothing: no attempt, no
// transition, no schedule. A search on a session that is not idle is refused,
// because every other state either already has a pending pass that will read the
// transcript (RESUMING) or is a decision ccresu must not walk back (DISABLED,
// COMPLETED, UNRECOVERABLE).
func (m *Manager) SearchLimit(ctx context.Context, sessionID string, scan LimitScan) (LimitSearch, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return LimitSearch{}, ErrNoSession
	}
	if ctx == nil {
		return LimitSearch{}, errors.New("recovery: nil context")
	}
	if !m.enabled {
		return LimitSearch{SessionID: sessionID}, sessionError(ErrDisabled, sessionID)
	}
	if scan == nil {
		return LimitSearch{SessionID: sessionID}, errors.New("recovery: no limit scan supplied")
	}
	if err := claude.ValidateSessionID(sessionID); err != nil {
		return LimitSearch{SessionID: sessionID}, err
	}

	// The same per-session lock a recovery takes: a search must not race a pass
	// that is handing the session off, and a pass must not start while a search
	// is rewriting the state.
	if !m.locks.tryLock(sessionID) {
		return LimitSearch{SessionID: sessionID}, sessionError(ErrAlreadyRunning, sessionID)
	}
	defer m.locks.unlock(sessionID)

	session, err := m.store.GetSession(ctx, sessionID)
	if err != nil {
		return LimitSearch{SessionID: sessionID}, fmt.Errorf("%w: %v", ErrNotEligible, err)
	}
	out := LimitSearch{SessionID: sessionID, State: session.State}
	if !session.Enabled {
		return out, sessionError(ErrDisabled, sessionID)
	}
	if !searchable(session.State) {
		return out, fmt.Errorf("%w: session %s is %s, and only an idle session (%s or %s) can be searched",
			ErrNotEligible, sessionID, session.State, StateDiscovered, StateResumed)
	}

	banner, found, err := scan(ctx, sessionID)
	if err != nil {
		return out, fmt.Errorf("search %s: %w", sessionID, err)
	}
	if !found {
		// Nothing is written and nothing moves. A search that finds no limit is
		// an answer, so the session keeps the state it had and the caller says
		// so rather than inventing an event.
		return out, nil
	}
	out.Found = true
	out.Banner = banner

	if m.limitPark == nil {
		return out, fmt.Errorf("%w: %s cannot be parked until the reset", ErrNoParker, sessionID)
	}
	wait, err := m.limitPark(ctx, sessionID, banner)
	if err != nil {
		// A refusal path treats a parker failure as non-fatal because the
		// refusal is itself the evidence. Here the banner is the evidence and
		// the wait is the whole point of the search, so the failure is reported
		// and the state is left alone: parking a session in WAITING_FOR_RESET
		// with no schedule would leave it waiting for a reset nothing watches.
		return out, fmt.Errorf("plan the wait for %s: %w", sessionID, err)
	}
	out.Wait = wait

	state := &stateWalker{ctx: ctx, store: m.store, sessionID: sessionID, current: session.State}
	// A resumed session goes back through DISCOVERED rather than straight to the
	// park. The machine has no RESUMED -> WAITING_FOR_RESET edge on purpose (see
	// state.go): a session that was just resumed must be observed again before it
	// is parked again, or the stale banner in its tail would drive a park per
	// pass. The search is that re-observation — the operator ran the scan and
	// found the banner — so it records the return to discovery, and DISCOVERED ->
	// WAITING_FOR_RESET is then the same edge the daemon takes when its own scan
	// parses a limit.
	if state.current == StateResumed {
		if err := state.move(StateDiscovered,
			"an operator searched an idle session for a usage limit", 0); err != nil {
			return out, err
		}
	}
	reason := "waiting for the reset time"
	if wait.Known && !wait.At.IsZero() {
		reason = "waiting until " + wait.At.UTC().Format(time.RFC3339)
	}
	if err := state.move(StateWaitingForReset, reason, 0); err != nil {
		return out, err
	}
	out.State = state.current
	out.Reason = reason

	m.log.Info("usage limit found in an idle session",
		"session", sessionID, "known_reset", wait.Known, "reset_at", wait.At, "due_at", wait.At)
	return out, nil
}

// searchable reports whether a search may start from s.
//
// RESUMED is the state this exists for: it is Settled, so no daemon pass would
// otherwise re-read its transcript, and the operator is asserting the session has
// gone idle. DISCOVERED is accepted because it is the same situation one beat
// earlier — the session is known, alive and not parked. Every other state is
// refused: WAITING_FOR_RESET and RESUMING already have a pending pass that will
// read the transcript, and a search that parked a session which is already parked
// would rewrite a wait that is already correct; DISABLED, COMPLETED and
// UNRECOVERABLE are decisions ccresu must not walk back.
func searchable(s State) bool {
	switch s {
	case StateDiscovered, StateResumed:
		return true
	default:
		return false
	}
}
