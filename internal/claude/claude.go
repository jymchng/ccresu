package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// ProviderName identifies this adapter in persisted session records.
const ProviderName = "claude-code"

// Recovery codes reported for a session.
//
// A session is RECOVERABLE only when ccresu knows a verified way to continue
// it. Everything else is reported as UNRECOVERABLE together with a reason; no
// command is ever invented for an unsupported session.
const (
	RecoveryRecoverable   = "RECOVERABLE"
	RecoveryUnrecoverable = "UNRECOVERABLE"
)

// SessionType classifies how a Claude Code session runs.
type SessionType string

// The session types ccresu understands. The names mirror storage.SessionType so
// discovered sessions can be persisted without translation, except for
// SessionTypeBackground which is persisted as headless.
const (
	// SessionTypeInteractive is a session attached to a terminal/TUI.
	SessionTypeInteractive SessionType = "interactive"
	// SessionTypeBackground is a background agent session (`claude agents`).
	SessionTypeBackground SessionType = "background"
	// SessionTypeTranscript is a session known only from its transcript file.
	SessionTypeTranscript SessionType = "transcript"
	// SessionTypeUnknown is a session whose type could not be determined.
	SessionTypeUnknown SessionType = "unknown"
)

// Source records which verified interface a session was discovered through.
type Source string

// The discovery sources.
const (
	// SourceAgents is `claude agents --json`.
	SourceAgents Source = "agents"
	// SourceTranscript is ~/.claude/projects/<slug>/<session-id>.jsonl.
	SourceTranscript Source = "transcript"
	// SourceSessionEnv is ~/.claude/session-env/<session-id>/.
	SourceSessionEnv Source = "session-env"
)

// Errors returned by this package. They are wrapped with context so callers can
// use errors.Is while still showing a useful message.
var (
	// ErrSessionNotFound reports that neither the CLI nor the on-disk stores
	// know the requested session.
	ErrSessionNotFound = errors.New("claude: session not found")
	// ErrUnrecoverable reports that a session exists but cannot be resumed.
	ErrUnrecoverable = errors.New("claude: session is unrecoverable")
	// ErrInvalidSessionID reports a session id that is not a UUID. Rejecting
	// anything else keeps the id from being interpreted as a command-line flag
	// or a path component.
	ErrInvalidSessionID = errors.New("claude: invalid session id")
	// ErrEmptyMessage reports a missing continuation message.
	ErrEmptyMessage = errors.New("claude: empty continuation message")
	// ErrMessageTooLong reports a continuation message above the configured cap.
	ErrMessageTooLong = errors.New("claude: continuation message too long")
	// ErrCLIUnavailable reports that the claude executable could not be run at
	// all: the binary is missing, or the invocation was cancelled before it
	// produced a status. A command that ran and exited non-zero is *not* this
	// error; see CLIError and ErrUsageLimitInForce.
	ErrCLIUnavailable = errors.New("claude: claude executable unavailable")
	// ErrUsageLimitInForce reports that the CLI ran and refused the request
	// because the account's usage window has not reset yet.
	//
	// It is deliberately distinct from ErrCLIUnavailable: the executable was
	// available and the refusal says exactly when the work may continue. Treating
	// it as a broken binary is what made ccresu re-attempt a paused session every
	// reconcile pass instead of waiting for the reset it had just been told
	// about.
	ErrUsageLimitInForce = errors.New("claude: usage limit is still in force")
	// ErrUnsupportedVersion reports a claude version older than the minimum
	// this package is verified against.
	ErrUnsupportedVersion = errors.New("claude: unsupported claude code version")
	// ErrUnrecognizedVersion reports a version string that cannot be parsed.
	ErrUnrecognizedVersion = errors.New("claude: unrecognized claude code version")
	// ErrMalformedOutput reports JSON that does not match the verified shape:
	// the `claude agents --json` listing, or one line of a transcript.
	ErrMalformedOutput = errors.New("claude: malformed claude output")
	// ErrNoSessionIDReported reports a resume whose output named no session id.
	//
	// A blocking `claude --resume <session-id> "<message>"` names the session in
	// its argument vector and prints the turn, not a reply, so there is no id to
	// read back and nothing in this package returns this error any more. It is
	// kept as the name for that condition, so a caller written against the
	// earlier handshake still compiles and still says what it means.
	ErrNoSessionIDReported = errors.New("claude: resume output reported no session id")
	// ErrSessionNotPreserved reports a resume that produced a different session
	// id, i.e. a new conversation instead of the original one.
	//
	// The same is true of it: with the id in the argument vector a substituted
	// one cannot be observed, so the shipped path never returns it. recovery's
	// non-retryable list still names it, because a session that came back as a
	// different conversation is permanent rather than something to retry, and
	// this is that condition's name.
	ErrSessionNotPreserved = errors.New("claude: resumed session id did not match the requested session")
	// ErrResumeFailed names a resume that ran and did not succeed.
	//
	// The verdict arrives as an exit status, not as a reply: CLI.Resume wraps a
	// non-zero one in *CLIError, which carries the exit code and the captured
	// streams, and whose Kind says whether the run was a usage-limit refusal or
	// an unclassified crash. This sentinel is the bare name for the same verdict,
	// for a caller that already holds the failure and has to say what it was.
	// errors.Is against it answers only for errors that carry it explicitly; a
	// *CLIError does not.
	ErrResumeFailed = errors.New("claude: resume reported an error")
)

// sessionIDPattern matches the UUID form Claude Code uses for session ids.
var sessionIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidSessionID reports whether id is a plausible Claude Code session id.
func ValidSessionID(id string) bool { return sessionIDPattern.MatchString(id) }

// ValidateSessionID returns an error wrapping ErrInvalidSessionID when id is not
// a plausible session id.
func ValidateSessionID(id string) error {
	if ValidSessionID(id) {
		return nil
	}
	return fmt.Errorf("%w: %q is not a UUID", ErrInvalidSessionID, id)
}

// Session is one Claude Code session discovered through a verified interface.
type Session struct {
	// ID is the Claude Code session UUID.
	ID string
	// Type classifies the session.
	Type SessionType
	// Kind preserves the raw `kind` reported by `claude agents --json`
	// ("interactive", "background") or "transcript" for on-disk discoveries.
	Kind string
	// ProjectPath is where the session started: the launch directory ccresu
	// first saw, kept stable so the fallback and the display have a fixed
	// meaning. The observed `cwd` no longer overwrites it (PRD 6 R1).
	ProjectPath string
	// RunsInPath is the directory the session actually runs in, refreshed from
	// the published `cwd` on every observation (PRD 6 R1, R4). For a background
	// session that is its worktree under `.claude/worktrees/`, which is the only
	// correct answer for a recovery; empty means "not observed yet", and the
	// resolver falls back to ProjectPath.
	RunsInPath string
	// TranscriptPath is the on-disk .jsonl transcript, when known.
	TranscriptPath string
	// Source records how the session was discovered.
	Source Source
	// PID is the process id reported by the CLI, zero when not running.
	PID int
	// Name is the (sanitised) session name reported by the CLI.
	Name string
	// Status is the raw CLI status: busy, idle or waiting.
	Status string
	// State is the raw CLI state for background sessions: working, blocked,
	// done, failed or stopped.
	State string
	// WaitingFor is set when the CLI reports what a waiting session waits for.
	WaitingFor string
	// StartedAt is when the session started, zero when unknown.
	StartedAt time.Time
	// LastActivity is the newest timestamp known for the session.
	LastActivity time.Time
	// TranscriptSize and TranscriptLines snapshot the transcript so a later
	// resume can prove the conversation grew.
	TranscriptSize  int64
	TranscriptLines int64
	// HasSessionEnv reports whether ~/.claude/session-env/<id>/ exists.
	HasSessionEnv bool
	// Recoverable is false when the session cannot be resumed; Reason explains
	// why and is always set in that case.
	Recoverable bool
	// Reason carries the human-readable reason for a negative Recoverable.
	Reason string
	// DiscoveredAt is when ccresu first saw the session.
	DiscoveredAt time.Time
	// LastSeenAt is the most recent discovery time.
	LastSeenAt time.Time
}

// RecoveryCode returns RECOVERABLE or UNRECOVERABLE for display.
func (s Session) RecoveryCode() string {
	if s.Recoverable {
		return RecoveryRecoverable
	}
	return RecoveryUnrecoverable
}

// Unrecoverable reports whether the session must not be resumed.
func (s Session) Unrecoverable() bool { return !s.Recoverable }

// String renders a one-line summary, useful for logs and CLI output.
func (s Session) String() string {
	var b strings.Builder
	b.WriteString(string(s.Type))
	if s.Kind != "" && s.Kind != string(s.Type) {
		b.WriteString(":")
		b.WriteString(s.Kind)
	}
	b.WriteString(" ")
	b.WriteString(s.ID)
	if s.ProjectPath != "" {
		b.WriteString(" cwd=")
		b.WriteString(s.ProjectPath)
	}
	b.WriteString(" ")
	b.WriteString(s.RecoveryCode())
	if s.Reason != "" {
		b.WriteString(" (")
		b.WriteString(s.Reason)
		b.WriteString(")")
	}
	return b.String()
}

// DiscoverFilter narrows a discovery pass. The zero value discovers every
// session, including unrecoverable ones.
type DiscoverFilter struct {
	// UnderCWD keeps only sessions whose project path is cwd or inside it.
	UnderCWD string
	// IDs keeps only these session ids. Unknown ids are simply absent.
	IDs []string
	// RecoverableOnly drops sessions that cannot be resumed.
	RecoverableOnly bool
	// All asks the CLI to include completed background sessions. When nil the
	// provider default is used.
	All *bool
}

// matches reports whether s passes the filter.
func (f DiscoverFilter) matches(s Session) bool {
	if f.RecoverableOnly && !s.Recoverable {
		return false
	}
	if f.UnderCWD != "" && !underPath(s.ProjectPath, f.UnderCWD) {
		return false
	}
	if len(f.IDs) > 0 {
		for _, id := range f.IDs {
			if id == s.ID {
				return true
			}
		}
		return false
	}
	return true
}

// underPath reports whether p is root or inside root.
//
// Comparison goes through filepath.Rel so it behaves the same on every
// platform: on Windows a naive prefix test would never match because the
// separator is '\\', silently hiding every session.
func underPath(p, root string) bool {
	if strings.TrimSpace(p) == "" || strings.TrimSpace(root) == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p))
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if filepath.IsAbs(rel) {
		return false
	}
	// ".." or a path starting with ".." + separator escapes the root.
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// SessionStatus is a point-in-time view of a session.
type SessionStatus struct {
	// Session is the discovered session record.
	Session Session
	// Running reports whether the session currently has a live process.
	Running bool
	// Status and State copy the raw CLI values.
	Status string
	State  string
	// WaitingFor copies the CLI's waiting reason, when present.
	WaitingFor string
	// Transcript is the freshly scanned transcript metadata.
	Transcript TranscriptInfo
	// CheckedAt is when the status was captured.
	CheckedAt time.Time
}

// ResumeResult is the outcome of one resume invocation.
type ResumeResult struct {
	// RequestedSessionID is the session id ccresu asked to resume.
	RequestedSessionID string
	// ExitCode is the process exit status of the resume.
	//
	// Zero is the verdict that the continuation ran. It is the only success
	// signal the command has: `claude --resume <session-id> "<message>"` returns
	// when the turn is over, and prints the turn rather than a reply, so there is
	// no acknowledgement to parse and nothing else to compare against.
	ExitCode int
	// CWD is the working directory the resume was given.
	CWD string
	// TotalCostUSD is what the CLI reported the turn cost, in US dollars.
	//
	// Nil means *not reported*, and that is the normal case: an interactive
	// resume prints no cost block. It is a pointer rather than a zero float
	// because PRD 5 R10 requires an unreported cost to be recorded as unknown
	// and to neither count toward a spend cap nor block a resume — a zero would
	// make every unmeasured attempt look free.
	TotalCostUSD *float64
	// Command is the exact argv that was executed.
	Command []string
}

// SessionProvider discovers Claude Code sessions and resumes a paused one.
//
// Implementations must never invent a command: resuming uses
// `claude --resume <session-id> "<message>"` and nothing else. The command runs
// the turn to its end, so the exit status — not a supervisor's acknowledgement —
// is what a recovery is judged by.
type SessionProvider interface {
	// Discover lists known sessions, newest activity first.
	Discover(ctx context.Context, filter DiscoverFilter) ([]Session, error)
	// GetSession returns one session or ErrSessionNotFound.
	GetSession(ctx context.Context, sessionID string) (Session, error)
	// GetSessionStatus returns the current status of one session.
	GetSessionStatus(ctx context.Context, sessionID string) (SessionStatus, error)
	// ResumeSession continues an existing session.
	ResumeSession(ctx context.Context, sessionID, message string) (ResumeResult, error)
}

// SessionResumerIn is implemented by providers that can hand off in a directory
// the caller has already resolved (PRD 6 R3).
//
// It is separate from SessionProvider because it is optional: a fake or a
// provider without a working directory to choose does not need it, and the
// recovery engine falls back to SessionProvider.ResumeSession — which resolves
// the directory itself — when the provider does not implement it.
type SessionResumerIn interface {
	ResumeSessionIn(ctx context.Context, sessionID, message, dir string) (ResumeResult, error)
}

// sortSessions orders sessions by recency and then by id so discovery output is
// deterministic.
func sortSessions(sessions []Session) {
	sort.SliceStable(sessions, func(i, j int) bool {
		a, b := sessions[i], sessions[j]
		if !a.LastActivity.Equal(b.LastActivity) {
			return a.LastActivity.After(b.LastActivity)
		}
		at, bt := a.StartedAt, b.StartedAt
		if !at.Equal(bt) {
			return at.After(bt)
		}
		return a.ID < b.ID
	})
}

// ParseReportedCost reads the cost the CLI reported for a turn, or nil when it
// reported none.
//
// The blocking resume prints the turn, and nothing in that output is priced, so
// nil is the ordinary answer rather than a failure. The one shape worth scanning
// for is a JSON result object printed alongside it —
// `{"type":"result","total_cost_usd":0.42}` — because that is where the CLI has
// ever stated a figure. It is scanned line by line rather than decoded as one
// document, because the prose and the result can share stdout. A value that is
// not a finite, non-negative number reads as *unknown* rather than zero (R10): a
// zero would make an unmeasured attempt look free, and a negative or absurd
// figure could move a session past or below a cap.
func ParseReportedCost(stdout []byte) *float64 {
	for _, line := range strings.Split(string(stdout), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "{") || !strings.Contains(trimmed, `"total_cost_usd"`) {
			continue
		}
		var reported struct {
			TotalCostUSD *json.Number `json:"total_cost_usd"`
		}
		if err := json.Unmarshal([]byte(trimmed), &reported); err != nil || reported.TotalCostUSD == nil {
			continue
		}
		value, err := reported.TotalCostUSD.Float64()
		if err != nil {
			continue
		}
		return KnownCost(&value)
	}
	return nil
}

// KnownCost turns a provider-reported cost into the value PRD 5 may act on, or
// nil when nothing usable was reported.
//
// A negative, NaN or infinite figure is treated as *unknown* rather than as a
// number: the cap exists to stop spending, and a bad value must never be able to
// move a session past it (by looking enormous) or below it (by looking negative
// or zero). Silently clamping instead would record a cost the CLI never stated.
func KnownCost(reported *float64) *float64 {
	if reported == nil {
		return nil
	}
	value := *reported
	if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}
