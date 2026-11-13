package recovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
)

// Wait is the wait a refused resume was parked on.
//
// It is what the parker learned from the refusal: the instant ccresu will next
// act, whether that instant is the reset the banner named or the first poll of
// an unknown one, how long remains until then, and the usage-limit row that
// records what the CLI said. It is a value rather than a schedule because the
// daemon owns the persisted schedule; the recovery engine only reports the wait
// it is about to take.
type Wait struct {
	// At is the instant ccresu will next act, in UTC: the reset the refusal
	// named, or the first poll when it named none. Zero when no wait could be
	// planned.
	At time.Time
	// Known reports whether At is a reset instant the banner named, as opposed
	// to the first poll of an unknown reset.
	Known bool
	// Delay is how long remains until At, measured when the wait was planned.
	Delay time.Duration
	// LimitID is the usage_limits row recording the refusal, or zero when no
	// row could be written.
	LimitID int64
}

// LimitPark records a usage-limit refusal and plans the wait until the reset.
//
// It is injected for the same reason LimitRecheck is: recognising a banner is
// the detector's job and resolving a reset phrase is the scheduler's, so the
// recovery engine asks for the wait instead of growing a second copy of either.
// banner is the CLI's own output from the refused command, verbatim, so the
// parker sees exactly what an operator would see.
//
// The returned error is reported and logged but never turns the refusal into a
// retry: a parker that cannot plan still leaves the session waiting, because
// resuming it again would ask the same question a second time.
type LimitPark func(ctx context.Context, sessionID, banner string) (Wait, error)

// LimitInForceError reports a resume the CLI refused because the usage limit has
// not reset yet.
//
// It is the refusal made explicit: the CLI ran, the conversation is intact, and
// the banner says when the work may continue. It unwraps to the adapter's own
// error, so errors.Is(err, claude.ErrUsageLimitInForce) keeps working, and it
// also reports ErrLimitNotCleared, which is the same fact stated as a wait.
type LimitInForceError struct {
	// SessionID is the session whose resume was refused.
	SessionID string
	// Banner is the CLI's own output from the refused command.
	Banner string
	// ResetAt is the instant the wait ends, in UTC. Zero when nothing is
	// scheduled.
	ResetAt time.Time
	// Known reports whether ResetAt is the reset the refusal named.
	Known bool
	// Wait is how long remains until ResetAt.
	Wait time.Duration
	// Cause is the failure the CLI adapter returned.
	Cause error
}

// Error renders the refusal as the wait it is.
func (e *LimitInForceError) Error() string {
	when := "until the limit clears"
	switch {
	case e.Known && !e.ResetAt.IsZero():
		when = "until " + e.ResetAt.UTC().Format(time.RFC3339)
	case e.Wait > 0:
		when = "for " + e.Wait.String()
	}
	return fmt.Sprintf("%s: usage limit in force, waiting %s: %s", e.SessionID, when, e.Banner)
}

// Is reports the refusal as ErrLimitNotCleared as well, so a caller that only
// knows about the pre-resume limit check treats both the same way.
func (e *LimitInForceError) Is(target error) bool { return target == ErrLimitNotCleared }

// Unwrap exposes the adapter's error, which carries claude.ErrUsageLimitInForce.
func (e *LimitInForceError) Unwrap() error { return e.Cause }

// refusalBanner returns what the CLI itself said when it refused the resume.
//
// The adapter already captured both streams; this picks the one that carries the
// banner instead of re-rendering the whole wrapped error, because the banner is
// what an operator needs to read and what the detector needs to parse. A failure
// with no captured output falls back to the error text.
func refusalBanner(cause error) string {
	var cliErr *claude.CLIError
	if errors.As(cause, &cliErr) {
		for _, stream := range [][]byte{cliErr.Stdout, cliErr.Stderr} {
			if text := strings.TrimSpace(string(stream)); text != "" {
				return text
			}
		}
	}
	return cause.Error()
}
