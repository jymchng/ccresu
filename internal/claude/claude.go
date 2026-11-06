package claude

import (
	"context"
	"errors"
	"fmt"
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
	// ErrCLIUnavailable reports that the claude executable could not be run.
	ErrCLIUnavailable = errors.New("claude: claude executable unavailable")
	// ErrUnsupportedVersion reports a claude version older than the minimum
	// this package is verified against.
	ErrUnsupportedVersion = errors.New("claude: unsupported claude code version")
	// ErrUnrecognizedVersion reports a version string that cannot be parsed.
	ErrUnrecognizedVersion = errors.New("claude: unrecognized claude code version")
	// ErrNoBackgroundJob reports a resume dispatch that exited 0 but printed no
	// background job id, so there is nothing to follow and nothing to verify.
	ErrNoBackgroundJob = errors.New("claude: the resume dispatch reported no background job id")
	// ErrMalformedOutput reports JSON that does not match the verified shape.
	ErrMalformedOutput = errors.New("claude: malformed claude output")
	// ErrNoSessionIDReported reports resume output without a session id. Without
	// it ccresu cannot prove the original conversation was continued, so the
	// attempt is treated as failed rather than assumed successful.
	ErrNoSessionIDReported = errors.New("claude: resume output reported no session id")
	// ErrSessionNotPreserved reports a resume that produced a different session
	// id, i.e. a new conversation instead of the original one.
	ErrSessionNotPreserved = errors.New("claude: resumed session id did not match the requested session")
	// ErrResumeFailed reports a resume whose JSON output carried is_error.
	ErrResumeFailed = errors.New("claude: resume reported an error")
	// ErrSessionRunningInBackground reports the CLI refusing to resume a
	// session that is still attached to a background agent. It is the one
	// condition ccresu answers with another command (`claude stop <job-id>`)
	// before repeating the same resume.
	ErrSessionRunningInBackground = errors.New("claude: session is running in the background")
	// ErrStopFailed reports that `claude stop <job-id>` itself did not succeed,
	// so the background session is still in the way.
	ErrStopFailed = errors.New("claude: session stop failed")
	// ErrInvalidBackgroundJobID reports a `claude stop` argument that is not the
	// eight-hex background job name the CLI resolves. A session UUID belongs
	// here: the CLI answers it with "No job matching" and exit 1, so passing it
	// is refused locally instead of costing a failed recovery.
	ErrInvalidBackgroundJobID = errors.New("claude: invalid background job id")
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
	// ProjectPath is the working directory the session belongs to.
	ProjectPath string
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

// ResumeResult is the outcome of a resume dispatch.
//
// A resume is dispatched into the background, so this is what a dispatch can
// honestly report: the id that was addressed and the background job that now
// holds the conversation. The continuation session id, the transcript growth and
// the turn's outcome are not part of it -- they are visible only afterwards,
// through `claude agents --json` and the transcript, and are what verification
// measures.
type ResumeResult struct {
	// RequestedSessionID is the session id ccresu asked to resume.
	RequestedSessionID string
	// JobID is the eight-hex background job the dispatch created, taken from the
	// CLI's own acknowledgement ("backgrounded · <job-id>").
	JobID string
	// ContinuationSessionID is the session id that job reports afterwards. It is
	// filled in by verification, never by the dispatch: a background agent
	// continues the conversation under its own session id.
	ContinuationSessionID string
	// Command is the exact argv that was executed.
	Command []string
}

// SessionProvider discovers and resumes Claude Code sessions.
//
// Implementations must never invent a command: resuming dispatches
// `claude --resume <session-id> --bg "<message>"` and, only when the CLI refuses
// that because the session is still attached to a background agent, the single
// `claude stop <job-id>` that clears the way before the same dispatch is
// repeated. The stop's job id is the eight-hex name the refusal prints, never the
// session UUID; the dispatch's own job id is the eight-hex name it prints on
// stdout.
type SessionProvider interface {
	// Discover lists known sessions, newest activity first.
	Discover(ctx context.Context, filter DiscoverFilter) ([]Session, error)
	// GetSession returns one session or ErrSessionNotFound.
	GetSession(ctx context.Context, sessionID string) (Session, error)
	// GetSessionStatus returns the current status of one session.
	GetSessionStatus(ctx context.Context, sessionID string) (SessionStatus, error)
	// ResumeSession dispatches a continuation of an existing session into the
	// background and reports the job that now holds it. Proving that the
	// conversation continued is the verifier's job, not this call's.
	ResumeSession(ctx context.Context, sessionID, message string) (ResumeResult, error)
	// BackgroundJob looks up one background job by its eight-hex job id, newest
	// `claude agents --json` listing first. It returns ErrSessionNotFound when no
	// job matches.
	BackgroundJob(ctx context.Context, jobID string) (AgentEntry, error)
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
