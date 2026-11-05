package claude

// This file adds the fifth verified interface, `claude stop <id>`.
//
// It exists for exactly one situation. `claude -p --resume <id>` refuses to run
// while the same session is still attached to a background agent:
//
//	Error: That session is running in the background (837eaaea). Run
//	`claude attach 837eaaea` to open it, or `claude stop 837eaaea` first to
//	resume it here. Add --fork-session to branch off a copy instead.
//
// Of the three remedies the CLI offers, only `stop` keeps ccresu's promise: the
// conversation is kept, the session id is unchanged, and the resume that follows
// is the same headless `-p --resume` invocation as always. `attach` needs a
// terminal ccresu deliberately does not have, and `--fork-session` would branch
// a new conversation, which is the one thing a recovery must never do.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// StopArgs returns the verified argument vector for `claude stop <session-id>`.
//
// Exported for the same reason as ResumeArgs: tests and log lines can assert the
// exact command without reimplementing it.
func StopArgs(sessionID string) []string {
	return []string{"stop", sessionID}
}

// Stop asks the CLI to stop a background session.
//
// The session id is validated first, so it can never be read as a subcommand or
// a flag, and the call is bounded by the query timeout: stopping a background
// agent is bookkeeping, not a model turn.
//
// There is deliberately no version probe here. `Stop` is only ever called after
// a resume has already proved the executable runs and is a supported version, and
// a CLI too old to know `stop` answers with a usage error that is reported as a
// failure like any other.
func (c *CLI) Stop(ctx context.Context, sessionID string) error {
	if err := ValidateSessionID(sessionID); err != nil {
		return err
	}
	args := StopArgs(sessionID)
	out, runErr := c.run(ctx, c.queryTimeout, nil, args...)
	if runErr != nil && (errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded)) {
		return fmt.Errorf("%w: %w", ErrStopFailed, runErr)
	}
	if runErr != nil || out.ExitCode != 0 {
		return fmt.Errorf("%w: %s", ErrStopFailed, describeDetail("claude stop", out, runErr))
	}
	return nil
}

// backgroundMarker is the fragment of the CLI's refusal that identifies the
// condition. It is matched on a normalised message so indentation, wrapping,
// ANSI colour codes and typography cannot hide it.
const backgroundMarker = "session is running in the background"

// ansiEscape matches the colour codes a CLI may wrap its own message in.
var ansiEscape = regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]")

// whitespaceRun collapses every run of whitespace, including the newlines a
// wrapped message introduces.
var whitespaceRun = regexp.MustCompile(`\s+`)

// IsSessionRunningInBackground reports whether err is the CLI refusing to resume
// a session because it is still attached to a background agent.
//
// The check is text-based because the CLI does not expose a typed error for it,
// and it is deliberately narrow: a nil error never matches, and the phrase has
// to appear after normalisation, so an unrelated failure that merely mentions a
// background job is not mistaken for this one.
func IsSessionRunningInBackground(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrSessionRunningInBackground) {
		return true
	}
	return strings.Contains(normalizeMessage(err.Error()), backgroundMarker)
}

// normalizeMessage lowercases a message, strips ANSI colour codes and collapses
// whitespace, so a wrapped or restyled message still matches.
//
// The detector folds limit banners with a much larger rule set (unicode dashes,
// curly quotes, wrapped qualifiers). Only three rules are needed to recognise
// this refusal, so the smaller copy lives here rather than making this package
// depend on the detector for text it will never parse for a limit.
func normalizeMessage(s string) string {
	if s == "" {
		return ""
	}
	s = ansiEscape.ReplaceAllString(s, " ")
	s = whitespaceRun.ReplaceAllString(s, " ")
	return strings.ToLower(strings.TrimSpace(s))
}
