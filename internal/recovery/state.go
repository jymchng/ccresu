package recovery

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jymchng/ccresu/internal/storage"
)

// State is the persisted recovery state of one session.
//
// It is an alias rather than a distinct type so a state can be passed straight
// to the storage repository with no translation layer and no chance of the two
// enumerations drifting apart.
type State = storage.State

// The states of the recovery state machine, re-exported for callers that do not
// need to import storage.
const (
	StateDiscovered         = storage.StateDiscovered
	StateRunning            = storage.StateRunning
	StateUsageLimitDetected = storage.StateUsageLimitDetected
	StateWaitingForReset    = storage.StateWaitingForReset
	StateResetTimeReached   = storage.StateResetTimeReached
	StateCheckingSession    = storage.StateCheckingSession
	StateCheckingLimit      = storage.StateCheckingLimit
	StateResuming           = storage.StateResuming
	StateResumed            = storage.StateResumed
	StateRetryWait          = storage.StateRetryWait
	StateCompleted          = storage.StateCompleted
	StateUserTerminated     = storage.StateUserTerminated
	StateDisabled           = storage.StateDisabled
	StateUnrecoverable      = storage.StateUnrecoverable
	// StateWaitingForInput is a session the supervisor reports as `blocked`: it
	// needs a permission, a sandbox decision, an answer or an expired login
	// cleared before it can continue, and nothing ccresu can do unblocks it.
	//
	// It is Pending — a restarted daemon re-observes it, because the operator
	// may have answered the prompt while ccresu was down — and it is not
	// Settled, because the conversation is not finished. What it is not is a
	// destination for a handoff: a continuation queued behind an unanswered
	// prompt is waste, so a session in this state is observed, never resumed.
	StateWaitingForInput = storage.StateWaitingForInput
	// StateSpendCapReached is the session parked because its recorded spend
	// reached a configured cap (PRD 5 R7).
	//
	// It is a budget verdict rather than a fact about the conversation, which is
	// why it is neither terminal nor settled, and why it is deliberately absent
	// from PendingStates: no reconcile pass may resume it, or the cap would be
	// defeated by rediscovery. It leaves only by an explicit operator action —
	// `ccresu recover`, `ccresu reset`, or raising the cap.
	StateSpendCapReached = storage.StateSpendCapReached
)

// ErrInvalidTransition reports an attempt to move a session along an edge that
// the state machine does not define.
var ErrInvalidTransition = errors.New("recovery: invalid state transition")

// transitions is the authoritative edge table.
//
// The happy path follows the specification exactly:
//
//	DISCOVERED -> RUNNING -> USAGE_LIMIT_DETECTED -> WAITING_FOR_RESET
//	-> RESET_TIME_REACHED -> CHECKING_SESSION -> CHECKING_LIMIT -> RESUMING
//	-> RESUMED
//
// with RETRY_WAIT looping back to CHECKING_SESSION. The remaining edges exist so
// an operator action (disable, terminate) or an off-nominal observation (already
// resumed, limit not actually cleared) is recorded instead of being forced
// through the happy path or silently dropped.
//
// RESUMING -> WAITING_FOR_RESET is the refusal edge: the CLI ran, said the usage
// limit is still in force and named the next reset. That is the same wait the
// session was already in, reached by an attempt that spent budget, so it returns
// to the waiting state rather than to RETRY_WAIT, where a backoff would ask the
// same question again a minute later.
//
// There is no terminal failure state. A session that cannot be resumed is not
// broken for ever: the daemon rediscovers it, reads its transcript again, and
// finds its next reset time. Every state that permits automatic work therefore
// has an edge back to DISCOVERED, and that is what a failed attempt or an
// exhausted attempt budget produces. DISCOVERED is the start of a new cycle, not
// the absence of one: entering it resets the attempt budget, so a session is
// retried as often as its transcript still shows a usage limit, without an
// operator running `ccresu reset`.
//
// The only state that stops automatic work on its own is UNRECOVERABLE, and only
// because ccresu has positively established that it has no verified way to
// continue the conversation. That is a fact about the session, not a budget.
var transitions = map[State]map[State]struct{}{
	StateDiscovered: {
		StateRunning:         {},
		StateCheckingSession: {}, // recover and retry start by checking the session
		StateDisabled:        {},
		StateUnrecoverable:   {},
		StateDiscovered:      {}, // a fresh cycle for a budget spent before discovery
	},
	StateRunning: {
		StateUsageLimitDetected: {},
		StateCompleted:          {},
		StateUserTerminated:     {},
		StateDisabled:           {},
		StateDiscovered:         {},
	},
	StateUsageLimitDetected: {
		StateWaitingForReset: {},
		StateUnrecoverable:   {},
		StateUserTerminated:  {},
		StateDisabled:        {},
		StateDiscovered:      {},
	},
	StateWaitingForReset: {
		// A repeat entry is legal: an unknown-reset session re-enters the
		// waiting state after every poll, and a restart re-observes the wait.
		StateWaitingForReset:  {},
		StateResetTimeReached: {},
		StateUnrecoverable:    {},
		StateUserTerminated:   {},
		StateDisabled:         {},
		StateDiscovered:       {},
	},
	StateResetTimeReached: {
		StateCheckingSession: {},
		StateUnrecoverable:   {},
		StateUserTerminated:  {},
		StateDisabled:        {},
		StateDiscovered:      {},
	},
	StateCheckingSession: {
		StateCheckingLimit:   {},
		StateResumed:         {}, // it resumed on its own; nothing to do
		StateCompleted:       {}, // it had already finished its last turn
		StateRetryWait:       {}, // the status check itself failed
		StateUnrecoverable:   {},
		StateUserTerminated:  {},
		StateDisabled:        {},
		StateDiscovered:      {},
		StateWaitingForInput: {}, // the supervisor says a human must act
	},
	StateCheckingLimit: {
		StateResuming:        {},
		StateSpendCapReached: {}, // the pre-attempt spend check refused the resume
		StateRetryWait:       {},
		StateWaitingForReset: {}, // the limit is still in force
		StateUnrecoverable:   {},
		StateUserTerminated:  {},
		StateDisabled:        {},
		StateDiscovered:      {},
	},
	StateResuming: {
		StateResumed:         {},
		StateRetryWait:       {},
		StateWaitingForReset: {}, // the resume was refused: the limit is still in force
		StateDiscovered:      {},
		StateUnrecoverable:   {},
		StateUserTerminated:  {},
		StateDisabled:        {},
		// A resume that lands on a blocked session parks instead of pretending
		// the continuation was delivered.
		StateWaitingForInput: {},
		// One attempt cost more than the per-attempt cap allows (R6).
		StateSpendCapReached: {},
	},
	StateRetryWait: {
		StateCheckingSession: {},
		StateWaitingForReset: {},
		StateDiscovered:      {},
		StateUserTerminated:  {},
		StateDisabled:        {},
	},
	// WAITING_FOR_INPUT is a park, not a verdict: the supervisor published
	// `blocked`, so ccresu cannot act, but the conversation is not finished. The
	// state therefore needs every edge a live state has — including DISCOVERED,
	// so a failure while parked can still be recorded — plus itself, because a
	// pass that observes `blocked` again re-enters the same state and writes the
	// audit row. It is deliberately not settled (a restarted daemon must
	// re-observe it) and deliberately not resumable from (a continuation would
	// queue behind a prompt nobody is there to answer).
	StateWaitingForInput: {
		StateRunning:         {},
		StateResumed:         {},
		StateRetryWait:       {},
		StateUserTerminated:  {},
		StateDisabled:        {},
		StateUnrecoverable:   {},
		StateDiscovered:      {},
		StateWaitingForInput: {},
	},
	// SPEND_CAP_REACHED is a park with a budget reason. It offers CHECKING_SESSION
	// so an explicit `ccresu recover` can walk back into the normal path — R11
	// exempts a human act from the cap — and DISCOVERED so raising the cap
	// returns the session to the ordinary cycle without a resume being issued
	// from here. It offers no edge to RESUMING: nothing in this state may spend
	// by itself.
	StateSpendCapReached: {
		StateCheckingSession: {},
		StateDiscovered:      {},
		StateUserTerminated:  {},
		StateDisabled:        {},
	},
	// RESUMED is not strictly terminal — a resumed session can hit its limit
	// again — but a second recovery is only legal after the session has been
	// observed running and hit a new limit, never straight from RESUMED.
	StateResumed: {
		StateRunning:        {},
		StateCompleted:      {},
		StateUserTerminated: {},
		StateDisabled:       {},
	},
	StateCompleted:      {},
	StateUserTerminated: {},
	StateDisabled: {
		StateRunning:        {},
		StateDiscovered:     {},
		StateUserTerminated: {},
	},
	StateUnrecoverable: {},
}

// CanTransition reports whether from -> to is a legal edge.
func CanTransition(from, to State) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	_, ok := transitions[from][to]
	return ok
}

// TransitionsFrom returns the states reachable from from, sorted for stable
// output in error messages and in the CLI.
func TransitionsFrom(from State) []State {
	allowed := transitions[from]
	out := make([]State, 0, len(allowed))
	for to := range allowed {
		out = append(out, to)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ValidateTransition returns ErrInvalidTransition, naming the legal targets,
// when from -> to is not allowed.
func ValidateTransition(from, to State) error {
	if CanTransition(from, to) {
		return nil
	}
	if !from.Valid() {
		return fmt.Errorf("%w: %q is not a known state", ErrInvalidTransition, from)
	}
	targets := TransitionsFrom(from)
	names := make([]string, 0, len(targets))
	for _, target := range targets {
		names = append(names, string(target))
	}
	if len(names) == 0 {
		return fmt.Errorf("%w: %s is terminal", ErrInvalidTransition, from)
	}
	return fmt.Errorf("%w: %s -> %s (legal targets: %s)",
		ErrInvalidTransition, from, to, strings.Join(names, ", "))
}

// Terminal reports whether no further transition is defined from s, i.e. the
// session will not move again without an operator explicitly restarting it.
func Terminal(s State) bool {
	return s.Valid() && len(transitions[s]) == 0
}

// Settled reports whether a session in state s must be left alone: it finished,
// was terminated, was disabled or proved unrecoverable. Recovery refuses to
// touch a settled session, which is what stops a completed conversation from
// being resumed again.
//
// WAITING_FOR_INPUT is deliberately absent: the conversation is not finished,
// so ccresu must keep observing it until the operator unblocks it.
func Settled(s State) bool {
	switch s {
	case StateResumed, StateCompleted, StateUserTerminated, StateDisabled, StateUnrecoverable:
		return true
	default:
		return false
	}
}

// Pending reports whether a session in state s still wants automatic work: it
// is waiting for a reset, being checked, resuming, waiting to retry, or parked
// because the supervisor published `blocked`. These are the states a restarted
// daemon must pick up.
//
// WAITING_FOR_INPUT is pending even though ccresu cannot act on it: the
// conversation is not finished, and the pass that re-observes it is what notices
// the operator unblocked it. Dropping it here would strand a parked session
// across a restart, which is the failure R11 exists to prevent. Being pending is
// not the same as being resumable — a session in this state is observed, never
// handed off.
func Pending(s State) bool {
	switch s {
	case StateUsageLimitDetected, StateWaitingForReset, StateResetTimeReached,
		StateCheckingSession, StateCheckingLimit, StateResuming, StateRetryWait,
		StateWaitingForInput:
		return true
	default:
		return false
	}
}

// PendingStates returns every state Pending accepts, in machine order.
func PendingStates() []State {
	return []State{
		StateUsageLimitDetected,
		StateWaitingForReset,
		StateResetTimeReached,
		StateCheckingSession,
		StateCheckingLimit,
		StateResuming,
		StateRetryWait,
		StateWaitingForInput,
	}
}

// Failed reports whether a session in state s has failed in a way that needs an
// operator. Only UNRECOVERABLE qualifies: a spent attempt budget returns the
// session to DISCOVERED and is retried on the next discovery cycle, so it is a
// pause, not a failure.
func Failed(s State) bool {
	return s == StateUnrecoverable
}
