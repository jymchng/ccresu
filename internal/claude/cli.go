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
//	claude -p --resume <session-id> <message> --output-format json
//	claude stop <session-id>
//	(the transcript and session-env files are read, not executed)
//
// `claude stop` is issued only when a resume is refused because the session is
// still attached to a background agent: its conversation is kept, so the same
// resume can follow immediately. No other command is ever constructed, and the
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

// Resume runs the verified resume command:
//
//	claude -p --resume <session-id> <message> --output-format json
//
// One situation needs a second command first. The CLI refuses to resume a
// session that is still attached to a background agent ("That session is
// running in the background"). Because `claude stop <id>` keeps both the
// conversation and the session id, ccresu answers that refusal by stopping the
// background agent and repeating the identical resume argument vector once.
// Only that exact refusal is handled this way: every other failure, a stop that
// itself fails, and a resume that still refuses after a successful stop are
// returned unchanged, so the existing classification (permanent, no attempt
// spent) is untouched.
//
// The session id is validated first so it can never be mistaken for a flag, and
// the returned session id must equal the requested one; otherwise the call
// fails with ErrSessionNotPreserved instead of reporting a false success.
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
	result, resumeErr := c.resumeOnce(ctx, sessionID, args)
	if resumeErr == nil || !IsSessionRunningInBackground(resumeErr) {
		return result, resumeErr
	}

	// The refusal says the session is busy elsewhere, not that the conversation
	// is gone. Stopping it is bookkeeping: the transcript and the session id
	// survive, so the resume that follows is the same command with the same
	// arguments. The stop is recorded in the log only; the attempt row keeps
	// showing the one `claude -p --resume …` command.
	c.log.Warn("claude refused the resume because the session is attached to a background agent; stopping it and resuming again",
		"session", sessionID)
	if stopErr := c.Stop(ctx, sessionID); stopErr != nil {
		// Without the stop the session is still in the way, so the original
		// refusal is the honest result. Returning it untouched keeps the
		// existing permanent classification intact.
		c.log.Warn("claude stop did not clear the background session; reporting the original resume failure",
			"session", sessionID, "error", stopErr)
		return result, resumeErr
	}
	c.log.Info("stopped the background session; repeating the identical resume", "session", sessionID)

	retried, retryErr := c.resumeOnce(ctx, sessionID, args)
	if retryErr != nil {
		c.log.Warn("resume failed again after stopping the background session",
			"session", sessionID, "error", retryErr)
	}
	return retried, retryErr
}

// resumeOnce runs one resume invocation and interprets its output.
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

	parsed, parseErr := ParseResumeJSON(out.Stdout)
	if parsed != nil {
		result.SessionID = parsed.SessionID
		result.Result = parsed.Result
		result.Subtype = parsed.Subtype
		result.IsError = parsed.IsError
		result.NumTurns = parsed.NumTurns
		result.DurationMS = parsed.DurationMS
		result.TotalCostUSD = parsed.TotalCostUSD
		result.Model = parsed.Model
	}

	if runErr != nil || out.ExitCode != 0 {
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			return result, fmt.Errorf("%w: %w", ErrCLIUnavailable, runErr)
		}
		return result, describeRunError("claude -p --resume", out, runErr)
	}
	if parseErr != nil {
		return result, parseErr
	}
	if parsed.IsError {
		return result, fmt.Errorf("%w: %s", ErrResumeFailed, firstNonEmpty(parsed.Result, parsed.Subtype, "unknown error"))
	}
	if result.SessionID == "" {
		return result, ErrNoSessionIDReported
	}
	if result.SessionID != sessionID {
		return result, fmt.Errorf("%w: got %s, want %s", ErrSessionNotPreserved, result.SessionID, sessionID)
	}
	result.Verified = true
	return result, nil
}

// ResumeArgs returns the verified resume argument vector.
//
// Exported so tests and the recovery audit trail can assert the exact command
// without reimplementing it.
func ResumeArgs(sessionID, message string) []string {
	return []string{"-p", "--resume", sessionID, message, "--output-format", "json"}
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
