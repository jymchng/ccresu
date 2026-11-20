package recovery

import (
	"fmt"
	"strings"
	"time"

	"github.com/jymchng/ccresu/internal/storage"
)

// StateObservation is what the supervisor published about one session at one
// instant, verbatim.
//
// Every field is copied from the `claude agents --json --all` entry and nothing
// is inferred: State, Status and WaitingFor are the raw values (lower-cased by
// the tolerant parser and never coerced afterwards), and InListing records
// whether the conversation appeared in the listing at all. That last field is
// the one the merged discovery record cannot carry, and the reason this type
// exists rather than a bare string: "the supervisor said nothing" and "the
// supervisor is not running" must not become "the session is fine".
type StateObservation struct {
	// SessionID is the conversation the observation is about.
	SessionID string
	// State is the published background state: working, blocked, done, failed,
	// stopped, or any newer value ccresu does not know.
	State string
	// Status is the published process status: busy, waiting, idle, or absent
	// when the process is not alive.
	Status string
	// WaitingFor is the published reason a waiting session waits, verbatim.
	// Empty means the CLI reported none, which is the common case: a live
	// payload shows `blocked` entries with no waitingFor at all.
	WaitingFor string
	// ObservedAt is when the listing was read.
	ObservedAt time.Time
	// InListing reports whether the session appeared in the listing.
	InListing bool
}

// Recorded reports whether an observation was actually made.
func (o StateObservation) Recorded() bool { return !o.ObservedAt.IsZero() }

// Decision is what an observation means for the session, given whether a handoff
// is outstanding.
//
// State is the state to move to, and the zero value means "no transition": the
// observation answered the question without settling anything, so the attempt
// stays open and the next pass looks again. That is a decision, not an omission
// — a session still working after a handoff is exactly the case PRD 1's handoff
// owns, and failing it for not having finished would undo the handoff.
type Decision struct {
	// State is the state to move to, or "" for no transition.
	State storage.State
	// Observed reports that the supervisor published something for this
	// session.
	Observed bool
	// HandoffUnobserved reports that a handoff is outstanding and the session
	// is missing from the listing (R8). It is its own outcome so it can be
	// retried and reported without ever being read as a failure or a success.
	HandoffUnobserved bool
	// WaitingFor is the verbatim published reason, empty when none was
	// reported.
	WaitingFor string
	// WaitingForReported distinguishes an absent reason from an empty one, so a
	// message can say "no reason was reported" rather than inventing one.
	WaitingForReported bool
	// Reason explains the decision in one clause, for the transition history.
	Reason string
}

// Transition reports whether the decision moves the session.
func (d Decision) Transition() bool { return d.State != "" }

// Contradiction is not a thing this mapping can produce, and that is the point:
// there is no fall-through case. Every documented value is matched explicitly,
// the two cases that are not values (no state, not listed) are matched
// explicitly, and anything else is handled by the default arm as an unknown
// value that is retried rather than guessed at. A new CLI value therefore cannot
// become a success: it becomes a retry with the raw value recorded.

// Observe maps a published observation onto a state, totally.
//
// handoffOutstanding is an argument rather than something the function infers
// (R5), because the same published state means different things before and after
// a handoff: `done` before one means there is nothing left to continue, and
// `done` after one means the continuation finished the work. stopIssuedByCCRESU
// is the second argument for the same reason: `stopped` covers Ctrl+X, an
// external kill *and* a session that ended while the background service was off,
// so only a stop ccresu itself issued is the operator's decision.
//
// The documented table, in order:
//
//	working  | no handoff: RESUMED (it continued by itself)
//	         | handoff:    no transition; the attempt stays open
//	blocked  | handoff:    RESUMED; the continuation landed and the session is
//	         |             waiting for a human to answer it
//	         | no handoff: no transition; the pass resumes it anyway
//	done     | no handoff: COMPLETED (nothing to continue)
//	         | handoff:    RESUMED
//	failed   | either:     WAITING_FOR_RESET (parked and retried)
//	stopped  | either:     WAITING_FOR_RESET, or DISABLED when ccresu stopped it
//	absent, in the listing     | no handoff: no transition (state is for
//	                           |   background sessions only)
//	                           | handoff: HandoffUnobserved, WAITING_FOR_RESET
//	absent, not in the listing | same, and recorded as a missing session
//	anything else              | WAITING_FOR_RESET with the raw value recorded
//
// `blocked` is the row that changed. It used to park the session in a
// waiting-for-input state and refuse to hand it a continuation, on the theory
// that a continuation queued behind an unanswered prompt is waste. It is not
// waste: the prompt is the operator's to answer whenever they get to it, the
// conversation is intact, and refusing to continue meant a blocked session was
// never resumed at all. ccresu's job is to keep the loop turning, so a blocked
// session is handed its continuation like any other. What the supervisor said is
// still recorded verbatim, in the attempt's observation and in the transition
// reason; it is simply no longer a reason to stop.
//
// The absent-without-a-handoff row is the one place the mapping deliberately
// does not settle anything: a session that has never been a background session
// publishes no state, and treating that as a verdict would refuse to recover
// every session on its first pass. Absence only means something once ccresu has
// something to be absent (R8).
func Observe(obs StateObservation, handoffOutstanding, stopIssuedByCCRESU bool) Decision {
	state := strings.ToLower(strings.TrimSpace(obs.State))
	decision := Decision{
		Observed:           obs.InListing,
		WaitingFor:         obs.WaitingFor,
		WaitingForReported: strings.TrimSpace(obs.WaitingFor) != "",
	}
	missing := func() Decision {
		if !handoffOutstanding {
			decision.Reason = "the supervisor published no state for this session, and no handoff is outstanding"
			return decision
		}
		decision.State = storage.StateWaitingForReset
		decision.HandoffUnobserved = true
		if obs.InListing {
			decision.Reason = "the supervisor listed the session but published no state for it"
		} else {
			decision.Reason = "the session was not in the supervisor's listing"
		}
		return decision
	}

	switch state {
	case "working":
		if handoffOutstanding {
			decision.Reason = "the handed-off session is working; the supervisor owns it"
			return decision
		}
		decision.State = storage.StateResumed
		decision.Reason = "the session is working, so it continued on its own once the window reset"
		return decision
	case "blocked":
		// A blocked session is alive, and the reason it is waiting is a human's
		// to answer, not ccresu's to wait on. With a handoff outstanding the
		// continuation landed and the session is RESUMED; without one the pass
		// falls through and hands it a continuation like any other session.
		if handoffOutstanding {
			decision.State = storage.StateResumed
			decision.Reason = "the handed-off session is blocked, so the continuation landed and is waiting for a human"
			if decision.WaitingForReported {
				decision.Reason = fmt.Sprintf("the handed-off session is blocked on %s, so the continuation landed", decision.WaitingFor)
			}
			return decision
		}
		decision.Reason = "the session is blocked, which is a human's to answer; resuming it anyway"
		if decision.WaitingForReported {
			decision.Reason = fmt.Sprintf("the session is blocked on %s, which is a human's to answer; resuming it anyway", decision.WaitingFor)
		}
		return decision
	case "done":
		if handoffOutstanding {
			decision.State = storage.StateResumed
			decision.Reason = "the handed-off session finished its turn, so the continuation landed"
			return decision
		}
		decision.State = storage.StateCompleted
		decision.Reason = "the session had already finished its last turn, so there was nothing to continue"
		return decision
	case "failed":
		decision.State = storage.StateWaitingForReset
		decision.Reason = "the supervisor reports the session's task ended with an error, so it is parked and retried"
		return decision
	case "stopped":
		if stopIssuedByCCRESU {
			decision.State = storage.StateDisabled
			decision.Reason = "ccresu stopped the session, so it was the operator's decision"
			return decision
		}
		decision.State = storage.StateWaitingForReset
		decision.Reason = "the session was stopped outside ccresu, which the supervisor also reports when the service was off"
		return decision
	case "":
		return missing()
	default:
		decision.State = storage.StateWaitingForReset
		decision.Reason = fmt.Sprintf("the supervisor published an unrecognised state %q, which is retried rather than guessed", obs.State)
		return decision
	}
}
