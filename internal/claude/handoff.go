package claude

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// DefaultHandoffTimeout bounds the handoff invocation.
//
// It is deliberately short. A handoff returns as soon as the supervisor accepts
// the session, which the docs describe as happening in seconds; the *work* is
// what takes minutes, and it is no longer ccresu's process. Keeping one timeout
// for both phases is the defect PRD 1 exists to remove, so the verification
// bound lives in the recovery configuration instead.
const DefaultHandoffTimeout = 2 * time.Minute

// Errors raised by the handoff path.
var (
	// ErrHandoffNotAcknowledged reports a handoff that ran but printed no
	// `backgrounded` block. Without that block ccresu cannot name the session it
	// created, so the handoff is not recorded as one.
	ErrHandoffNotAcknowledged = errors.New("claude: the handoff printed no backgrounded block")
	// ErrWorkspaceNotTrusted reports the CLI's own refusal to background a
	// session in an untrusted directory. It is its own outcome because the
	// remedy is the operator's decision, not a retry.
	ErrWorkspaceNotTrusted = errors.New("claude: workspace is not trusted")
	// ErrHandoffDirectoryMissing reports a handoff with no working directory.
	// The directory is chosen deliberately (never inherited from the daemon), so
	// an empty one is a caller bug rather than a reason to guess.
	ErrHandoffDirectoryMissing = errors.New("claude: the handoff needs the session's directory")
)

// Handoff is what the CLI printed when it accepted a background session.
//
// The block is the only place the state ID exists: `claude attach`, `claude
// logs` and `claude stop` all take it, and it is not the session UUID, so it is
// kept in a field of its own.
type Handoff struct {
	// Raw is the output the block was parsed out of, for the attempt record.
	Raw string
	// Acknowledged reports that a `backgrounded` block was found.
	Acknowledged bool
	// StartedService reports the `Starting background service…` line, which the
	// CLI prints only when it had to start a supervisor first.
	StartedService bool
	// StateID is the short ID from the block.
	StateID string
	// Name is the display name from the block, normally the label ccresu asked
	// for with `--name`.
	Name string
	// Note is the CLI's `note:` line, present when the continuation was copied
	// rather than continued in place.
	Note string
	// Copied reports that the note says a copy was started.
	Copied bool
	// CopySessionID is the new conversation's session ID when the note names
	// one, and empty when it does not.
	CopySessionID string
}

// HandoffLabel is the deterministic, ccresu-owned display name for a handoff.
//
// It exists so a recovered row is identifiable in agent view without consulting
// ccresu's database, and it is derived from the session ID so two handoffs of
// the same session cannot produce two different labels.
func HandoffLabel(sessionID string) string {
	short := sessionID
	if len(short) > 8 {
		short = short[:8]
	}
	short = strings.ToLower(short)
	if short == "" {
		return "ccresu"
	}
	return "ccresu-" + short
}

// StateIDFor derives the short state ID shape the CLI prints for a session.
//
// It exists so a fake or a test can build a `backgrounded` block with the same
// shape production parses, rather than a second, drifting one. It is not used to
// predict a real ID: only the CLI can say what it printed.
func StateIDFor(sessionID string) string {
	short := strings.ReplaceAll(sessionID, "-", "")
	if len(short) > 8 {
		short = short[:8]
	}
	if short == "" {
		return "00000000"
	}
	return strings.ToLower(short)
}

// HandoffArgs returns the handoff argument vector:
//
//	--bg --resume <session-id> --name <label> <message>
//
// The print-mode flags are absent by construction: `--bg` cannot be combined
// with `-p`/`--print`, so a vector that carries both would be rejected before a
// session existed. No bare `--` separator is emitted either, because the
// message is a positional argument and a separator would change how a leading
// dash in it is read.
func HandoffArgs(sessionID, message string) []string {
	return []string{
		"--bg",
		"--resume", sessionID,
		"--name", HandoffLabel(sessionID),
		message,
	}
}

var (
	// backgroundedPattern matches the block the CLI prints for a new background
	// session: `backgrounded · <short-id> · <name>`. Both `·` and `|` are
	// accepted because the block is presentation, not an interface.
	backgroundedPattern = regexp.MustCompile(`(?i)^\s*backgrounded\s*[·|]\s*([0-9a-z_-]+)\s*[·|]\s*(.+?)\s*$`)
	// backgroundedBarePattern matches a block without the detail columns.
	backgroundedBarePattern = regexp.MustCompile(`(?i)^\s*backgrounded\s*$`)
	// notePattern matches the CLI's explanation line.
	notePattern = regexp.MustCompile(`(?i)^\s*note:\s*(.*)$`)
	// uuidPattern finds a session UUID inside the note.
	uuidPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	// copyWords are the words the note uses when a copy was started.
	copyWords = []string{"copy", "copied", "new session", "forked", "different session"}
)

// ParseHandoff reads the handoff's printed block.
//
// It tolerates a preceding `Starting background service…` line, because the CLI
// prints it above the block when it had to start a supervisor, and it tolerates
// trailing management commands. A copy's `note:` line is captured rather than
// rejected: a copy is an expected outcome, not a failure.
func ParseHandoff(stdout []byte) (Handoff, error) {
	handoff := Handoff{Raw: strings.TrimSpace(string(stdout))}
	for _, line := range strings.Split(string(stdout), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(strings.ToLower(trimmed), "starting background service"):
			handoff.StartedService = true
		case notePattern.MatchString(trimmed):
			handoff.Note = strings.TrimSpace(notePattern.FindStringSubmatch(trimmed)[1])
			lower := strings.ToLower(handoff.Note)
			for _, word := range copyWords {
				if strings.Contains(lower, word) {
					handoff.Copied = true
					break
				}
			}
			if match := uuidPattern.FindString(handoff.Note); match != "" {
				handoff.CopySessionID = strings.ToLower(match)
			}
		default:
			if match := backgroundedPattern.FindStringSubmatch(trimmed); match != nil {
				handoff.Acknowledged = true
				handoff.StateID = strings.ToLower(strings.TrimSpace(match[1]))
				handoff.Name = strings.TrimSpace(match[2])
				continue
			}
			if backgroundedBarePattern.MatchString(trimmed) {
				handoff.Acknowledged = true
			}
		}
	}
	if !handoff.Acknowledged {
		return handoff, fmt.Errorf("%w: %s", ErrHandoffNotAcknowledged, tail(handoff.Raw, 200))
	}
	return handoff, nil
}

// WorkspaceNotTrusted reports whether out carries the CLI's untrusted-workspace
// refusal. The refusal is matched on its own words rather than on an exit code,
// because the code is not documented and a message is.
func WorkspaceNotTrusted(out Output) bool {
	text := strings.ToLower(string(out.Stdout) + "\n" + string(out.Stderr))
	return strings.Contains(text, "workspace not trusted")
}
