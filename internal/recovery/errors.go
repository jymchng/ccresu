package recovery

import (
	"errors"
	"fmt"
	"time"

	"github.com/jymchng/ccresu/internal/storage"
)

// Errors reported by the recovery manager. Each one is wrapped with the session
// id so a CLI message points at the session without the caller rebuilding it.
var (
	// ErrNoSession reports a recovery request with no session id.
	ErrNoSession = errors.New("recovery: session id is required")
	// ErrNoProvider reports a manager built without a session provider.
	ErrNoProvider = errors.New("recovery: session provider is required")
	// ErrNoStore reports a manager built without a persistence layer.
	ErrNoStore = errors.New("recovery: store is required")
	// ErrDisabled reports that automatic recovery is switched off, or that the
	// session itself is disabled.
	ErrDisabled = errors.New("recovery: recovery is disabled")
	// ErrUnrecoverable reports a session ccresu has no verified way to resume.
	ErrUnrecoverable = errors.New("recovery: session is unrecoverable")
	// ErrNotEligible reports a session whose state forbids another recovery,
	// e.g. it already resumed or the operator terminated it.
	ErrNotEligible = errors.New("recovery: session is not eligible for recovery")
	// ErrAlreadyRunning reports that another recovery of the same session is in
	// flight in this process.
	ErrAlreadyRunning = errors.New("recovery: a recovery attempt is already running")
	// ErrConcurrencyLimit reports that the configured number of simultaneous
	// recoveries is already in use.
	ErrConcurrencyLimit = errors.New("recovery: concurrency limit reached")
	// ErrMaxAttempts reports that the attempt budget for a session is spent.
	ErrMaxAttempts = errors.New("recovery: maximum attempts reached")
	// ErrVerificationFailed reports that the resume ran but could not be proved
	// to have continued the original conversation.
	ErrVerificationFailed = errors.New("recovery: resume could not be verified")
	// ErrInvalidConfig reports an unusable manager configuration.
	ErrInvalidConfig = errors.New("recovery: invalid configuration")
)

// RetryError carries the retry schedule for a failed but retryable attempt.
//
// It is returned alongside the Outcome so a caller can sleep until NextAttempt
// instead of recomputing the backoff, and so the persisted RETRY_WAIT state and
// the value the daemon actually waits for can never disagree.
type RetryError struct {
	SessionID string
	Attempt   int
	Max       int
	// Delay is the wait before the next attempt.
	Delay time.Duration
	Cause error
}

// Error implements error.
func (e *RetryError) Error() string {
	return fmt.Sprintf("recovery: session %s: attempt %d/%d failed (%s); next attempt in %s: %v",
		e.SessionID, e.Attempt, e.Max, Classify(e.Cause), e.Delay, e.Cause)
}

// Unwrap exposes the underlying failure to errors.Is/As.
func (e *RetryError) Unwrap() error { return e.Cause }

// cycleEndError records a failure that ends the current recovery cycle for a
// session. It does not end the session: the state it names is where the next
// cycle begins, and DISCOVERED is the usual one.
type cycleEndError struct {
	SessionID string
	State     State
	Cause     error
}

// Error implements error.
func (e *cycleEndError) Error() string {
	return fmt.Sprintf("recovery: session %s ended its cycle in %s: %v", e.SessionID, e.State, e.Cause)
}

// Unwrap exposes the underlying failure to errors.Is/As.
func (e *cycleEndError) Unwrap() error { return e.Cause }

// sessionError wraps an error with the session it concerns.
func sessionError(err error, sessionID string) error {
	return fmt.Errorf("%w: session %s", err, sessionID)
}

// stateError reports that a session cannot move to want from its current state.
func stateError(sessionID string, from, want storage.State) error {
	return fmt.Errorf("%w: session %s is %s and cannot become %s",
		ErrNotEligible, sessionID, from, want)
}
