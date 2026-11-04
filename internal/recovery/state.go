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
	StatePermanentlyFailed  = storage.StatePermanentlyFailed
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
// with RETRY_WAIT looping back to CHECKING_SESSION, and PERMANENTLY_FAILED once
// the attempt budget is spent. The remaining edges exist so an operator action
// (disable, terminate, retry) or an off-nominal observation (already resumed,
// limit not actually cleared) is recorded instead of being forced through the
// happy path or silently dropped.
var transitions = map[State]map[State]struct{}{
	StateDiscovered: {
		StateRunning:       {},
		StateDisabled:      {},
		StateUnrecoverable: {},
	},
	StateRunning: {
		StateUsageLimitDetected: {},
		StateCompleted:          {},
		StateUserTerminated:     {},
		StateDisabled:           {},
	},
	StateUsageLimitDetected: {
		StateWaitingForReset: {},
		StateUnrecoverable:   {},
		StateUserTerminated:  {},
		StateDisabled:        {},
	},
	StateWaitingForReset: {
		// A repeat entry is legal: an unknown-reset session re-enters the
		// waiting state after every poll, and a restart re-observes the wait.
		StateWaitingForReset:  {},
		StateResetTimeReached: {},
		StateUnrecoverable:    {},
		StateUserTerminated:   {},
		StateDisabled:         {},
	},
	StateResetTimeReached: {
		StateCheckingSession: {},
		StateUnrecoverable:   {},
		StateUserTerminated:  {},
		StateDisabled:        {},
	},
	StateCheckingSession: {
		StateCheckingLimit:  {},
		StateResumed:        {}, // it resumed on its own; nothing to do
		StateRetryWait:      {}, // the status check itself failed
		StateUnrecoverable:  {},
		StateUserTerminated: {},
		StateDisabled:       {},
	},
	StateCheckingLimit: {
		StateResuming:        {},
		StateRetryWait:       {},
		StateWaitingForReset: {}, // the limit is still in force
		StateUnrecoverable:   {},
		StateUserTerminated:  {},
		StateDisabled:        {},
	},
	StateResuming: {
		StateResumed:           {},
		StateRetryWait:         {},
		StatePermanentlyFailed: {},
		StateUnrecoverable:     {},
		StateUserTerminated:    {},
		StateDisabled:          {},
	},
	StateRetryWait: {
		StateCheckingSession:   {},
		StateWaitingForReset:   {},
		StatePermanentlyFailed: {},
		StateUserTerminated:    {},
		StateDisabled:          {},
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
	StatePermanentlyFailed: {
		StateCheckingSession: {}, // an operator asked for another go
		StateUserTerminated:  {},
		StateDisabled:        {},
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
func Settled(s State) bool {
	switch s {
	case StateResumed, StateCompleted, StateUserTerminated, StateDisabled, StateUnrecoverable:
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
		StateCheckingSession, StateCheckingLimit, StateResuming, StateRetryWait:
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
	}
}

// Failed reports whether a session in state s has failed in a way that needs an
// operator: either the attempt budget is spent or the session can never resume.
func Failed(s State) bool {
	return s == StatePermanentlyFailed || s == StateUnrecoverable
}
