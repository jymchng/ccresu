package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Default command timeouts.
const (
	// DefaultQueryTimeout bounds `--version` and `agents --json`.
	DefaultQueryTimeout = 30 * time.Second
)

// MaxMessageBytes caps the continuation message ccresu will send. Anything
// larger is a caller bug, not something to hand to a command line.
const MaxMessageBytes = 8 << 10

// CLIOptions configures a CLI adapter.
type CLIOptions struct {
	// Executable is the claude binary. Empty means "claude" from PATH.
	Executable string
	// Execer runs commands. Empty means OSExecer.
	Execer Execer
	// QueryTimeout bounds `--version` and `agents --json`.
	QueryTimeout time.Duration
	// HandoffTimeout bounds the handoff invocation. It does not bound the
	// supervisor's work, which is no longer ccresu's process.
	HandoffTimeout time.Duration
	// MaxOutputBytes caps captured output.
	MaxOutputBytes int
	// MinVersion overrides the supported-version floor.
	MinVersion *Version
	// ConfigDir is the resolved Claude Code config directory. When set, every
	// invocation carries CLAUDE_CONFIG_DIR=<dir>, so the read and the handoff can
	// never land on two different supervisors (PRD 3 R9): $CLAUDE_CONFIG_DIR makes
	// separate instances, and inheriting it from the environment means a daemon
	// started one way could read one supervisor and hand off to another.
	ConfigDir string
	// Logger receives the recovery diagnostics for a handoff that needed a
	// retry or a directory decision. Empty means slog.Default().
	Logger *slog.Logger
	// Guardrails are the flags every handoff carries (PRD 5 R2). The zero value
	// adds none.
	Guardrails Guardrails
}

// CLI is the version-aware adapter around the claude executable.
//
// It exposes exactly these verified invocations:
//
//	claude --version
//	claude agents --json [--all] [--cwd <path>]
//	claude --bg --resume <session-id> --name <label> <message>
//	claude stop <job-id>
//	(the transcript and session-env files are read, not executed)
//
// Recovery hands the continuation to the supervisor rather than running it:
// `--bg` cannot be combined with `-p`/`--print`, so the print-mode flags are
// absent by construction and not merely unused. The handoff is given the
// session's own directory, never the daemon's, and the message is always passed
// as a single argv element so no shell ever sees it.
type CLI struct {
	bin            string
	exec           Execer
	queryTimeout   time.Duration
	handoffTimeout time.Duration
	maxOutputBytes int
	minVersion     Version
	configDir      string
	log            *slog.Logger
	// guardrails are the flags every handoff carries (PRD 5 R2).
	guardrails Guardrails

	mu          sync.Mutex
	version     Version
	versionErr  error
	versionRead bool
}

// NewCLI builds a CLI adapter, applying defaults.
func NewCLI(opts CLIOptions) *CLI {
	c := &CLI{
		bin:            strings.TrimSpace(opts.Executable),
		exec:           opts.Execer,
		queryTimeout:   opts.QueryTimeout,
		handoffTimeout: opts.HandoffTimeout,
		maxOutputBytes: opts.MaxOutputBytes,
		configDir:      strings.TrimSpace(opts.ConfigDir),
		log:            opts.Logger,
		guardrails:     opts.Guardrails,
	}
	if c.bin == "" {
		c.bin = DefaultExecutable
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	if c.exec == nil {
		c.exec = OSExecer{}
	}
	if c.queryTimeout <= 0 {
		c.queryTimeout = DefaultQueryTimeout
	}
	if c.handoffTimeout <= 0 {
		c.handoffTimeout = DefaultHandoffTimeout
	}
	c.minVersion = MinimumVersion
	if opts.MinVersion != nil {
		c.minVersion = *opts.MinVersion
	}
	return c
}

// DefaultExecutable is the CLI name looked up in PATH.
const DefaultExecutable = "claude"

// Executable returns the configured binary.
func (c *CLI) Executable() string { return c.bin }

// ConfigDir returns the resolved Claude Code config directory, empty when the
// CLI is left to resolve it from the environment itself.
func (c *CLI) ConfigDir() string { return c.configDir }

// VersionReport is the outcome of a version probe, including the exit code and
// stderr so `ccresu doctor` can explain a failure.
type VersionReport struct {
	Version  Version
	ExitCode int
	Stderr   string
	Command  []string
}

// ProbeVersion runs `claude --version` and parses the result. Unlike Version it
// never caches and always reports what happened.
func (c *CLI) ProbeVersion(ctx context.Context) (VersionReport, error) {
	args := []string{"--version"}
	out, err := c.run(ctx, c.queryTimeout, nil, args...)
	report := VersionReport{
		ExitCode: out.ExitCode,
		Stderr:   strings.TrimSpace(string(out.Stderr)),
		Command:  append([]string{c.bin}, args...),
	}
	if err != nil {
		// A probe that exited non-zero ran, so it is not ErrCLIUnavailable; the
		// same rule as every other command applies here, and doctor gets the
		// captured stderr instead of a bare "exit status 1".
		return report, describeRunError("claude --version", out, err)
	}
	version, parseErr := ParseVersion(string(out.Stdout))
	if parseErr != nil {
		report.Version = version
		return report, parseErr
	}
	report.Version = version
	return report, nil
}

// Version returns the cached CLI version, probing once.
func (c *CLI) Version(ctx context.Context) (Version, error) {
	c.mu.Lock()
	if c.versionRead {
		version, err := c.version, c.versionErr
		c.mu.Unlock()
		return version, err
	}
	c.mu.Unlock()

	report, err := c.ProbeVersion(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.versionRead {
		c.version, c.versionErr, c.versionRead = report.Version, err, true
	} else {
		// Another goroutine finished first; prefer its result.
		version, cachedErr := c.version, c.versionErr
		return version, cachedErr
	}
	return report.Version, err
}

// ResetVersionCache forgets the cached version, e.g. after `claude update`.
func (c *CLI) ResetVersionCache() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.version, c.versionErr, c.versionRead = Version{}, nil, false
}

// AgentsQuery configures `claude agents --json`.
type AgentsQuery struct {
	// All passes --all, which also lists completed background sessions.
	All bool
	// CWD passes --cwd <path> to restrict the listing.
	CWD string
}

// Args returns the verified argument vector for the query.
func (q AgentsQuery) Args() []string {
	args := []string{"agents", "--json"}
	if q.All {
		args = append(args, "--all")
	}
	if cwd := strings.TrimSpace(q.CWD); cwd != "" {
		args = append(args, "--cwd", cwd)
	}
	return args
}

// Agents runs `claude agents --json` and defensively maps the output.
func (c *CLI) Agents(ctx context.Context, query AgentsQuery) ([]AgentEntry, error) {
	version, err := c.Version(ctx)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(version, c.minVersion); err != nil {
		return nil, err
	}
	args := query.Args()
	out, err := c.run(ctx, c.queryTimeout, nil, args...)
	if err != nil || out.ExitCode != 0 {
		return nil, describeRunError("claude agents --json", out, err)
	}
	entries, parseErr := ParseAgentsJSON(out.Stdout)
	if parseErr != nil {
		return nil, parseErr
	}
	return entries, nil
}

// Resume hands the session to the supervisor:
//
//	claude --bg --resume <session-id> --name <label> <message>
//
// It is a handoff, not a run. The print-mode flags are gone: `--bg` cannot be
// combined with `-p`/`--print`, so a single invocation cannot carry both, and
// R12 of the PRD retires print mode rather than keeping it beside the handoff.
// The invocation returns as soon as the supervisor accepts the session, which
// the CLI reports by printing a `backgrounded · <short-id> · <name>` block;
// catching that block is the whole verification this PRD keeps, and proving the
// work happened is a later PRD's job.
//
// dir is the directory the session occupies, never the caller's. The handoff is
// the first ccresu invocation that chooses a working directory deliberately; a
// recovery that ran in the daemon's cwd would resume the wrong tree.
//
// Either refusal that is not a crash is named rather than described: an
// untrusted workspace wraps ErrWorkspaceNotTrusted, and output without a block
// wraps ErrHandoffNotAcknowledged. Neither is retried, because neither gets
// better by trying again with the same arguments.
func (c *CLI) Resume(ctx context.Context, sessionID, message, dir string) (ResumeResult, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return ResumeResult{}, err
	}
	if strings.TrimSpace(message) == "" {
		return ResumeResult{}, ErrEmptyMessage
	}
	if len(message) > MaxMessageBytes {
		return ResumeResult{}, fmt.Errorf("%w: %d bytes (max %d)", ErrMessageTooLong, len(message), MaxMessageBytes)
	}
	if strings.TrimSpace(dir) == "" {
		return ResumeResult{}, fmt.Errorf("%w: session %s", ErrHandoffDirectoryMissing, sessionID)
	}

	version, err := c.Version(ctx)
	if err != nil {
		return ResumeResult{}, err
	}
	if err := checkVersion(version, c.minVersion); err != nil {
		return ResumeResult{}, err
	}

	// The guardrail flags ride the handoff, and an unset knob adds nothing: a
	// config with no guardrails produces the same vector as before PRD 5.
	args := HandoffArgs(sessionID, message, c.guardrails)
	result := ResumeResult{
		RequestedSessionID: sessionID,
		Command:            append([]string{c.bin}, args...),
		CWD:                dir,
	}
	out, runErr := c.runIn(ctx, c.handoffTimeout, dir, nil, args...)
	result.Handoff = Handoff{Raw: strings.TrimSpace(string(out.Stdout))}
	// R4: what the turn cost, when the CLI says. It is read before the error
	// checks because an attempt that failed still spent whatever it spent, and a
	// nil figure stays nil — unknown, never zero (R10).
	result.TotalCostUSD = ParseReportedCost(out.Stdout)
	if runErr != nil || out.ExitCode != 0 {
		if WorkspaceNotTrusted(out) {
			// The remedy is the operator's: trust the directory, or resume
			// somewhere they do trust. It is its own outcome so it is not
			// mistaken for a transient failure and retried on a backoff.
			result.Handoff = Handoff{Raw: strings.TrimSpace(string(out.Stdout))}
			return result, fmt.Errorf("%w: %s", ErrWorkspaceNotTrusted, dir)
		}
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			return result, fmt.Errorf("%w: %w", ErrCLIUnavailable, runErr)
		}
		return result, describeRunError("claude --bg --resume", out, runErr)
	}

	handoff, parseErr := ParseHandoff(out.Stdout)
	result.Handoff = handoff
	result.Backgrounded = handoff.Acknowledged
	result.StateID = handoff.StateID
	result.Name = handoff.Name
	result.Note = handoff.Note
	result.Copied = handoff.Copied
	result.CopySessionID = handoff.CopySessionID
	result.SupervisorStarted = handoff.StartedService
	if parseErr != nil {
		return result, parseErr
	}
	if handoff.Copied {
		// A copy is an expected outcome, not a failure: the continuation lives
		// in a new conversation. The note names it when the CLI says so, and
		// the field is left empty when it does not rather than guessed.
		result.SessionID = handoff.CopySessionID
		return result, nil
	}
	result.SessionID = sessionID
	return result, nil
}

// run executes one command with the per-command timeout applied when the caller
// provided none. It inherits the caller's working directory, which is right for
// the query commands: they inspect the CLI, not a session.
func (c *CLI) run(ctx context.Context, timeout time.Duration, stdin []byte, args ...string) (Output, error) {
	return c.runIn(ctx, timeout, "", stdin, args...)
}

// runIn executes one command in dir.
//
// It exists because the handoff is the one invocation whose working directory is
// part of its meaning: a recovery handed to the supervisor from the daemon's cwd
// would continue the wrong tree. Everything else passes an empty dir and
// inherits, so the deliberate choice is visible at the one call site that makes
// it.
func (c *CLI) runIn(ctx context.Context, timeout time.Duration, dir string, stdin []byte, args ...string) (Output, error) {
	if err := ctx.Err(); err != nil {
		return Output{ExitCode: -1}, err
	}
	runCtx := ctx
	cancel := func() {}
	if _, ok := ctx.Deadline(); !ok && timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	opts := RunOptions{MaxOutputBytes: c.maxOutputBytes, Dir: dir}
	// One config directory for every invocation, whether it is the state read or
	// the handoff (R9). Passing it explicitly rather than inheriting it is what
	// makes "the same supervisor" a property ccresu guarantees instead of a
	// property of how the daemon happened to be started.
	if c.configDir != "" {
		opts.Env = append(opts.Env, EnvConfigDir+"="+c.configDir)
	}
	if len(stdin) > 0 {
		opts.Stdin = bytes.NewReader(stdin)
	}
	return c.exec.Run(runCtx, c.bin, args, opts)
}

// describeRunError turns a failed invocation into an error that carries the
// exit code, the output the CLI produced, and the condition the failure is
// evidence of.
//
// It never echoes the prompt: stdout is included in the message only for a
// usage-limit refusal, where the banner is the whole point of the message.
func describeRunError(what string, out Output, err error) error {
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return fmt.Errorf("%w: %w", ErrCLIUnavailable, err)
	}
	return &CLIError{
		What:     what,
		ExitCode: out.ExitCode,
		Stdout:   out.Stdout,
		Stderr:   out.Stderr,
		Err:      err,
		Kind:     failureKind(out),
	}
}

// CLIError is a command that ran and failed.
//
// It exists because a sentinel alone loses the thing an operator needs: the
// output. The refusal that made ccresu re-attempt a paused session lived on
// stdout, and the old message never looked there, so the reset banner was
// discarded and the failure was reported as a missing binary.
//
// Kind names the condition the failure was recognised as, so callers can branch
// with errors.Is while the message stays readable; the captured streams stay
// available as fields for anything that needs the raw output.
type CLIError struct {
	// What is the invocation being described, e.g. "claude --bg --resume".
	What string
	// ExitCode is the process exit status.
	ExitCode int
	// Stdout and Stderr are the captured streams, truncated to the configured
	// capture limit.
	Stdout []byte
	Stderr []byte
	// Err is the process error, when there was one.
	Err error
	// Kind is the sentinel this failure is evidence of: ErrUsageLimitInForce for
	// a refusal, ErrCLIUnavailable when the process never started, and nil for
	// any other non-zero exit, which is a crash nobody here can explain.
	Kind error
}

// Error renders "<what> failed (exit N): <stderr tail>: <banner>: <err>".
func (e *CLIError) Error() string {
	parts := []string{fmt.Sprintf("%s failed (exit %d)", e.What, e.ExitCode)}
	if detail := tail(string(e.Stderr), 400); detail != "" {
		parts = append(parts, detail)
	}
	if errors.Is(e.Kind, ErrUsageLimitInForce) {
		if detail := tail(string(e.Stdout), 400); detail != "" {
			parts = append(parts, detail)
		}
	}
	if e.Err != nil {
		parts = append(parts, e.Err.Error())
	}
	return strings.Join(parts, ": ")
}

// Unwrap exposes Kind so errors.Is(err, ErrUsageLimitInForce) and
// errors.Is(err, ErrCLIUnavailable) work on the returned error.
func (e *CLIError) Unwrap() error { return e.Kind }

// failureKind names the condition a failed invocation is evidence of.
//
// Only two answers are possible here. A process that never started is the one
// thing ErrCLIUnavailable is for. A refusal that carries a usage-limit banner is
// a wait, not a failure, and saying so is what stops ccresu from hammering a
// paused session. Every other non-zero exit keeps Kind nil: the command ran and
// crashed, and this package cannot say why.
func failureKind(out Output) error {
	if out.ExitCode < 0 {
		return ErrCLIUnavailable
	}
	if isUsageLimitRefusal(out) {
		return ErrUsageLimitInForce
	}
	return nil
}

// usageLimitNouns and usageLimitVerbs are the two halves of the refusal
// vocabulary the CLI uses when the account's usage window has not reset.
//
// Recognition needs both: a bare "usage limit" can appear in ordinary output,
// whereas "5-hour limit reached" or "your weekly limit resets in 2h" cannot be
// anything but a refusal. The list mirrors the decisive phrasings in
// internal/detector; the authoritative parse of the banner — the limit kind and
// the reset instant — stays the detector's job, not this package's.
var (
	usageLimitNouns = []string{
		"usage limit",
		"5-hour limit",
		"five-hour limit",
		"weekly limit",
		"7-day limit",
		"seven-day limit",
		"opus limit",
		"sonnet limit",
		"message limit",
	}
	usageLimitVerbs = []string{
		"reached",
		"hit",
		"exceeded",
		"will reset",
		"resets",
		"in force",
	}
)

// isUsageLimitRefusal reports whether a failed invocation's output is a
// usage-limit refusal.
func isUsageLimitRefusal(out Output) bool {
	text := strings.ToLower(string(out.Stdout) + "\n" + string(out.Stderr))
	return containsAny(text, usageLimitNouns) && containsAny(text, usageLimitVerbs)
}

// containsAny reports whether text contains any of the fragments.
func containsAny(text string, fragments []string) bool {
	for _, fragment := range fragments {
		if strings.Contains(text, fragment) {
			return true
		}
	}
	return false
}

// describeDetail renders "<what> failed (exit N): <stderr tail>: <err>" without
// choosing a sentinel, so a caller that owns a more specific error (such as
// ErrStopFailed) can reuse the same wording.
func describeDetail(what string, out Output, err error) string {
	parts := []string{fmt.Sprintf("%s failed (exit %d)", what, out.ExitCode)}
	if detail := tail(string(out.Stderr), 400); detail != "" {
		parts = append(parts, detail)
	}
	if err != nil {
		parts = append(parts, err.Error())
	}
	return strings.Join(parts, ": ")
}

// tail returns the last n bytes of s on a single line.
//
// The cut is moved forward to a rune boundary so a multi-byte character is
// never split into invalid UTF-8 inside an error message.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if s == "" || n <= 0 {
		return ""
	}
	if len(s) > n {
		cut := len(s) - n
		for cut < len(s) && !utf8.RuneStart(s[cut]) {
			cut++
		}
		s = "…" + s[cut:]
	}
	return strings.Join(strings.Fields(s), " ")
}

// flexBool accepts true/false, "true"/"false" and 0/1.
type flexBool struct {
	value bool
	set   bool
}

// UnmarshalJSON implements json.Unmarshaler.
func (f *flexBool) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return nil
		}
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true", "1", "yes":
			f.value, f.set = true, true
		case "false", "0", "no":
			f.value, f.set = false, true
		}
	case 't':
		f.value, f.set = true, true
	case 'f':
		f.value, f.set = false, true
	default:
		var num json.Number
		if err := json.Unmarshal(trimmed, &num); err == nil {
			if n, convErr := num.Int64(); convErr == nil {
				f.value, f.set = n != 0, true
			}
		}
	}
	return nil
}

// Bool returns the mapped value.
func (f flexBool) Bool() bool { return f.value }
