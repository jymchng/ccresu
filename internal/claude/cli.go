package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Default command timeouts.
const (
	// DefaultQueryTimeout bounds `--version` and `agents --json`.
	DefaultQueryTimeout = 30 * time.Second
	// DefaultResumeTimeout bounds a resume turn, which runs a full model turn.
	DefaultResumeTimeout = 15 * time.Minute
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
	// ResumeTimeout bounds a resume invocation.
	ResumeTimeout time.Duration
	// MaxOutputBytes caps captured output.
	MaxOutputBytes int
	// MinVersion overrides the supported-version floor.
	MinVersion *Version
	// Logger receives the recovery diagnostics for a resume that needed a
	// `claude stop` first. Empty means slog.Default().
	Logger *slog.Logger
}

// CLI is the version-aware adapter around the claude executable.
//
// It exposes exactly the five verified invocations:
//
//	claude --version
//	claude agents --json [--all] [--cwd <path>]
//	claude --resume <session-id> --bg <message>
//	claude stop <job-id>
//	(the transcript and session-env files are read, not executed)
//
// `claude stop` takes an eight-hex background job name, not the session UUID --
// the CLI resolves job names only and answers a UUID with "No job matching" and
// exit 1. It is issued only when a resume is refused because the session is
// still attached to a background agent, and the job name is read out of that
// refusal: its conversation is kept, so the same resume can follow immediately. No other command is ever constructed, and the
// message is always passed as a single argv element so no shell ever sees it.
type CLI struct {
	bin            string
	exec           Execer
	queryTimeout   time.Duration
	resumeTimeout  time.Duration
	maxOutputBytes int
	minVersion     Version
	log            *slog.Logger

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
		resumeTimeout:  opts.ResumeTimeout,
		maxOutputBytes: opts.MaxOutputBytes,
		log:            opts.Logger,
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
	if c.resumeTimeout <= 0 {
		c.resumeTimeout = DefaultResumeTimeout
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
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return report, fmt.Errorf("%w: %w", ErrCLIUnavailable, err)
		}
		return report, fmt.Errorf("%w: %s: %v", ErrCLIUnavailable, c.bin, err)
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

// Resume dispatches the resume command in the background:
//
//	claude --resume <session-id> --bg <message>
//
// The CLI acknowledges the dispatch on stdout with `backgrounded · <job-id>` and
// exits 0. `--output-format json` is deliberately absent: JSON output exists only
// in print mode, and print mode refuses `--bg` outright ("--bg and --print
// conflict"), so the job id is the only thing a dispatch can report. The
// continuation session id, the transcript and the outcome are visible only
// afterwards -- through `claude agents --json` and the transcript file -- which
// is what the verifier reads. Proving continuation is not this call's job.
//
// One situation still needs a second command first. The CLI refuses to resume a
// session that is still attached to a background agent. Because
// `claude stop <job-id>` keeps both the conversation and the session id, ccresu
// answers that refusal by stopping the background agent and repeating the
// identical dispatch once. The stop takes the eight-hex job name the refusal
// itself prints, not the session UUID; see stop.go. Only that exact refusal is
// handled this way: every other failure, a stop that itself fails, and a resume
// that still refuses after a successful stop are returned unchanged.
//
// The session id is validated first so it can never be mistaken for a flag.
func (c *CLI) Resume(ctx context.Context, sessionID, message string) (ResumeResult, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return ResumeResult{}, err
	}
	if strings.TrimSpace(message) == "" {
		return ResumeResult{}, ErrEmptyMessage
	}
	if len(message) > MaxMessageBytes {
		return ResumeResult{}, fmt.Errorf("%w: %d bytes (max %d)", ErrMessageTooLong, len(message), MaxMessageBytes)
	}

	version, err := c.Version(ctx)
	if err != nil {
		return ResumeResult{}, err
	}
	if err := checkVersion(version, c.minVersion); err != nil {
		return ResumeResult{}, err
	}

	args := ResumeArgs(sessionID, message)
	result, dispatchErr := c.resumeOnce(ctx, sessionID, args)
	if dispatchErr == nil || !IsSessionRunningInBackground(dispatchErr) {
		return result, dispatchErr
	}

	// The refusal says the session is busy elsewhere, not that the conversation
	// is gone. Stopping it is bookkeeping: the transcript and the session id
	// survive, so the dispatch that follows is the same command with the same
	// arguments. The stop is recorded in the log only; the attempt row keeps
	// showing the one `claude --resume ...` command.
	jobID := BackgroundJobID(dispatchErr)
	if jobID == "" {
		c.log.Warn("the refusal named no background job id; cannot stop the session holding it",
			"session", sessionID)
		return result, dispatchErr
	}
	c.log.Warn("claude refused the resume because the session is attached to a background agent; stopping it and dispatching again",
		"session", sessionID, "job", jobID)
	if stopErr := c.Stop(ctx, jobID); stopErr != nil {
		// Without the stop the session is still in the way, so the original
		// refusal is the honest result.
		c.log.Warn("claude stop did not clear the background session; reporting the original resume failure",
			"session", sessionID, "job", jobID, "error", stopErr)
		return result, dispatchErr
	}
	c.log.Info("stopped the background session; repeating the identical dispatch",
		"session", sessionID, "job", jobID)

	retried, retryErr := c.resumeOnce(ctx, sessionID, args)
	if retryErr != nil {
		c.log.Warn("resume failed again after stopping the background session",
			"session", sessionID, "error", retryErr)
	}
	return retried, retryErr
}

// resumeOnce runs one background dispatch and reads the job id it reports.
//
// It exists so the background-session recovery repeats exactly the same command:
// the argument vector is built once by the caller and handed in, never rebuilt
// by a second, subtly different code path.
func (c *CLI) resumeOnce(ctx context.Context, sessionID string, args []string) (ResumeResult, error) {
	out, runErr := c.run(ctx, c.resumeTimeout, nil, args...)
	result := ResumeResult{
		RequestedSessionID: sessionID,
		Command:            append([]string{c.bin}, args...),
	}
	if runErr != nil || out.ExitCode != 0 {
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			return result, fmt.Errorf("%w: %w", ErrCLIUnavailable, runErr)
		}
		return result, describeRunError("claude --resume --bg", out, runErr)
	}
	jobID, ok := ParseBackgroundDispatch(out.Stdout)
	if !ok {
		return result, fmt.Errorf("%w: the dispatch printed no background job id: %q",
			ErrNoBackgroundJob, tail(string(out.Stdout), 200))
	}
	result.JobID = jobID
	return result, nil
}

// ResumeArgs returns the resume argument vector.
//
// It carries no `-p` and no `--output-format`, because print mode refuses `--bg`
// ("--bg and --print conflict: --print never starts the interactive session that
// `claude agents` attaches to"). A resume is a background dispatch by design:
// what the dispatch cannot report -- the JSON result, the turn count, the cost --
// is reported as unavailable rather than guessed at.
func ResumeArgs(sessionID, message string) []string {
	return []string{"--resume", sessionID, "--bg", message}
}

// backgroundedPattern matches the CLI's dispatch acknowledgement:
//
//	backgrounded · 93874eb3
//
// The job id is the eight lower-case hex name `claude stop` and `claude attach`
// accept. The separator is tolerated as a middle dot, a colon or whitespace, so
// a restyled line still parses.
var backgroundedPattern = regexp.MustCompile(`(?i)backgrounded\s*(?:[\x{00b7}:\x{2022}-]\s*)?([0-9a-f]{8})\b`)

// ParseBackgroundDispatch extracts the job id from a dispatch's stdout.
//
// It returns ("", false) rather than an error: a dispatch that printed no job id
// is a failure the caller reports with its own sentinel, and the caller has the
// output to quote.
func ParseBackgroundDispatch(stdout []byte) (string, bool) {
	text := ansiEscape.ReplaceAll(stdout, []byte(" "))
	match := backgroundedPattern.FindSubmatch(text)
	if match == nil {
		return "", false
	}
	id := strings.ToLower(string(match[1]))
	if !ValidBackgroundJobID(id) {
		return "", false
	}
	return id, true
}

// run executes one command with the per-command timeout applied when the caller
// provided none.
func (c *CLI) run(ctx context.Context, timeout time.Duration, stdin []byte, args ...string) (Output, error) {
	if err := ctx.Err(); err != nil {
		return Output{ExitCode: -1}, err
	}
	runCtx := ctx
	cancel := func() {}
	if _, ok := ctx.Deadline(); !ok && timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	opts := RunOptions{MaxOutputBytes: c.maxOutputBytes}
	if len(stdin) > 0 {
		opts.Stdin = bytes.NewReader(stdin)
	}
	return c.exec.Run(runCtx, c.bin, args, opts)
}

// describeRunError turns a failed invocation into a message that carries the
// exit code and the tail of stderr, without ever echoing a prompt.
func describeRunError(what string, out Output, err error) error {
	if err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		return fmt.Errorf("%w: %w", ErrCLIUnavailable, err)
	}
	return fmt.Errorf("%w: %s", ErrCLIUnavailable, describeDetail(what, out, err))
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
