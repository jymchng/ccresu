package recovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
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
	// when a limit was found and parked or re-parked, otherwise the state it
	// started in.
	State State
	// Banner is the transcript text the detector recognised, verbatim. Empty
	// when nothing was found.
	Banner string
	// Wait is the wait the session is parked on: the one that was just planned
	// when Applied, otherwise the zero Wait, because a read-only search plans
	// nothing.
	Wait Wait
	// Reason is what the session was told, for `ccresu inspect` and for the
	// operator's own output.
	Reason string
	// Applied reports whether the search wrote anything: the wait was planned,
	// recorded and the park transition taken. It is false for a search that found
	// nothing and for one whose session does not take a park (see
	// searchApplies); in both cases the transcript was read and the question was
	// answered, which is why neither is an error.
	Applied bool
}

// ErrNoParker reports a search that found a limit on a manager built without a
// LimitPark hook, so the wait it found could not be planned.
var ErrNoParker = errors.New("recovery: no limit parker is installed")

// SearchLimit reads a session's transcript for a usage limit and, when one is
// found, parks the session until the reset.
//
// It exists because RESUMED is Settled (see state.go): a resumed session is
// never re-observed by the daemon, so a limit that lands in its transcript
// after the resume goes unnoticed and the session never reaches
// WAITING_FOR_RESET. The daemon cannot fix that by looking harder — not looking
// is what keeps a finished conversation from being resumed twice — so the read
// is an operator act. `ccresu search-limit` is that act, and it is the only
// caller.
//
// # The read is unconditional; only the park is gated
//
// Every state is searched. Parsing a banner is a question about the transcript
// — "what reset does this session's limit name?" — and refusing to look because
// of the state a session happens to be in answers "I will not tell you" to the
// one question the command exists to answer. The state that matters most is
// WAITING_FOR_RESET: a session already parked on a wait whose instant is wrong,
// unparsed or stale is exactly the session whose reset time an operator needs
// re-parsed, and that is the state an eligibility gate used to refuse.
//
// What the state decides is whether the search may *write*:
//
//   - DISCOVERED and RESUMED are parkable: a found banner plans the wait and
//     parks the session (RESUMED through DISCOVERED, see below).
//   - WAITING_FOR_RESET is already parked, so the wait is re-planned from the
//     banner that is in the transcript now and the schedule is corrected: this
//     is how an operator repairs a reset time instead of waiting out a wrong
//     one. The state cannot move, so the re-parse is recorded as the self-edge
//     WAITING_FOR_RESET -> WAITING_FOR_RESET, and only when the instant actually
//     changed, so pressing the key twice does not fill the history with
//     identical rows.
//   - RESUMING already has a pass that will read the transcript, and COMPLETED,
//     DISABLED and UNRECOVERABLE are decisions ccresu must not walk back. Those
//     are read-only: the banner is reported, nothing is written, and the result
//     says so. A disabled session is read-only for the same reason — turning the
//     loop back on is `ccresu enable`'s job, not a search's.
//
// A search that finds nothing writes nothing: no attempt, no transition, no
// schedule. Its exit is still success, because an answer is not a failure.
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

	if !searchApplies(session) {
		out.Reason = searchReadOnlyReason(session)
		return out, nil
	}

	if m.limitPark == nil {
		return out, fmt.Errorf("%w: %s cannot be parked until the reset", ErrNoParker, sessionID)
	}

	// The instant the stored schedule was waiting for, read before the parker
	// overwrites it, so a re-parse can say whether it changed the wait rather
	// than only that it looked.
	priorAt, hadPrior := m.storedDueAt(ctx, sessionID)

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
	out.Applied = true

	reason := "waiting for the reset time"
	if wait.Known && !wait.At.IsZero() {
		reason = "waiting until " + wait.At.UTC().Format(time.RFC3339)
	}

	state := &stateWalker{ctx: ctx, store: m.store, sessionID: sessionID, current: session.State}
	switch session.State {
	case StateResumed:
		// A resumed session goes back through DISCOVERED rather than straight to
		// the park. The machine has no RESUMED -> WAITING_FOR_RESET edge on
		// purpose (see state.go): a session that was just resumed must be
		// observed again before it is parked again, or the stale banner in its
		// tail would drive a park per pass. The search is that re-observation —
		// the operator ran the scan and found the banner — so it records the
		// return to discovery, and DISCOVERED -> WAITING_FOR_RESET is then the
		// same edge the daemon takes when its own scan parses a limit.
		if err := state.move(StateDiscovered,
			"an operator searched an idle session for a usage limit", 0); err != nil {
			return out, err
		}
		if err := state.move(StateWaitingForReset, reason, 0); err != nil {
			return out, err
		}
	case StateWaitingForReset:
		// Already parked: the parker rewrote the wait from the banner that is in
		// the transcript now, which is the point of searching a waiting session —
		// the stored reset may be stale, parsed from a different banner, or a poll
		// of an unknown reset that the transcript has since named. The state is
		// where it needs to be, so only a changed instant is worth a row.
		if !hadPrior || !priorAt.Equal(wait.At) {
			if err := state.move(StateWaitingForReset,
				"an operator re-parsed the reset time from the transcript", 0); err != nil {
				return out, err
			}
			reason = "the reset time was re-parsed: " + reason
		} else {
			reason = "the stored reset already matches the banner: " + reason
		}
	default: // DISCOVERED
		if err := state.move(StateWaitingForReset, reason, 0); err != nil {
			return out, err
		}
	}
	out.State = state.current
	out.Reason = reason

	m.log.Info("usage limit found in a search",
		"session", sessionID, "state", session.State,
		"known_reset", wait.Known, "reset_at", wait.At, "due_at", wait.At, "applied", out.Applied)
	return out, nil
}

// storedDueAt reports the instant the session's stored schedule is waiting for.
//
// It exists so a re-parse can tell a corrected wait from an unchanged one. A
// missing schedule or a schedule with no deadline reports false: there was no
// wait to compare against, so whatever the search planned is a change.
func (m *Manager) storedDueAt(ctx context.Context, sessionID string) (time.Time, bool) {
	sched, err := m.store.GetSchedule(ctx, sessionID)
	if err != nil {
		return time.Time{}, false
	}
	if sched.Unknown {
		if sched.NextPollAt.IsZero() {
			return time.Time{}, false
		}
		return sched.NextPollAt.UTC(), true
	}
	if sched.ResetAt.IsZero() {
		return time.Time{}, false
	}
	return sched.ResetAt.UTC(), true
}

// searchApplies reports whether a search may write to session, as opposed to
// only reading its transcript.
//
// Writing means planning a wait and recording it, so it is limited to the states
// where a wait is the right answer: the idle states a search exists for
// (RESUMED, DISCOVERED) and WAITING_FOR_RESET, where the point of the search is
// to correct the wait that is already there. RESUMING is excluded because a pass
// is already going to read the transcript and two writers would fight over the
// schedule; the verdicts (COMPLETED, DISABLED, UNRECOVERABLE) are excluded
// because ccresu must not walk back a decision, and a disabled session is an
// operator's decision even when its state would otherwise allow a park.
func searchApplies(session storage.Session) bool {
	if !session.Enabled {
		return false
	}
	switch session.State {
	case StateDiscovered, StateResumed, StateWaitingForReset:
		return true
	default:
		return false
	}
}

// searchReadOnlyReason explains why a search read the transcript but wrote
// nothing. The words are the ones the operator sees, so each state says what it
// means for them rather than restating the state's name.
func searchReadOnlyReason(session storage.Session) string {
	if !session.Enabled {
		return "the banner was read and nothing was recorded: the session is disabled, so run `ccresu enable` before a wait can be planned"
	}
	switch session.State {
	case StateResuming:
		return "the banner was read and nothing was recorded: a recovery is in progress and reads the transcript itself"
	case StateCompleted:
		return "the banner was read and nothing was recorded: the session completed"
	case StateUnrecoverable:
		return "the banner was read and nothing was recorded: the session cannot be recovered"
	default:
		return "the banner was read and nothing was recorded: this state does not take a park"
	}
}
