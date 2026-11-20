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
//
// There are seven, because ccresu does one thing: it reads a session's
// transcript, waits for the usage limit the banner names to reset, hands the
// session a continuation, and goes back to reading. Those four beats are the
// whole of it, and three verdicts stop it.
const (
	// StateDiscovered is the first beat: a known session whose transcript the
	// scan reads. It is where a lap starts and where a lap that found nothing
	// stays.
	StateDiscovered = storage.StateDiscovered
	// StateWaitingForReset is the second beat: a usage limit was parsed out of
	// the transcript and the session is parked on the instant it named.
	StateWaitingForReset = storage.StateWaitingForReset
	// StateResuming is the third beat: the instant arrived and the continuation
	// is being handed to the backgrounded session.
	StateResuming = storage.StateResuming
	// StateResumed is the fourth beat: the handoff was verified and the session
	// is alive again.
	//
	// It exists as a state, rather than as an immediate return to DISCOVERED,
	// for one reason: the banner that parked the session is still inside the
	// transcript tail the detector reads, so a session that went straight back to
	// DISCOVERED would be re-parked and re-resumed off its own stale banner.
	StateResumed = storage.StateResumed
	// StateCompleted is a verdict: the session finished its last turn, so there
	// is nothing to continue.
	StateCompleted = storage.StateCompleted
	// StateDisabled is the loop's off switch: the operator turned the session
	// off.
	StateDisabled = storage.StateDisabled
	// StateUnrecoverable is a verdict: ccresu has positively established that it
	// has no verified way to resume this session.
	StateUnrecoverable = storage.StateUnrecoverable
)

// ErrInvalidTransition reports an attempt to move a session along an edge that
// the state machine does not define.
var ErrInvalidTransition = errors.New("recovery: invalid state transition")

// transitions is the authoritative edge table.
//
// # The loop
//
// One lap is four beats and it repeats for as long as the session keeps hitting
// its limit:
//
//	DISCOVERED -> WAITING_FOR_RESET -> RESUMING -> RESUMED -> DISCOVERED
//
// Each beat does exactly one thing. DISCOVERED reads the transcript and parses
// the reset time out of a limit banner. WAITING_FOR_RESET sits on that instant
// with the wait on a persisted schedule, so a restart or a reboot resumes
// waiting instead of losing the session. RESUMING spends an attempt handing the
// backgrounded session a continuation. RESUMED is the landing beat, and the
// edge back to DISCOVERED is what makes the loop a loop: the next scan notices
// the next limit and the lap starts again.
//
// WAITING_FOR_RESET -> WAITING_FOR_RESET is a re-entry rather than progress: a
// banner that named no reset time is polled, and every poll re-parks the session
// on the next instant.
//
// # The four exits
//
// Every live beat — DISCOVERED, WAITING_FOR_RESET, RESUMING and RESUMED — has
// the same four ways out, so no beat can strand a session:
//
//   - DISCOVERED, the way back into the loop. A failed attempt, a spent attempt
//     budget and a wait that no longer applies all return here rather than to a
//     failure state, which is why the loop needs no operator to keep going.
//   - COMPLETED, DISABLED and UNRECOVERABLE, the three verdicts that stop it.
//
// # The edges that are deliberately absent
//
// RESUMED has no edge to RESUMING or to WAITING_FOR_RESET. A session that was
// just resumed must be observed again before it is parked again, or the stale
// banner in its tail would drive a resume per pass.
//
// # The two observing edges into RESUMED
//
// DISCOVERED -> RESUMED and WAITING_FOR_RESET -> RESUMED exist because a pass can
// find the session already alive: the supervisor publishes `working`, `done`, or
// `blocked` with a handoff outstanding, which is a session that continued on its
// own once the window reset. No continuation is handed over and no attempt is
// spent on that path, so it is an observation rather than a resume, and both
// edges are entered only from the published state — never from a command.
//
// That is also why they do not weaken the stale-banner guard above: the guard is
// about RESUMED's outgoing edges, and those are unchanged.
//
// DISCOVERED -> RESUMING is the operator's way in: `ccresu recover` and
// `ccresu retry` ask for a resume now instead of waiting for a parsed instant.
// It is the same loop, entered by hand, and it spends an attempt like any other.
//
// DISABLED -> DISCOVERED is the only edge out of a verdict, and only
// `ccresu enable` takes it: turning the loop back on for a session is a decision
// a human makes, never something a pass does on its own.
var transitions = map[State]map[State]struct{}{
	StateDiscovered: {
		StateWaitingForReset: {}, // a limit was parsed out of the transcript
		StateResuming:        {}, // the operator asked for a resume now
		StateResumed:         {}, // the supervisor says it is alive again already
		StateDiscovered:      {}, // the scan found nothing; a lap that keeps going
		StateCompleted:       {}, // it finished its last turn
		StateDisabled:        {}, // the operator turned it off
		StateUnrecoverable:   {}, // there is no verified way to resume it
	},
	StateWaitingForReset: {
		StateWaitingForReset: {}, // an unknown reset is polled again
		StateResuming:        {}, // the parsed instant arrived
		StateResumed:         {}, // it resumed itself once the window reset
		StateDiscovered:      {}, // the wait no longer applies; a new lap
		StateCompleted:       {},
		StateDisabled:        {},
		StateUnrecoverable:   {},
	},
	StateResuming: {
		StateResumed:         {}, // the handoff was verified
		StateWaitingForReset: {}, // refused: the limit is still in force
		StateDiscovered:      {}, // the attempt failed or its budget is spent
		StateCompleted:       {}, // it finished while the continuation was prepared
		StateDisabled:        {},
		StateUnrecoverable:   {},
	},
	StateResumed: {
		StateDiscovered:    {}, // the next limit was noticed: the lap restarts
		StateCompleted:     {},
		StateDisabled:      {},
		StateUnrecoverable: {},
	},
	// COMPLETED and UNRECOVERABLE are the ends of the line: no pass and no
	// command moves a session out of them.
	StateCompleted: {},
	// DISABLED offers exactly one edge, back to DISCOVERED, and only an operator
	// takes it. Nothing automatic ever resumes a disabled session.
	StateDisabled: {
		StateDiscovered: {},
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
//
// A state that does not exist at all is named as such rather than reported as a
// missing edge: with the machine this small, the likeliest way to reach this
// function with a bad state is a value left over from an older version, and
// "WAITING_FOR_INPUT is not a known state" says that; a list of legal targets
// would suggest the state exists and only the edge is wrong.
func ValidateTransition(from, to State) error {
	if CanTransition(from, to) {
		return nil
	}
	if !from.Valid() {
		return fmt.Errorf("%w: %q is not a known state", ErrInvalidTransition, from)
	}
	if !to.Valid() {
		return fmt.Errorf("%w: %s -> %q: the target is not a known state", ErrInvalidTransition, from, to)
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

// Terminal reports whether the loop stops in s.
//
// COMPLETED and UNRECOVERABLE are the ends of the line: the table defines no
// edge out of them at all. DISABLED is terminal in the sense that matters to
// every automatic caller — nothing resumes, polls or waits from here — and it
// has exactly one edge, DISABLED -> DISCOVERED, which only `ccresu enable`
// takes.
func Terminal(s State) bool {
	switch s {
	case StateCompleted, StateDisabled, StateUnrecoverable:
		return true
	default:
		return false
	}
}

// Settled reports whether recovery must leave a session in state s alone.
//
// RESUMED is settled on purpose. A resumed session is alive and needs nothing
// from the engine; the transcript scan still reads it, which is how the next
// limit is noticed, but no recovery pass may act on it. That separation is what
// stops a just-resumed session from being resumed again.
func Settled(s State) bool {
	switch s {
	case StateResumed, StateCompleted, StateDisabled, StateUnrecoverable:
		return true
	default:
		return false
	}
}

// Pending reports whether a session in state s still wants automatic work, i.e.
// whether a restarted daemon must pick it up: it is parked on a reset, or a
// resume is in flight.
//
// DISCOVERED is not pending. The scan reads every discovered session on its own
// schedule, and treating it as pending would make the recovery pass resume
// sessions that never showed a limit.
func Pending(s State) bool {
	switch s {
	case StateWaitingForReset, StateResuming:
		return true
	default:
		return false
	}
}

// PendingStates returns every state Pending accepts, in machine order.
func PendingStates() []State {
	return []State{
		StateWaitingForReset,
		StateResuming,
	}
}

// Failed reports whether a session in state s has failed in a way that needs an
// operator. Only UNRECOVERABLE qualifies: a spent attempt budget and a failed
// attempt both return the session to DISCOVERED, so they are a pause, not a
// failure.
func Failed(s State) bool {
	return s == StateUnrecoverable
}
