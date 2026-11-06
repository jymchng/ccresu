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
	StateWorking            = storage.StateWorking
	StateResumed            = storage.StateResumed
	StateRetryWait          = storage.StateRetryWait
	StateCompleted          = storage.StateCompleted
	StateUserTerminated     = storage.StateUserTerminated
	StateDisabled           = storage.StateDisabled
	StateUnrecoverable      = storage.StateUnrecoverable
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
// There is no terminal failure state. A session that cannot be resumed is not
// broken for ever: the daemon rediscovers it, reads its transcript again, and
// finds its next reset time. Every state that permits automatic work therefore
// has an edge back to DISCOVERED — RESUMED included, because a resumed
// conversation is revisited on the next discovery pass, not treated as the end
// of the line — and that is what a failed attempt or an exhausted attempt budget
// produces. DISCOVERED is the start of a new cycle, not
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
		StateCheckingLimit:  {},
		StateResumed:        {}, // it resumed on its own; nothing to do
		StateRetryWait:      {}, // the status check itself failed
		StateUnrecoverable:  {},
		StateUserTerminated: {},
		StateDisabled:       {},
		StateDiscovered:     {},
	},
	StateCheckingLimit: {
		StateResuming:        {},
		StateRetryWait:       {},
		StateWaitingForReset: {}, // the limit is still in force
		StateUnrecoverable:   {},
		StateUserTerminated:  {},
		StateDisabled:        {},
		StateDiscovered:      {},
	},
	StateResuming: {
		// The dispatch answered with a job id, and that job is still working.
		// WORKING is where the row waits for it: the job is real work in
		// progress, which is neither "resumed" nor a failure.
		StateWorking:        {},
		StateResumed:        {},
		StateRetryWait:      {},
		StateDiscovered:     {},
		StateUnrecoverable:  {},
		StateUserTerminated: {},
		StateDisabled:       {},
	},
	// WORKING is the state of a session whose background job has not stopped
	// working yet. Everything a resume could end in is reachable from it,
	// because the job's outcome is only known once it stops.
	StateWorking: {
		StateResumed:        {},
		StateRetryWait:      {},
		StateDiscovered:     {},
		StateUnrecoverable:  {},
		StateUserTerminated: {},
		StateDisabled:       {},
	},
	StateRetryWait: {
		StateCheckingSession: {},
		StateWaitingForReset: {},
		StateDiscovered:      {},
		StateUserTerminated:  {},
		StateDisabled:        {},
	},
	// RESUMED records that a recovery completed, not that the conversation is
	// over. The resumed conversation keeps running under its continuation
	// session, so a second recovery is legal straight from RESUMED: the discovery
	// pass revisits a resumed session, reads its transcript again, and either
	// finds the next usage limit (a new wait) or parks it with a next-attempt
	// instant. Nothing has to observe the session running first — ccresu is not
	// attached to it and cannot.
	StateResumed: {
		StateRunning: {},
		// The cycle repeats: a resumed conversation keeps running under the
		// continuation session, its transcript is read again, and the next usage
		// limit it hits starts a new wait. Without these edges the loop would
		// stop after one recovery.
		StateUsageLimitDetected: {},
		StateWaitingForReset:    {},
		// The rediscovery edge. A RESUMED session is re-examined from the top of
		// the cycle with a fresh attempt budget, which is the only way the loop
		// can run a second time without an operator running `ccresu recover`.
		StateDiscovered: {},
		// A repeat entry is legal here for the same reason it is legal on
		// WAITING_FOR_RESET: the rediscovery stage re-observes a resumed session
		// every pass, and a continuation that is still producing output must be
		// allowed to record that it was looked at and deferred rather than being
		// moved along as if it were idle. Keeping the row RESUMED while it works
		// is what stops a second resume being dispatched for a conversation that
		// is already running.
		StateResumed:        {},
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
// RESUMED is settled for the recovery engine — it has already dispatched and
// verified that resume, and issuing another would duplicate it — but it is not
// the end of the cycle: the discovery pass revisits it (RESUMED has an edge to
// DISCOVERED) and starts the next cycle under the continuation session.
func Settled(s State) bool {
	switch s {
	// WORKING is not settled in the sense of "finished", but it is settled in the
	// sense this function reports: automatic recovery must not touch it. The job
	// is already working on the conversation, so issuing another resume would
	// duplicate the work. The follow stage polls it, not the recovery engine.
	case StateWorking, StateResumed, StateCompleted, StateUserTerminated, StateDisabled, StateUnrecoverable:
		return true
	default:
		return false
	}
}

// Pending reports whether a session in state s still wants automatic work: it
// is waiting for a reset, being checked, resuming, or waiting to retry. These
// are the states a restarted daemon must pick up.
func Pending(s State) bool {
	switch s {
	case StateUsageLimitDetected, StateWaitingForReset, StateResetTimeReached,
		StateCheckingSession, StateCheckingLimit, StateResuming, StateWorking,
		StateRetryWait:
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
		StateWorking,
		StateRetryWait,
	}
}

// Failed reports whether a session in state s has failed in a way that needs an
// operator. Only UNRECOVERABLE qualifies: a spent attempt budget returns the
// session to DISCOVERED and is retried on the next discovery cycle, so it is a
// pause, not a failure.
func Failed(s State) bool {
	return s == StateUnrecoverable
}
