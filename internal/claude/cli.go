package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
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
	// Logger receives the recovery diagnostics for a handoff that needed a
	// retry or a directory decision. Empty means slog.Default().
	Logger *slog.Logger
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
		handoffTimeout: opts.HandoffTimeout,
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

	args := HandoffArgs(sessionID, message)
	result := ResumeResult{
		RequestedSessionID: sessionID,
		Command:            append([]string{c.bin}, args...),
		CWD:                dir,
	}
	out, runErr := c.runIn(ctx, c.handoffTimeout, dir, nil, args...)
	result.Handoff = Handoff{Raw: strings.TrimSpace(string(out.Stdout))}
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
	// What is the invocation being described, e.g. "claude -p --resume".
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

// ResumeOutput is the verified `-p --output-format json` result object.
//
// Verified shape (Claude Code 2.1.223):
//
//	{"type":"result","subtype":"success","is_error":false,"duration_ms":1234,
//	 "num_turns":2,"result":"…","session_id":"<uuid>","total_cost_usd":0.01,
//	 "usage":{…},"modelUsage":{"claude-sonnet-4":{"…":true}}}
//
// Every field is optional in the mapping: a missing session_id is reported as a
// failure rather than assumed to match.
type ResumeOutput struct {
	Type         string
	Subtype      string
	IsError      bool
	Result       string
	SessionID    string
	NumTurns     int
	DurationMS   int
	TotalCostUSD float64
	Model        string
	Raw          json.RawMessage
}

// resumeWire is the permissive wire form of the resume result.
type resumeWire struct {
	Type         flexString `json:"type"`
	Subtype      flexString `json:"subtype"`
	IsError      flexBool   `json:"is_error"`
	Result       flexString `json:"result"`
	SessionID    flexString `json:"session_id"`
	SessionIDAlt flexString `json:"sessionId"`
	NumTurns     flexInt    `json:"num_turns"`
	DurationMS   flexInt    `json:"duration_ms"`
	Cost         flexFloat  `json:"total_cost_usd"`
	Model        flexString `json:"model"`
	ModelUsage   flexModel  `json:"modelUsage"`
	Error        flexString `json:"error"`
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

// flexFloat accepts a JSON number or a numeric string.
type flexFloat struct {
	value float64
	set   bool
}

// UnmarshalJSON implements json.Unmarshaler.
func (f *flexFloat) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var num json.Number
	if err := json.Unmarshal(trimmed, &num); err == nil {
		if v, convErr := num.Float64(); convErr == nil {
			f.value, f.set = v, true
		}
		return nil
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err != nil {
		return nil
	}
	if v, convErr := strconv.ParseFloat(strings.TrimSpace(s), 64); convErr == nil {
		f.value, f.set = v, true
	}
	return nil
}

// Float returns the mapped value.
func (f flexFloat) Float() float64 { return f.value }

// flexModel extracts the first model name from the modelUsage map, whose values
// have changed shape between releases.
type flexModel struct {
	value string
}

// UnmarshalJSON implements json.Unmarshaler.
func (f *flexModel) UnmarshalJSON(data []byte) error {
	var usage map[string]json.RawMessage
	if err := json.Unmarshal(data, &usage); err != nil {
		return nil
	}
	names := make([]string, 0, len(usage))
	for name := range usage {
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil
	}
	// Deterministic choice: shortest name, then lexical, avoids map ordering.
	best := names[0]
	for _, name := range names[1:] {
		if len(name) < len(best) || (len(name) == len(best) && name < best) {
			best = name
		}
	}
	f.value = best
	return nil
}

// String returns the mapped model name.
func (f flexModel) String() string { return f.value }

// ParseResumeJSON maps `-p --output-format json` output.
//
// The verified output is a single JSON object; a newline-delimited stream or a
// JSON array (as produced by --output-format stream-json) is also accepted by
// taking the last result-shaped object, because the trailing object is the one
// that summarises the turn.
func ParseResumeJSON(data []byte) (*ResumeOutput, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("%w: empty resume output", ErrMalformedOutput)
	}

	candidates := [][]byte{}
	if trimmed[0] == '[' {
		var raws []json.RawMessage
		if err := json.Unmarshal(trimmed, &raws); err != nil {
			return nil, fmt.Errorf("%w: resume array: %v", ErrMalformedOutput, err)
		}
		for _, raw := range raws {
			candidates = append(candidates, raw)
		}
	} else if trimmed[0] == '{' {
		if json.Valid(trimmed) {
			candidates = append(candidates, trimmed)
		} else {
			// Fall back to newline-delimited objects.
			for _, line := range bytes.Split(trimmed, []byte("\n")) {
				line = bytes.TrimSpace(line)
				if len(line) > 0 && line[0] == '{' && json.Valid(line) {
					candidates = append(candidates, line)
				}
			}
		}
	} else {
		return nil, fmt.Errorf("%w: resume output is not JSON", ErrMalformedOutput)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("%w: no JSON object in resume output", ErrMalformedOutput)
	}

	// The result object is the last one in a stream, so later candidates win.
	var chosen *resumeWire
	var chosenRaw json.RawMessage
	for _, raw := range candidates {
		var wire resumeWire
		if err := json.Unmarshal(raw, &wire); err != nil {
			continue
		}
		kind := strings.ToLower(wire.Type.String())
		if kind != "" && kind != "result" && wire.Result.String() == "" {
			// stream-json also emits assistant/user/system objects.
			continue
		}
		if kind == "" && wire.SessionID.String() == "" && wire.Result.String() == "" && !wire.IsError.set {
			continue
		}
		copied := wire
		chosen = &copied
		chosenRaw = append(json.RawMessage(nil), raw...)
	}
	if chosen == nil {
		return nil, fmt.Errorf("%w: resume output had no result object", ErrMalformedOutput)
	}

	out := &ResumeOutput{
		Type:         chosen.Type.String(),
		Subtype:      chosen.Subtype.String(),
		IsError:      chosen.IsError.Bool(),
		Result:       firstNonEmpty(chosen.Result.String(), chosen.Error.String()),
		SessionID:    firstNonEmpty(chosen.SessionID.String(), chosen.SessionIDAlt.String()),
		NumTurns:     chosen.NumTurns.Int(),
		DurationMS:   chosen.DurationMS.Int(),
		TotalCostUSD: chosen.Cost.Float(),
		Model:        firstNonEmpty(chosen.Model.String(), chosen.ModelUsage.String()),
		Raw:          chosenRaw,
	}
	if out.Subtype == "" && out.IsError {
		out.Subtype = "error"
	}
	return out, nil
}
