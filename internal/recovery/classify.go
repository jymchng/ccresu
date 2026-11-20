package recovery

import (
	"context"
	"errors"
	"strings"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
)

// Class is how a failure is treated by the retry policy.
type Class string

const (
	// ClassRetryable means the attempt may be repeated: network trouble, a
	// timeout, a malformed reply, or an error nobody recognised. Unrecognised
	// failures are retryable because the attempt budget bounds them anyway,
	// whereas treating them as permanent would abandon a recoverable session.
	ClassRetryable Class = "retryable"
	// ClassPermanent means repeating the attempt on the current schedule cannot
	// help: an authentication failure, a missing binary, a session id the CLI
	// will not accept. It does NOT mean the session is abandoned — a permanent
	// failure still returns the session to DISCOVERED, where the next discovery
	// cycle re-reads its transcript and decides afresh.
	ClassPermanent Class = "permanent"
	// ClassLimitInForce means the CLI ran and refused the resume because the
	// account's usage window has not reset yet.
	//
	// It is neither a failure of the attempt nor a reason to wait for a backoff:
	// the refusal names when the work may continue, so the only useful answer is
	// to park the session until that instant. Calling it retryable is what made
	// ccresu resume the same paused session every reconcile pass; calling it
	// permanent is what made it look like a broken binary and sent the session
	// back to DISCOVERED. It is a wait, so it waits.
	ClassLimitInForce Class = "limit-in-force"
	// ClassUnrecoverable means ccresu has no verified way to continue the
	// session at all.
	ClassUnrecoverable Class = "unrecoverable"
	// ClassCanceled means the attempt was abandoned because ccresu is shutting
	// down or the caller cancelled. It is neither the session's fault nor a
	// reason to spend the remaining budget on a tighter schedule.
	ClassCanceled Class = "canceled"
	// ClassSpendCapReached means ccresu refused to spend, or stopped after an
	// attempt overspent, because a configured spend cap said so (PRD 5 R5, R6).
	//
	// It is deliberately not ClassPermanent: a permanent failure returns the
	// session to DISCOVERED, where the next cycle starts a fresh attempt budget
	// and resumes again — which is the spending the cap exists to prevent. It is
	// also not retryable: repeating the attempt cannot change a number that is
	// already recorded.
	ClassSpendCapReached Class = "spend-cap-reached"
)

// Valid reports whether c is a known class.
func (c Class) Valid() bool {
	switch c {
	case ClassRetryable, ClassPermanent, ClassLimitInForce, ClassUnrecoverable, ClassCanceled,
		ClassSpendCapReached:
		return true
	default:
		return false
	}
}

// Retryable reports whether the policy may repeat the attempt.
//
// A limit refusal is deliberately not retryable: repeating it now would be the
// hot loop that produced sixty refused resumes in half an hour. What ends the
// wait is the reset instant the refusal names, not the attempt budget.
func (c Class) Retryable() bool { return c == ClassRetryable }

// State returns the state the session moves to when an attempt fails this way.
//
// A failure that is not positively unrecoverable returns the session to
// DISCOVERED, whatever the class: the daemon rediscovers it, re-reads the
// transcript and finds the next reset time. There is deliberately no terminal
// failure state, because a resume that failed is not evidence that the
// conversation is gone.
//
// Two classes are exceptions, and both are parks rather than verdicts:
//
//   - A limit refusal parks the session in WAITING_FOR_RESET: the conversation
//     is fine and the reset is known, so rediscovery would only re-ask a question
//     that has already been answered.
//   - A retryable failure parks it there too. "Parked until an instant" is what
//     that state means, and the instant here is the backoff: the session stays
//     where it is, keeps accumulating attempt budget, and is tried again. Sending
//     it to DISCOVERED instead would reset the budget on every failure, because
//     the budget counts the attempts spent since the session last entered
//     DISCOVERED — so a flaky network could spend an unlimited number of
//     attempts.
//
// A spend stop is neither: it is a decision that nothing more may be spent until
// an operator acts, which is DISABLED.
func (c Class) State() State {
	switch c {
	case ClassUnrecoverable:
		return StateUnrecoverable
	case ClassLimitInForce, ClassRetryable, ClassCanceled:
		// A wait, not a verdict: the session is parked and tried again.
		return StateWaitingForReset
	case ClassSpendCapReached:
		// Nothing may be spent until an operator decides, which is what DISABLED
		// means. `ccresu enable` is the way back.
		return StateDisabled
	default:
		// Permanent: repeating on this schedule cannot help. The session is
		// rediscovered rather than abandoned, and the next cycle starts a fresh
		// attempt budget.
		return StateDiscovered
	}
}

// ErrSpendCapReached reports that a configured spend cap stopped recovery for a
// session (PRD 5 R7).
//
// The message names the knob, the configured cap and the observed spend, because
// that text is what reaches the operator: `permanent_failure` renders
// EventData.Reason from the failure it was raised with (R9).
var ErrSpendCapReached = errors.New("recovery: spend cap reached")

// nonRetryable collects the sentinel errors that must never be retried.
//
// The list is deliberately narrow and explicit. Every entry is a condition that
// will still be true a minute later: a missing binary, an unsupported version,
// a session that cannot be resumed, a session id the CLI will not accept, or a
// handoff the supervisor refused or never acknowledged. The handoff entries are
// here because the same arguments produce the same silence: `claude.Resume`
// documents that neither an untrusted workspace nor a missing `backgrounded`
// block gets better by trying again, and the classifier is what makes that true
// rather than merely intended.
var nonRetryable = []error{
	claude.ErrCLIUnavailable,
	claude.ErrUnsupportedVersion,
	claude.ErrUnrecognizedVersion,
	claude.ErrInvalidSessionID,
	claude.ErrEmptyMessage,
	claude.ErrMessageTooLong,
	claude.ErrSessionNotPreserved,
	claude.ErrWorkspaceNotTrusted,
	claude.ErrHandoffNotAcknowledged,
	claude.ErrHandoffDirectoryMissing,
}

// permanentMarkers are text fragments that identify an authentication or
// permission failure reported inside an otherwise generic error. The CLI does
// not expose typed errors for these, and retrying them burns the attempt budget
// while the user is asleep.
var permanentMarkers = []string{
	"invalid api key",
	"invalid x-api-key",
	"authentication_error",
	"authentication failed",
	"unauthorized",
	"forbidden",
	"permission denied",
	"credit balance is too low",
	"invalid_request_error",
	"billing",
	"usage limit reached for this account", // account-level block, not the 5h window
	"api key",
}

// retryableMarkers are transient transport failures worth another attempt.
var retryableMarkers = []string{
	"connection reset",
	"connection refused",
	"broken pipe",
	"eof",
	"network is unreachable",
	"no such host",
	"server closed",
	"temporarily unavailable",
	"timeout",
	"timed out",
	"try again",
	"overloaded",
	"429",
	"500",
	"502",
	"503",
	"504",
}

// Classify decides how a failed attempt is treated.
//
// The checks run in a fixed order so the outcome never depends on how the error
// happened to be wrapped:
//
//  1. A cancelled context is ClassCanceled, whatever else the error says.
//  2. Typed sentinels win over text: an unrecoverable session is unrecoverable
//     even if the message also mentions a network.
//  3. A usage-limit refusal is a wait, and it is decided before any text marker
//     is consulted. The banner is prose, and prose is exactly what the marker
//     lists guess from: "usage limit reached for this account" is a permanent
//     marker, so a refusal that carried those words would otherwise be
//     abandoned as an account block. The typed sentinel is the fact; the
//     markers are the guess.
//  4. Authentication/permission text is permanent.
//  5. Transport text, and any error that looks like a network-level failure, is
//     retryable.
//  6. Anything left is retryable, bounded by the attempt budget.
func Classify(err error) Class {
	if err == nil {
		return ClassRetryable
	}
	if errors.Is(err, context.Canceled) {
		return ClassCanceled
	}
	if errors.Is(err, claude.ErrUnrecoverable) || errors.Is(err, ErrUnrecoverable) {
		return ClassUnrecoverable
	}
	if errors.Is(err, claude.ErrUsageLimitInForce) {
		return ClassLimitInForce
	}
	// A spend stop is decided before the text markers for the same reason a
	// limit refusal is: its own message names a knob such as
	// `max_cost_usd_per_session`, and the markers below guess from prose.
	if errors.Is(err, ErrSpendCapReached) {
		return ClassSpendCapReached
	}
	for _, sentinel := range nonRetryable {
		if errors.Is(err, sentinel) {
			return ClassPermanent
		}
	}
	// A session that is gone from disk and from the CLI has nothing left to
	// resume.
	if errors.Is(err, claude.ErrSessionNotFound) || errors.Is(err, storage.ErrNotFound) {
		return ClassPermanent
	}

	message := strings.ToLower(err.Error())
	for _, marker := range permanentMarkers {
		if strings.Contains(message, marker) {
			return ClassPermanent
		}
	}
	for _, marker := range retryableMarkers {
		if strings.Contains(message, marker) {
			return ClassRetryable
		}
	}

	// Everything left is retryable, including a net.Error, a
	// context.DeadlineExceeded or an *fs.PathError whose text carries none of the
	// markers above: the attempt budget bounds the cost of one more try, and
	// abandoning a session that might still resume is the worse mistake. There
	// used to be explicit branches for those three shapes here; each of them
	// returned ClassRetryable, exactly like this fallthrough, so they were
	// deleted instead of being left as unreachable code.
	return ClassRetryable
}

// Retryable reports whether err may be retried. A nil error is not a failure,
// so it reports false.
func Retryable(err error) bool {
	return err != nil && Classify(err).Retryable()
}

// Explain renders the classification for a log line or a CLI message.
func Explain(err error) string {
	if err == nil {
		return "ok"
	}
	return string(Classify(err)) + ": " + err.Error()
}
