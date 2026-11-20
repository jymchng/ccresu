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
	// TurnTimeout bounds the resume invocation, which runs the whole turn.
	TurnTimeout time.Duration
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
		TurnTimeout:    opts.TurnTimeout,
		MaxOutputBytes: opts.MaxOutputBytes,
		MinVersion:     opts.MinimumVersion,
		// The directory the store resolved, not the option as given: an empty
		// Home and an explicit one both end up as the same absolute directory, and
		// that one value is what every command runs with (R9). Resolving it twice
		// is how a read and a resume end up on different Claude Code instances.
		ConfigDir: store.Home(),
		Logger:    logger,
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

// CLIVersion reports the version of the executable ccresu invokes.
func (p *Provider) CLIVersion(ctx context.Context) (Version, error) { return p.cli.Version(ctx) }

// MinimumVersion reports the version floor this build supports (PRD 3 R2).
func (p *Provider) MinimumVersion() Version { return p.cli.minVersion }

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
			// PRD 6 R1: where a session started is a fact that must survive a
			// pass. The map is rebuilt on every pass, so without this carry-over
			// the observed `cwd` would become ProjectPath again and the launch
			// directory would be lost the first time a session moved.
			if prev.ProjectPath != "" {
				out[i].ProjectPath = prev.ProjectPath
			}
			// PRD 6 R9: a recorded run directory that disagrees with the newest
			// observation must not be used silently. The newer observation has
			// already won (merge takes `cwd` from it); what is left is to say so,
			// because a recovery must never resume into a tree the session does
			// not occupy.
			if prev.RunsInPath != "" && out[i].RunsInPath != "" && prev.RunsInPath != out[i].RunsInPath {
				p.log.Warn("session changed its working directory; using the newer observation",
					"session", out[i].ID, "was", prev.RunsInPath, "now", out[i].RunsInPath)
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
		// PRD 6 R1: the observed `cwd` is where the session *runs*, which is a
		// different fact from where it started. It is recorded in its own field
		// and refreshed on every pass (R4), so a session that moves into a
		// worktree mid-life is picked up without losing its launch directory.
		if cwd := strings.TrimSpace(entry.CWD); cwd != "" {
			session.RunsInPath = cwd
		}
		if session.ProjectPath == "" {
			// The launch directory, recorded once and never overwritten: it is
			// the fallback when a worktree disappears (R8) and the value the
			// display and PRD 5's messages quote.
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
// resume command.
//
// The resume runs in the directory the session occupies, resolved in PRD 6's
// fixed order: the recorded run directory, then the project directory (R3). A
// recorded directory that no longer exists falls back and says so once (R8); a
// session with neither is a named failure, never an inherited cwd (R2).
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
	workspace, err := ResolveWorkspace(session.RunsInPath, session.ProjectPath)
	if err != nil {
		// Not wrapped as ErrUnrecoverable: a directory can come back (a worktree
		// restored, a checkout re-cloned), so this is a failed attempt with a
		// name, not a verdict on the session.
		return ResumeResult{}, fmt.Errorf("resume session %s: %w", sessionID, err)
	}
	if workspace.FellBack {
		// R8: the fallback is announced rather than silent, because an operator
		// reading a recovery that ran in the checkout instead of the worktree
		// needs to know which of the two answered and why.
		p.log.Warn("the session's recorded run directory is gone; resuming in the project directory",
			"session", sessionID, "missing", workspace.Missing, "using", workspace.Dir)
	}
	return p.resumeIn(ctx, sessionID, session, message, workspace.Dir)
}

// ResumeSessionIn hands the session off in a directory the caller has already
// resolved.
//
// It exists so the recovery engine can use the directory it persisted rather
// than a second opinion computed here (PRD 6 R3): after a restart the stored row
// may be the only source for a session the CLI no longer lists, and a session
// that moved since the last pass is better served by the observation. The caller
// owns that choice; this method only carries it out.
func (p *Provider) ResumeSessionIn(ctx context.Context, sessionID, message, dir string) (ResumeResult, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return ResumeResult{}, err
	}
	session, err := p.GetSession(ctx, sessionID)
	if err != nil {
		// A session the CLI no longer lists is still resumable: the handoff is
		// addressed by id, and the caller has already resolved a directory. The
		// zero Session is enough for the bookkeeping below — but not for the
		// vector, which is why resumeIn takes the id as its own argument rather
		// than reading it off a session that may be the zero value.
		p.log.Debug("resuming a session discovery does not report", "session", sessionID, "error", err)
	}
	return p.resumeIn(ctx, sessionID, session, message, dir)
}

// resumeIn is the one place a handoff is issued, so the two entry points cannot
// drift into running in different directories.
//
// sessionID is passed separately from session because session may be the zero
// value (a session discovery does not report) while the handoff still has to
// name the conversation it continues. Reading the id off the session instead
// built a `--resume ""` vector for exactly that case.
func (p *Provider) resumeIn(ctx context.Context, sessionID string, session Session, message, dir string) (ResumeResult, error) {
	result, err := p.cli.Resume(ctx, sessionID, message, dir)
	if err != nil {
		return result, err
	}
	p.mu.Lock()
	updated := session
	updated.ID = sessionID
	updated.LastSeenAt = p.clock.Now().UTC()
	// The turn ran to its end, so the session was active: the exit status is the
	// only evidence there is, and a zero one is what it means.
	updated.LastActivity = updated.LastSeenAt
	p.known[sessionID] = updated
	p.mu.Unlock()
	return result, nil
}

// ContinuationMessage is the default prompt ccresu sends when resuming. The
// caller normally supplies config.Claude.ContinuationMessage instead; it exists
// so the provider is usable on its own.
const ContinuationMessage = "The usage limit has reset. Continue the paused goal from where you stopped."

var _ SessionProvider = (*Provider)(nil)
