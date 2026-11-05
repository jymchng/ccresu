package claude

// This file adds the fifth verified interface, `claude stop <job-id>`.
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
//
// The argument is NOT the session UUID. `claude stop` resolves a background job
// name -- exactly eight lower-case hex characters -- by prefix against the job
// directories it keeps (`/^[a-f0-9]{8}$/`, matched with startsWith, Claude Code
// 2.1.223). A full UUID never matches one of those names, so
// `claude stop 837eaaea-ee9a-4ef0-ae47-add73a107369` always answers
//
//	No job matching '837eaaea-ee9a-4ef0-ae47-add73a107369'. Run 'claude agents'
//	to list running sessions.
//
// and exits 1; `claude stop 837eaaea` is the command that works, which is
// precisely the form the CLI prints in its own refusal. ccresu used to pass the
// session UUID here, so the stop always failed, the original refusal was
// reported unchanged, and a recoverable session landed in PERMANENTLY_FAILED.
// The job name now comes from the refusal message itself (BackgroundJobID), and
// Stop refuses anything that is not a job name before a process is started.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// backgroundJobIDPattern is the shape `claude stop` resolves: eight lower-case
// hex characters, the name of a background job directory.
var backgroundJobIDPattern = regexp.MustCompile(`^[a-f0-9]{8}$`)

// BackgroundJobIDFromStopClause finds the job name in the refusal's own remedy:
// "or `claude stop 837eaaea` first to resume it here".
//
// The remediation clause is preferred over the parenthesised name because it is
// the command the CLI tells a human to run, so it is the one guaranteed to work.
var backgroundJobIDStopClause = regexp.MustCompile(`(?i)\bstop\s+([0-9a-f]{8})\b`)

// BackgroundJobIDFromParentheses finds the job name the refusal announces as the
// owner of the session: "That session is running in the background (837eaaea)".
var backgroundJobIDParentheses = regexp.MustCompile(`(?i)running in the background\s*\(([0-9a-f]{8})\)`)

// ValidBackgroundJobID reports whether id is the form `claude stop` accepts.
func ValidBackgroundJobID(id string) bool { return backgroundJobIDPattern.MatchString(id) }

// BackgroundJobID extracts the background job name from a resume refusal.
//
// It returns "" when the message names no job, and never guesses: stopping the
// wrong eight-hex job would attach ccresu's recovery to a different session, so
// an unnamed job is reported as "cannot stop" rather than derived from the
// session id. Case, backticks, ANSI colour and line wrapping do not matter,
// because the patterns run over the raw message with an explicit case fold.
func BackgroundJobID(err error) string {
	if err == nil {
		return ""
	}
	text := ansiEscape.ReplaceAllString(err.Error(), " ")
	for _, pattern := range []*regexp.Regexp{backgroundJobIDStopClause, backgroundJobIDParentheses} {
		if match := pattern.FindStringSubmatch(text); match != nil {
			id := strings.ToLower(match[1])
			if ValidBackgroundJobID(id) {
				return id
			}
		}
	}
	return ""
}

// StopArgs returns the verified argument vector for `claude stop <job-id>`.
//
// Exported for the same reason as ResumeArgs: tests and log lines can assert the
// exact command without reimplementing it.
func StopArgs(jobID string) []string {
	return []string{"stop", jobID}
}

// Stop asks the CLI to stop one background job.
//
// jobID is the eight-hex job name the CLI printed in its refusal, not the
// session UUID; the UUID is rejected before anything is executed, because the
// CLI answers it with "No job matching" and a failed recovery.
//
// The call is bounded by the query timeout: stopping a background agent is
// bookkeeping, not a model turn.
//
// There is deliberately no version probe here. `Stop` is only ever called after
// a resume has already proved the executable runs and is a supported version, and
// a CLI too old to know `stop` answers with a usage error that is reported as a
// failure like any other.
func (c *CLI) Stop(ctx context.Context, jobID string) error {
	if !ValidBackgroundJobID(jobID) {
		return fmt.Errorf("%w: %q is not an eight-hex background job name", ErrInvalidBackgroundJobID, jobID)
	}
	args := StopArgs(jobID)
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
