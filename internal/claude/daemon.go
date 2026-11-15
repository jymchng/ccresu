package claude

// This file adds the supervisor's documented read-only commands and the version
// comparison that goes with them.
//
// PRD 3's premise is that the supervisor owns the lifecycle: it restarts a
// session that exits unexpectedly, migrates sessions across its own updates, and
// idle-stops a session after about an hour. ccresu used to imitate that with a
// child process and a PID check, and the imitation was strictly worse, so the
// only lifecycle command left is `claude stop <job-id>` for a termination an
// operator asked for (R3, R4, R5).
//
// Two commands are therefore deliberately absent, and their absence is the
// design rather than an omission:
//
//	claude daemon stop   -- stops every session the supervisor hosts (R3)
//	claude respawn       -- re-runs the original prompt when a transcript is
//	                        gone, which a recovery must never do (R4)
//
// ccresu reports those commands to the operator when they are the remedy. It
// does not run them. Both are covered by TestCcresuNeverRespawns, which asserts
// that no argv constructor in this package can produce either one.
//
// What ccresu does read: `claude daemon status` once per pass (R1), and
// `claude daemon logs` / `claude logs <id>` on demand for `ccresu inspect`
// (R10). Nothing here is polled.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Supervisor failures, each a distinct named condition.
var (
	// ErrDaemonStatusFailed reports a `daemon status` that failed for a reason
	// other than "the supervisor is not running". Exit 1 is not a failure: it is
	// the documented answer when no supervisor exists yet, which is the normal
	// state of a machine that has never run `claude --bg` (R1).
	ErrDaemonStatusFailed = errors.New("claude: `claude daemon status` failed")

	// ErrSupervisorVersionMismatch reports that the running supervisor and the
	// CLI ccresu invokes are different versions (R2). It is its own condition
	// because the remedy stops the sessions the supervisor hosts, so the
	// operator has to decide, and no retry may perform it.
	ErrSupervisorVersionMismatch = errors.New("claude: the running supervisor is a different version")

	// ErrLogsFailed reports that a documented log read failed.
	ErrLogsFailed = errors.New("claude: reading logs failed")
)

// RemedySupervisorVersionMismatch is the documented remedy for a stale
// supervisor.
//
// `--keep-workers` is the part that matters: without it the command stops the
// sessions the supervisor hosts, turning a warning into an outage. The next
// `claude --bg` handoff (or `claude agents`) starts a supervisor on the new
// version, which is why nothing has to be restarted by hand afterwards.
const RemedySupervisorVersionMismatch = "claude daemon stop --any --keep-workers"

// DaemonStatusArgs returns the verified vector for `claude daemon status`.
func DaemonStatusArgs() []string { return []string{"daemon", "status"} }

// DaemonLogsArgs returns the verified vector for `claude daemon logs`.
func DaemonLogsArgs() []string { return []string{"daemon", "logs"} }

// SessionLogsArgs returns the verified vector for `claude logs <id>`.
//
// id is the short state id the supervisor prints in its
// `backgrounded · <short-id> · <name>` block — the same identifier `claude
// attach` and `claude stop` take — not the session UUID.
func SessionLogsArgs(id string) []string { return []string{"logs", id} }

// SupervisorStatus is what `claude daemon status` said on one pass.
//
// Running is decided by the exit code, never by the text: exit 0 means a
// supervisor answered, exit 1 means there is none. The parsed fields are
// best-effort, because the documentation promises "state, version, socket
// directory, and worker count" without promising a format — so Raw is always
// kept and nothing important is inferred from a field that was not found.
type SupervisorStatus struct {
	// Running reports whether a supervisor answered at all.
	Running bool
	// State is the supervisor's own word for what it is doing, lower-cased
	// only for comparison; the raw value is preserved.
	State string
	// Version is the supervisor's version, unparsed (Raw set) when the text
	// carried something the version parser did not recognise.
	Version Version
	// SocketDir is the socket directory the supervisor reported.
	SocketDir string
	// Workers is the number of sessions the supervisor hosts. It is only
	// meaningful when WorkersKnown is true.
	Workers      int
	WorkersKnown bool
	// Raw is the exact stdout, so `ccresu inspect` can show the reading
	// instead of a lossy summary of it.
	Raw string
	// Stderr is the exact stderr, which is where a CLI that lacks the `daemon`
	// subcommand explains itself.
	Stderr string
	// ExitCode is the process exit status.
	ExitCode int
	// Argv is the command actually executed (R11), so a log line or an
	// inspection can be reproduced exactly.
	Argv []string
}

// Recognised reports whether any documented field was parsed out of the output.
//
// It exists so a caller can say "answered, but in a format ccresu does not
// know" rather than presenting an empty reading as an empty supervisor.
func (s SupervisorStatus) Recognised() bool {
	return s.State != "" || s.Version.Parsed || s.WorkersKnown || s.SocketDir != ""
}

// SupervisorVersionMismatch is the named condition R2 requires.
//
// It carries all three versions, because the operator's next question is
// always "which of the two is behind, and what is the floor?" — and the remedy,
// because the answer is not something ccresu may do on its own.
type SupervisorVersionMismatch struct {
	// Supervisor is the version the running supervisor reported.
	Supervisor Version
	// CLI is the version of the executable ccresu invokes.
	CLI Version
	// Minimum is the floor this build supports.
	Minimum Version
	// Remedy is the documented command, reported and never run.
	Remedy string
}

// Error renders both versions and the remedy.
func (e *SupervisorVersionMismatch) Error() string {
	return fmt.Sprintf("the running supervisor is %s but the claude ccresu invokes is %s (floor %s): run `%s`, which keeps the sessions it hosts and lets the next `claude --bg` start a supervisor on the new version",
		e.Supervisor, e.CLI, e.Minimum, e.Remedy)
}

// Unwrap makes the condition readable as ErrSupervisorVersionMismatch.
func (e *SupervisorVersionMismatch) Unwrap() error { return ErrSupervisorVersionMismatch }

// CompareSupervisorVersion decides whether the supervisor differs from the CLI
// ccresu invokes, or falls below the floor (R2).
//
// Three cases are deliberately not mismatches:
//
//   - No supervisor answered. "There is none yet" is the normal state before the
//     first handoff, and the handoff starts one.
//   - The supervisor's version was not parseable. An unreadable version is a
//     fact to record, not a verdict to act on: the documented remedy costs the
//     operator their hosted sessions, so it is raised only on evidence.
//   - The CLI's own version was not parseable; the caller has a different
//     problem (it cannot hand off at all) and says so with its own error.
func CompareSupervisorVersion(status SupervisorStatus, cli, minimum Version) error {
	if !status.Running || !status.Version.Parsed || !cli.Parsed {
		return nil
	}
	if cli.Compare(status.Version) != 0 || (minimum.Parsed && !status.Version.AtLeast(minimum)) {
		return &SupervisorVersionMismatch{
			Supervisor: status.Version,
			CLI:        cli,
			Minimum:    minimum,
			Remedy:     RemedySupervisorVersionMismatch,
		}
	}
	return nil
}

// DaemonStatus reads the supervisor's state, version, socket directory and
// worker count.
//
// Exit 1 is the documented "the supervisor isn't running" answer and returns a
// status with Running false and a nil error, so a first run on a machine that
// has never used `--bg` is not a failure (R1). Any other non-zero exit is
// ErrDaemonStatusFailed, which is a different fact and says so.
//
// The call is bounded by the query timeout: `daemon status` is bookkeeping, not
// a model turn.
func (c *CLI) DaemonStatus(ctx context.Context) (SupervisorStatus, error) {
	args := DaemonStatusArgs()
	out, runErr := c.run(ctx, c.queryTimeout, nil, args...)
	status := SupervisorStatus{
		Raw:      strings.TrimSpace(string(out.Stdout)),
		Stderr:   strings.TrimSpace(string(out.Stderr)),
		ExitCode: out.ExitCode,
		Argv:     args,
	}
	if runErr != nil && (errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded)) {
		// A timeout is the "unresponsive supervisor" failure mode, not an
		// absent one: the remedy differs, so it must not be read as exit 1.
		return status, fmt.Errorf("%w: %w", ErrDaemonStatusFailed, runErr)
	}
	switch out.ExitCode {
	case 0:
		status.Running = true
		parsed := ParseSupervisorStatus(status.Raw)
		parsed.Raw, parsed.Stderr = status.Raw, status.Stderr
		parsed.ExitCode, parsed.Argv, parsed.Running = 0, args, true
		return parsed, nil
	case 1:
		return status, nil
	default:
		return status, fmt.Errorf("%w: %s", ErrDaemonStatusFailed,
			describeDetail("claude daemon status", out, runErr))
	}
}

// LogsResult is one on-demand log read.
type LogsResult struct {
	// Argv is the command actually executed (R11).
	Argv []string
	// Text is the captured output, already bounded by the output limit.
	Text string
	// ExitCode is the process exit status.
	ExitCode int
}

// SessionLogs reads a session's recent output with `claude logs <id>`.
//
// It is on demand — `ccresu inspect` — and never polled (R10): the supervisor
// holds the output, and ccresu reading the file behind its back would be a
// second, disagreeing source of truth.
func (c *CLI) SessionLogs(ctx context.Context, id string) (LogsResult, error) {
	if err := validLogTarget(id); err != nil {
		return LogsResult{Argv: SessionLogsArgs(id)}, err
	}
	return c.readLogs(ctx, SessionLogsArgs(id), "claude logs")
}

// DaemonLogs reads the supervisor's own log with `claude daemon logs`.
func (c *CLI) DaemonLogs(ctx context.Context) (LogsResult, error) {
	return c.readLogs(ctx, DaemonLogsArgs(), "claude daemon logs")
}

// readLogs runs one log command and returns its output.
//
// A failed read is reported, never swallowed into an empty log: an empty log
// and an unreadable one are different facts, which is the same distinction the
// published-state read makes for a failed listing.
func (c *CLI) readLogs(ctx context.Context, args []string, what string) (LogsResult, error) {
	out, runErr := c.run(ctx, c.queryTimeout, nil, args...)
	result := LogsResult{Argv: args, ExitCode: out.ExitCode}
	if runErr != nil && (errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded)) {
		return result, fmt.Errorf("%w: %w", ErrLogsFailed, runErr)
	}
	if runErr != nil || out.ExitCode != 0 {
		return result, fmt.Errorf("%w: %s", ErrLogsFailed, describeDetail(what, out, runErr))
	}
	result.Text = strings.TrimRight(string(out.Stdout), "\n")
	return result, nil
}

// validLogTarget rejects an id that a process would read as something else.
//
// The accepted set is deliberately narrow — the command line takes an
// identifier, never a flag or a path — because the one thing ccresu must not do
// is pass a value the CLI interprets as an option.
func validLogTarget(id string) error {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return fmt.Errorf("%w: no log target given", ErrInvalidSessionID)
	}
	if strings.HasPrefix(trimmed, "-") || strings.ContainsAny(trimmed, " \t\r\n") {
		return fmt.Errorf("%w: %q is not an identifier", ErrInvalidSessionID, id)
	}
	return nil
}

// supervisorFieldPattern matches one `key: value` (or `key value`) line.
//
// The separator is optional because the output format is only promised in prose:
// "state: running", "State  running", "- workers: 3" and "socket dir=/x" all
// have to work, and none of them may be mistaken for a fact that was not stated.
var supervisorFieldPattern = regexp.MustCompile(`^([A-Za-z][A-Za-z _-]*?)\s*[:=]\s*(.*)$|^([A-Za-z][A-Za-z _-]*?)\s{2,}(.*)$`)

// supervisorDigits pulls the leading integer out of a worker count.
var supervisorDigits = regexp.MustCompile(`^[^\d-]*(-?\d+)`)

// ParseSupervisorStatus reads what it can out of `claude daemon status` output.
//
// The parser is tolerant on purpose (open question 1 in PRD 3 is whether the
// output is machine-readable at all): unknown lines are ignored, colour codes and
// bullets are removed, key spelling is folded, and anything not recognised is
// simply not claimed. A status whose version could not be parsed keeps the raw
// text so the operator can see what the supervisor actually said.
func ParseSupervisorStatus(raw string) SupervisorStatus {
	status := SupervisorStatus{Raw: strings.TrimSpace(raw)}
	for _, line := range strings.Split(ansiEscape.ReplaceAllString(raw, ""), "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimSpace(strings.TrimLeft(line, "-*•"))
		if line == "" {
			continue
		}
		key, value, ok := splitSupervisorField(line)
		if !ok {
			continue
		}
		switch canonicalSupervisorKey(key) {
		case "state":
			status.State = strings.TrimSpace(value)
		case "version":
			parsed, err := ParseVersion(value)
			if err != nil {
				status.Version = Version{Raw: strings.TrimSpace(value)}
				continue
			}
			status.Version = parsed
		case "socket":
			status.SocketDir = strings.TrimSpace(value)
		case "workers":
			if match := supervisorDigits.FindStringSubmatch(strings.TrimSpace(value)); match != nil {
				if n, convErr := strconv.Atoi(match[1]); convErr == nil && n >= 0 {
					status.Workers, status.WorkersKnown = n, true
				}
			}
		}
	}
	return status
}

// splitSupervisorField separates one line into its key and value.
func splitSupervisorField(line string) (string, string, bool) {
	if match := supervisorFieldPattern.FindStringSubmatch(line); match != nil {
		if match[1] != "" {
			return strings.TrimSpace(match[1]), strings.TrimSpace(match[2]), true
		}
		return strings.TrimSpace(match[3]), strings.TrimSpace(match[4]), true
	}
	// A bare `state: running` with an unusual separator still has a colon; a
	// line without one is prose and carries no field.
	if idx := strings.Index(line, ":"); idx > 0 {
		return strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+1:]), true
	}
	return "", "", false
}

// canonicalSupervisorKey folds the spellings a supervisor might use.
func canonicalSupervisorKey(key string) string {
	folded := strings.ToLower(strings.TrimSpace(key))
	folded = strings.Join(strings.Fields(folded), " ")
	switch folded {
	case "state", "status":
		return "state"
	case "version", "supervisor version", "claude version":
		return "version"
	case "socket", "socket dir", "socket directory", "socket_dir", "socket path":
		return "socket"
	case "worker", "workers", "worker count", "worker_count", "session count", "sessions":
		return "workers"
	}
	return ""
}
