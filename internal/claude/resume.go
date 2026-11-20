package claude

import (
	"errors"
	"strings"
	"time"
)

// DefaultTurnTimeout bounds one resume invocation.
//
// The resume runs the whole turn. `claude --resume <id> "<message>"` returns
// when the continuation is finished rather than when it is accepted, so the
// bound has to cover the work: a handshake timeout would kill every resume that
// was doing what it was asked to do. A turn that outlives the bound is killed
// and reported as a deadline, which is a failed attempt rather than a crash.
const DefaultTurnTimeout = 30 * time.Minute

// Errors raised by the resume path.
var (
	// ErrWorkspaceNotTrusted reports the CLI's own refusal to run in a
	// directory it does not trust. It is its own outcome because the remedy is
	// the operator's decision, not a retry.
	ErrWorkspaceNotTrusted = errors.New("claude: workspace is not trusted")
	// ErrResumeDirectoryMissing reports a resume with no working directory. The
	// directory is chosen deliberately (never inherited from the daemon), so an
	// empty one is a caller bug rather than a reason to guess.
	ErrResumeDirectoryMissing = errors.New("claude: the resume needs the session's directory")
)

// ResumeArgs returns the resume argument vector:
//
//	--resume <session-id> <message>
//
// That is the whole command, deliberately. There is no background flag (the
// resume runs to the end of the turn and its exit status is the verdict), no
// session label (a label only exists to name a supervisor's job) and no
// guardrail flags (nothing rides a resume but the continuation itself).
//
// No bare `--` separator is emitted either: the message is a positional
// argument, and a separator would change how a leading dash in it is read.
func ResumeArgs(sessionID, message string) []string {
	return []string{"--resume", sessionID, message}
}

// WorkspaceNotTrusted reports whether out carries the CLI's untrusted-workspace
// refusal. The refusal is matched on its own words rather than on an exit code,
// because the code is not documented and a message is.
func WorkspaceNotTrusted(out Output) bool {
	text := strings.ToLower(string(out.Stdout) + "\n" + string(out.Stderr))
	return strings.Contains(text, "workspace not trusted")
}
