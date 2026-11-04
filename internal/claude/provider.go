package claude

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jymchng/ccresu/internal/clock"
)

// Known session kinds reported by `claude agents --json`.
const (
	kindInteractive = "interactive"
	kindBackground  = "background"
)

// Options configures the default SessionProvider.
type Options struct {
	// Executable is the claude binary. Empty means "claude".
	Executable string
	// Home is the Claude Code data directory. Empty means $CLAUDE_CONFIG_DIR or
	// ~/.claude.
	Home string
	// Execer runs commands. Nil means OSExecer.
	Execer Execer
	// Clock supplies time. Nil means the system clock.
	Clock clock.Clock
	// Logger receives discovery diagnostics. Nil means slog.Default().
	Logger *slog.Logger
	// QueryTimeout bounds `--version` and `agents --json`.
	QueryTimeout time.Duration
	// ResumeTimeout bounds a resume invocation.
	ResumeTimeout time.Duration
	// MaxOutputBytes caps captured command output.
	MaxOutputBytes int
	// All passes --all to `claude agents --json` by default.
	All bool
	// CWD restricts discovery to sessions under this directory.
	CWD string
	// MinimumVersion overrides the supported-version floor. Tests use it to
	// keep fixtures stable.
	MinimumVersion *Version
}

// Provider is the default SessionProvider: `claude agents --json` for live
// sessions, the on-disk stores for everything else.
type Provider struct {
	cli   *CLI
	store *Store
	clock clock.Clock
	log   *slog.Logger

	all bool
	cwd string

	mu    sync.Mutex
	known map[string]Session
}

// New builds a provider. It validates its options but runs no commands, so it
// is safe to construct before the CLI is installed.
func New(opts Options) (*Provider, error) {
	store, err := NewStore(opts.Home)
	if err != nil {
		return nil, err
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.System()
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}
	execer := opts.Execer
	if execer == nil {
		execer = OSExecer{}
	}
	cli := NewCLI(CLIOptions{
		Executable:     opts.Executable,
		Execer:         execer,
		QueryTimeout:   opts.QueryTimeout,
		ResumeTimeout:  opts.ResumeTimeout,
		MaxOutputBytes: opts.MaxOutputBytes,
		MinVersion:     opts.MinimumVersion,
	})
	return &Provider{
		cli:   cli,
		store: store,
		clock: clk,
		log:   logger,
		all:   opts.All,
		cwd:   strings.TrimSpace(opts.CWD),
		known: make(map[string]Session),
	}, nil
}

// CLI exposes the configured CLI adapter, e.g. for `ccresu doctor`.
func (p *Provider) CLI() *CLI { return p.cli }

// Store exposes the on-disk store, e.g. for transcript inspection.
func (p *Provider) Store() *Store { return p.store }

// Version reports the Claude Code version ccresu is talking to.
func (p *Provider) Version(ctx context.Context) (VersionReport, error) {
	report, err := p.cli.ProbeVersion(ctx)
	if err != nil {
		return report, err
	}
	if checkErr := checkVersion(report.Version, p.cli.minVersion); checkErr != nil {
		return report, checkErr
	}
	return report, nil
}

// Discover lists sessions, merging the CLI listing with the on-disk stores.
//
// Sessions known only on disk are included because that is exactly the state
// after a usage limit: the process is gone but the conversation must still be
// resumed. Sessions the CLI reports without a usable session id, and sessions
// of an unsupported kind, are returned as UNRECOVERABLE with a reason.
func (p *Provider) Discover(ctx context.Context, filter DiscoverFilter) ([]Session, error) {
	version, err := p.cli.Version(ctx)
	if err != nil {
		return nil, err
	}
	if err := checkVersion(version, p.cli.minVersion); err != nil {
		return nil, err
	}

	query := AgentsQuery{All: p.all, CWD: p.cwd}
	if filter.All != nil {
		query.All = *filter.All
	}
	if cwd := strings.TrimSpace(filter.UnderCWD); cwd != "" {
		query.CWD = cwd
	}

	entries, agentsErr := p.cli.Agents(ctx, query)
	if agentsErr != nil {
		// A CLI failure must not hide sessions that are resumable from disk, so
		// discovery degrades to the store and reports the failure.
		p.log.Warn("claude agents listing failed; falling back to on-disk transcripts", "error", agentsErr)
	}

	records, storeErr := p.store.List()
	if storeErr != nil {
		if agentsErr != nil {
			return nil, errors.Join(agentsErr, storeErr)
		}
		return nil, storeErr
	}

	now := p.clock.Now().UTC()
	sessions := p.merge(entries, records, now)

	out := make([]Session, 0, len(sessions))
	for _, session := range sessions {
		if !filter.matches(session) {
			continue
		}
		out = append(out, session)
	}
	sortSessions(out)

	// Preserve the first-seen time across passes, and remember the newest view
	// so ResumeSession can reuse it.
	p.mu.Lock()
	for i := range out {
		if prev, ok := p.known[out[i].ID]; ok {
			if !prev.DiscoveredAt.IsZero() {
				out[i].DiscoveredAt = prev.DiscoveredAt
			}
			if out[i].Name == "" {
				out[i].Name = prev.Name
			}
		}
		p.known[out[i].ID] = out[i]
	}
	p.mu.Unlock()

	return out, nil
}

// merge combines CLI entries and on-disk records into sessions.
func (p *Provider) merge(entries []AgentEntry, records []SessionRecord, now time.Time) []Session {
	sessions := make(map[string]Session)
	byID := func(id string) Session {
		if s, ok := sessions[id]; ok {
			return s
		}
		return Session{ID: id, DiscoveredAt: now}
	}

	for _, entry := range entries {
		id := entry.SessionID
		if id == "" || !ValidSessionID(id) {
			// Without a session id there is nothing to resume. Keep the entry
			// visible as UNRECOVERABLE so `ccresu sessions` explains why it is
			// ignored instead of silently dropping it.
			key := "unidentified:" + firstNonEmpty(entry.StateID, entry.CWD, entry.Kind, "unknown")
			session := Session{
				ID:           key,
				Type:         SessionTypeUnknown,
				Kind:         entry.Kind,
				ProjectPath:  entry.CWD,
				Source:       SourceAgents,
				PID:          entry.PID,
				Name:         entry.Name,
				Status:       entry.Status,
				State:        entry.State,
				WaitingFor:   entry.WaitingFor,
				StartedAt:    entry.StartedAt,
				LastActivity: firstNonZeroTime(entry.StartedAt),
				Recoverable:  false,
				Reason:       "claude agents --json reported no session id, so the conversation cannot be resumed",
				DiscoveredAt: now,
				LastSeenAt:   now,
			}
			sessions[key] = session
			continue
		}

		session := byID(id)
		session.ID = id
		session.Kind = entry.Kind
		session.Source = SourceAgents
		session.PID = entry.PID
		session.Name = entry.Name
		session.Status = entry.Status
		session.State = entry.State
		session.WaitingFor = entry.WaitingFor
		session.StartedAt = entry.StartedAt
		session.LastActivity = firstNonZeroTime(entry.StartedAt)
		if session.ProjectPath == "" {
			session.ProjectPath = entry.CWD
		}
		if session.DiscoveredAt.IsZero() {
			session.DiscoveredAt = now
		}
		session.LastSeenAt = now

		switch entry.Kind {
		case kindInteractive:
			session.Type = SessionTypeInteractive
			session.Recoverable = true
			session.Reason = ""
		case kindBackground:
			session.Type = SessionTypeBackground
			session.Recoverable = true
			session.Reason = ""
		default:
			session.Type = SessionTypeUnknown
			session.Recoverable = false
			session.Reason = fmt.Sprintf("unsupported session type %q reported by claude agents --json", entry.Kind)
		}
		sessions[id] = session
	}

	for _, record := range records {
		session := byID(record.ID)
		session.ID = record.ID
		if session.Source == "" {
			if record.Transcript.Path != "" {
				session.Source = SourceTranscript
			} else {
				session.Source = SourceSessionEnv
			}
		}
		if session.Type == "" {
			session.Type = SessionTypeTranscript
			if session.Kind == "" {
				session.Kind = string(SessionTypeTranscript)
			}
		}
		if session.ProjectPath == "" {
			session.ProjectPath = record.CWD()
		}
		if record.Transcript.Path != "" {
			session.TranscriptPath = record.Transcript.Path
			session.TranscriptSize = record.Transcript.Size
			session.TranscriptLines = record.Transcript.Lines
		}
		session.HasSessionEnv = record.EnvPath != ""
		if !record.Transcript.LastTimestamp.IsZero() &&
			(session.LastActivity.IsZero() || record.Transcript.LastTimestamp.After(session.LastActivity)) {
			session.LastActivity = record.Transcript.LastTimestamp
		}
		if session.StartedAt.IsZero() {
			session.StartedAt = record.Transcript.FirstTimestamp
		}
		if session.DiscoveredAt.IsZero() {
			session.DiscoveredAt = now
		}
		session.LastSeenAt = now
		// A record carries a real session id, so the verified resume command can
		// target it. Whether the transcript exists only affects verification.
		if session.Reason == "" {
			session.Recoverable = true
		}
		sessions[session.ID] = session
	}

	out := make([]Session, 0, len(sessions))
	for _, session := range sessions {
		if session.LastActivity.IsZero() {
			session.LastActivity = session.StartedAt
		}
		out = append(out, session)
	}
	return out
}

// GetSession returns one discovered session.
func (p *Provider) GetSession(ctx context.Context, sessionID string) (Session, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return Session{}, err
	}
	sessions, err := p.Discover(ctx, DiscoverFilter{IDs: []string{sessionID}})
	if err != nil {
		return Session{}, err
	}
	for _, session := range sessions {
		if session.ID == sessionID {
			return session, nil
		}
	}
	return Session{}, fmt.Errorf("%w: %s", ErrSessionNotFound, sessionID)
}

// GetSessionStatus refreshes one session and reports its live state.
func (p *Provider) GetSessionStatus(ctx context.Context, sessionID string) (SessionStatus, error) {
	session, err := p.GetSession(ctx, sessionID)
	if err != nil {
		return SessionStatus{}, err
	}
	status := SessionStatus{
		Session:    session,
		Status:     session.Status,
		State:      session.State,
		WaitingFor: session.WaitingFor,
		Running:    sessionRunning(session),
		CheckedAt:  p.clock.Now().UTC(),
	}
	if session.TranscriptPath != "" {
		if info, scanErr := p.store.ScanTranscript(session.TranscriptPath); scanErr == nil {
			status.Transcript = info
			if !info.LastTimestamp.IsZero() {
				status.Session.LastActivity = info.LastTimestamp
			}
			status.Session.TranscriptSize = info.Size
			status.Session.TranscriptLines = info.Lines
		} else if !errors.Is(scanErr, os.ErrNotExist) {
			p.log.Warn("transcript scan failed", "session", sessionID, "error", scanErr)
		}
	}
	return status, nil
}

// sessionRunning reports whether the CLI still sees an active process.
func sessionRunning(session Session) bool {
	switch strings.ToLower(session.State) {
	case "done", "failed", "stopped":
		return false
	case "working", "blocked":
		return true
	}
	switch strings.ToLower(session.Status) {
	case "busy", "waiting":
		return true
	}
	return session.PID > 0
}

// ResumeSession continues the original conversation using the single verified
// resume command and verifies the session id was preserved.
func (p *Provider) ResumeSession(ctx context.Context, sessionID, message string) (ResumeResult, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return ResumeResult{}, err
	}
	session, err := p.GetSession(ctx, sessionID)
	if err != nil {
		return ResumeResult{}, err
	}
	if session.Unrecoverable() {
		return ResumeResult{}, fmt.Errorf("%w: %s: %s", ErrUnrecoverable, sessionID, session.Reason)
	}
	result, err := p.cli.Resume(ctx, sessionID, message)
	if err != nil {
		return result, err
	}
	p.mu.Lock()
	updated := session
	updated.LastSeenAt = p.clock.Now().UTC()
	if result.Verified {
		updated.LastActivity = updated.LastSeenAt
	}
	p.known[sessionID] = updated
	p.mu.Unlock()
	return result, nil
}

// ContinuationMessage is the default prompt ccresu sends when resuming. The
// caller normally supplies config.Claude.ContinuationMessage instead; it exists
// so the provider is usable on its own.
const ContinuationMessage = "The usage limit has reset. Continue the paused goal from where you stopped."

var _ SessionProvider = (*Provider)(nil)
