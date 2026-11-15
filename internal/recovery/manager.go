package recovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/config"
	"github.com/jymchng/ccresu/internal/storage"
)

// DefaultMaxConcurrent bounds simultaneous recoveries.
const DefaultMaxConcurrent = 3

// DefaultContinuationMessage is sent when a paused goal needs an explicit nudge.
const DefaultContinuationMessage = "Continue working on the existing active goal from where execution was interrupted by the usage limit. Review the current state, avoid repeating completed work, and continue until the goal is completed."

// ErrLimitNotCleared reports that the usage limit is still in force, so the
// session went back to waiting instead of being resumed.
var ErrLimitNotCleared = errors.New("recovery: usage limit is still in force")

// Store is the persistence the manager needs. *storage.Store satisfies it.
type Store interface {
	GetSession(ctx context.Context, id string) (storage.Session, error)
	ListSessions(ctx context.Context, filter storage.SessionFilter) ([]storage.Session, error)
	TransitionSession(ctx context.Context, id string, to storage.State, reason string, attemptID int64) (storage.Transition, error)
	RecordTransition(ctx context.Context, transition storage.Transition) error
	SetSessionError(ctx context.Context, id, message string) error
	SetSessionRetryAt(ctx context.Context, id string, at time.Time) error
	SetSessionTranscriptMetrics(ctx context.Context, id, path string, size, lines int64) error
	StartAttempt(ctx context.Context, sessionID string, usageLimitID int64, command string) (storage.RecoveryAttempt, error)
	FinishAttempt(ctx context.Context, attemptID int64, result storage.AttemptResult) (storage.RecoveryAttempt, error)
	CountAttempts(ctx context.Context, sessionID string) (int, error)
	SessionSpend(ctx context.Context, sessionID string) (storage.SpendRecord, error)
	LatestAttempt(ctx context.Context, sessionID string) (storage.RecoveryAttempt, error)
	ListAttempts(ctx context.Context, sessionID string, limit int) ([]storage.RecoveryAttempt, error)
	ListTransitions(ctx context.Context, sessionID string, limit int) ([]storage.Transition, error)
}

// LimitRecheck reports whether a session's usage limit has actually cleared.
//
// It is injected so the recovery engine does not have to know how a limit is
// detected. A nil hook means "trust the schedule": the reset time was verified
// by the scheduler before this point.
type LimitRecheck func(ctx context.Context, sessionID string) (cleared bool, err error)

// Config assembles a Manager.
//
// Every field has a safe default, so a zero Config plus a provider and a store
// runs the documented policy: recovery enabled, ten attempts, 30s doubling to
// 15m with 15% jitter, three concurrent recoveries.
type Config struct {
	// Store persists sessions, attempts and transitions. Required.
	Store Store
	// Provider discovers and resumes sessions. Required.
	Provider claude.SessionProvider
	// Clock is the time source. Nil means the system clock.
	Clock clock.Clock
	// Logger receives recovery diagnostics. Nil means slog.Default().
	Logger *slog.Logger
	// Enabled switches automatic recovery on. The zero value is false, so a
	// caller must opt in — matching recovery.enabled in the configuration.
	Enabled bool
	// MaxAttempts is the attempt budget per session. Zero means the default.
	MaxAttempts int
	// Backoff is the retry schedule. Zero value means the documented defaults.
	Backoff Backoff
	// MaxConcurrent bounds simultaneous recoveries. Zero means the default.
	MaxConcurrent int
	// ContinuationMessage is the message sent with the resume command.
	ContinuationMessage string
	// VerificationTimeout and HealthCheckInterval configure the verifier.
	VerificationTimeout time.Duration
	HealthCheckInterval time.Duration
	// Verifier overrides the default verifier, for tests and for hosts that
	// measure transcript growth differently.
	Verifier *Verifier
	// LimitRecheck is an optional pre-resume limit check. Nil means the
	// scheduler already confirmed the reset.
	LimitRecheck LimitRecheck
	// LimitPark records a usage-limit refusal and plans the wait until the
	// reset. Nil means a refusal is still reported as a wait, but no schedule
	// and no usage-limit row are written, so the wait ends only when something
	// else re-plans it; the daemon always supplies one.
	LimitPark LimitPark
	// Observer receives every recovery milestone, so a caller can tell an
	// operator that an attempt started, succeeded, failed or stopped for good.
	// Nil means nothing is announced: the recovery runs identically either way,
	// which is why the notifications cannot stand in its path.
	Observer Observer
	// Limits are PRD 5's two spend caps. The zero value disables both, which is
	// today's behaviour exactly: no sum is taken and no check runs.
	Limits SpendLimits
	// Supervisor reads the background supervisor's status and version (PRD 3
	// R1, R2). Nil means no supervisor capability is installed: every pass
	// reports "no supervisor" and recovers exactly as before, which is the same
	// answer as a machine that has never run `claude --bg`.
	Supervisor SupervisorReader
}

// Manager runs recoveries for one process.
//
// It is safe for concurrent use. Duplicate recovery of the same session is
// prevented twice over: a per-session lock stops two goroutines in this process
// from racing, and the persisted attempt row plus the state machine stop a
// restart from resuming a session that already resumed.
type Manager struct {
	store    Store
	provider claude.SessionProvider
	clock    clock.Clock
	log      *slog.Logger

	enabled     bool
	maxAttempts int
	backoff     Backoff
	message     string
	verifier    Verifier
	limitCheck  LimitRecheck
	limitPark   LimitPark
	limits      SpendLimits
	observer    Observer
	// observations reads the supervisor's published state. It caches one read
	// per pass, so every session in a pass is answered from the same instant.
	observations *ObservationSource
	// supervisors reads `claude daemon status` once per pass (R1), so the
	// supervisor's state, version and worker count are one fact per pass rather
	// than one fact per session.
	supervisors *SupervisorSource

	locks *sessionLocks
	sem   chan struct{}
}

// New validates cfg and builds a Manager.
func New(cfg Config) (*Manager, error) {
	if cfg.Store == nil {
		return nil, ErrNoStore
	}
	if cfg.Provider == nil {
		return nil, ErrNoProvider
	}
	// A caller that supplies no policy at all gets the documented one. A policy
	// that was set is honoured as given, including an explicit Jitter of zero.
	policy := cfg.Backoff
	if policy.isZero() {
		policy = DefaultBackoff()
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	backoff := policy.withDefaults()

	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxAttempts
	}
	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxConcurrent
	}
	message := cfg.ContinuationMessage
	if strings.TrimSpace(message) == "" {
		message = DefaultContinuationMessage
	}
	clk := cfg.Clock
	if clk == nil {
		clk = clock.System()
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	}

	verifier := Verifier{
		Provider: cfg.Provider,
		Clock:    clk,
		Timeout:  cfg.VerificationTimeout,
		Interval: cfg.HealthCheckInterval,
	}
	if cfg.Verifier != nil {
		verifier = *cfg.Verifier
		if verifier.Provider == nil {
			verifier.Provider = cfg.Provider
		}
		if verifier.Clock == nil {
			verifier.Clock = clk
		}
	}

	return &Manager{
		store:        cfg.Store,
		provider:     cfg.Provider,
		clock:        clk,
		log:          logger,
		enabled:      cfg.Enabled,
		maxAttempts:  maxAttempts,
		backoff:      backoff,
		message:      message,
		verifier:     verifier,
		limits:       cfg.Limits,
		limitCheck:   cfg.LimitRecheck,
		limitPark:    cfg.LimitPark,
		observer:     cfg.Observer,
		observations: NewObservationSource(cfg.Provider, clk),
		supervisors:  NewSupervisorSource(cfg.Supervisor),
		locks:        newSessionLocks(),
		sem:          make(chan struct{}, maxConcurrent),
	}, nil
}

// announce reports a milestone to the installed observer, if any.
//
// It is called on the recovery goroutine and returns immediately when no
// observer is installed, so a deployment without notifications pays nothing for
// the hook. Nothing here can fail the recovery: the observer returns no error
// and its own failures are its business (see Observer).
func (m *Manager) announce(ctx context.Context, kind MilestoneKind, outcome Outcome, cause error) {
	if m.observer == nil {
		return
	}
	m.observer(ctx, Milestone{
		Kind:        kind,
		Outcome:     outcome,
		MaxAttempts: m.maxAttempts,
		Cause:       cause,
	})
}

// announceObservation reports a milestone together with the published state it
// was raised from, so the message can quote the CLI instead of paraphrasing it.
//
// It exists next to announce because a needs-input milestone is only honest if
// it carries the observation: "blocked on a permission prompt" and "blocked, no
// reason reported" are different messages, and only the observation can tell
// them apart. Like announce it does nothing when no observer is installed.
func (m *Manager) announceObservation(ctx context.Context, kind MilestoneKind, outcome Outcome, observation StateObservation) {
	if m.observer == nil {
		return
	}
	m.observer(ctx, Milestone{
		Kind:        kind,
		Outcome:     outcome,
		MaxAttempts: m.maxAttempts,
		Observation: observation,
	})
}

// attemptObservation picks the observation an attempt row should carry: the one
// taken after the resume when there is one, and the pass's pre-resume read
// otherwise (PRD 2 / R13).
//
// Preferring the later read is the point — it is the observation the verdict
// rests on. Falling back rather than leaving it empty is the other half: a
// handoff is proven by the CLI's block, and the row still owes an operator what
// the supervisor had said about the session before it was handed over.
func attemptObservation(before, after StateObservation) StateObservation {
	if after.Recorded() {
		return after
	}
	return before
}

// recordTranscriptMetrics stores the transcript measurement a status check
// observed, so the next pass compares against what is actually on disk.
//
// It is a diagnostic and nothing else (R3): no decision is read back out of it,
// and a failure to record is logged rather than returned. A session that was
// observed, parked or recognised as already resumed still owes the operator its
// state even if the measurement could not be stored.
func (m *Manager) recordTranscriptMetrics(ctx context.Context, sessionID string, status claude.SessionStatus) {
	if strings.TrimSpace(status.Transcript.Path) == "" {
		return
	}
	if err := m.store.SetSessionTranscriptMetrics(ctx, sessionID, status.Transcript.Path,
		status.Transcript.Size, status.Transcript.Lines); err != nil {
		m.log.Warn("record transcript metrics", "session", sessionID, "error", err)
	}
}

// FromConfig builds a Manager from the application configuration.
func FromConfig(cfg *config.Config, store Store, provider claude.SessionProvider, clk clock.Clock) (*Manager, error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil config", ErrInvalidConfig)
	}
	rec := cfg.Recovery
	return New(Config{
		Store:               store,
		Provider:            provider,
		Clock:               clk,
		Enabled:             rec.Enabled,
		MaxAttempts:         rec.MaxAttempts,
		Backoff:             FromConfigValues(rec.InitialDelay.Duration, rec.MaxDelay.Duration, rec.Multiplier, rec.Jitter),
		MaxConcurrent:       rec.MaxConcurrentRecoveries,
		ContinuationMessage: cfg.Claude.ContinuationMessage,
		VerificationTimeout: rec.VerificationTimeout.Duration,
		HealthCheckInterval: rec.HealthCheckInterval.Duration,
		Limits: SpendLimits{
			PerSession: rec.Guardrails.MaxCostUSDPerSession,
			PerAttempt: rec.Guardrails.MaxCostUSDPerAttempt,
		},
	})
}

// Enabled reports whether automatic recovery is switched on.
func (m *Manager) Enabled() bool { return m.enabled }

// MaxAttempts reports the attempt budget.
func (m *Manager) MaxAttempts() int { return m.maxAttempts }

// Backoff exposes the retry policy, e.g. for `ccresu doctor`.
func (m *Manager) Backoff() Backoff { return m.backoff }

// ContinuationMessage reports the message a resume sends.
func (m *Manager) ContinuationMessage() string { return m.message }

// Request describes one recovery.
type Request struct {
	// SessionID is the session to recover. Required.
	SessionID string
	// UsageLimitID links the attempt to the detected limit, when known.
	UsageLimitID int64
	// Message overrides the configured continuation message.
	Message string
	// Force allows a session whose attempt budget is spent to be tried once
	// more, which is what `ccresu retry` does. It never bypasses the state
	// machine, the per-session lock or the disabled/recoverable checks.
	Force bool
	// Manual marks the request as a human act — `ccresu recover`, `ccresu retry`,
	// or the TUI's recover key — rather than a reconcile pass. PRD 5 R11 exempts
	// a human act from the spend caps: the cap exists to replace an operator who
	// is not there, and it must not bind one who is. The override is still
	// recorded: the attempt's cost is written and the refusal it walked past is
	// logged.
	Manual bool
}

// Outcome is the result of one recovery.
type Outcome struct {
	SessionID    string
	State        State
	Attempt      storage.RecoveryAttempt
	Verification Verification
	// Delay is how long the caller should wait before the next attempt. It is
	// set only when State is RETRY_WAIT.
	Delay time.Duration
	// Wait is how long the session must wait before ccresu looks again. It is
	// set only when State is WAITING_FOR_RESET because a resume was refused by a
	// usage limit: the refusal names when the work may continue, so there is
	// nothing to retry in the meantime.
	Wait time.Duration
	// WaitUntil is the instant Wait ends: the reset the refusal named, or the
	// first poll when it named none. Zero when no wait was planned.
	WaitUntil time.Time
	// Resumed reports that the original conversation was proven to continue,
	// either because ccresu resumed it or because it had already resumed.
	Resumed bool
	// Interrupted reports that the pass ended because an attempt left running by
	// a process that died was closed as interrupted (PRD 3 R6). It is not a
	// failure: nothing was handed off, no budget was spent, and the session
	// returns to DISCOVERED for the next pass to re-observe.
	Interrupted bool
	// Supervisor is what `claude daemon status` reported on this pass (PRD 3
	// R1). It is carried rather than re-read, so a caller that wants to record or
	// show the reading cannot end up describing a different supervisor than the
	// pass acted on.
	Supervisor claude.SupervisorStatus
}

// Recover attempts one recovery of a session.
func (m *Manager) Recover(ctx context.Context, sessionID string) (Outcome, error) {
	return m.RecoverRequest(ctx, Request{SessionID: sessionID})
}

// Retry attempts a recovery of a session whose budget is spent.
//
// It is a manual request: `ccresu retry` and the TUI's retry key are a human
// asking for one more attempt, so the spend caps do not bind them (PRD 5 R11).
func (m *Manager) Retry(ctx context.Context, sessionID string) (Outcome, error) {
	return m.RecoverRequest(ctx, Request{SessionID: sessionID, Force: true, Manual: true})
}

// RecoverRequest attempts one recovery of a session.
//
// The whole sequence is persisted: every state change appends a transition row
// and every attempt is a row of its own, so `ccresu history` can replay exactly
// what happened even across restarts.
func (m *Manager) RecoverRequest(ctx context.Context, req Request) (Outcome, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return Outcome{}, ErrNoSession
	}
	if ctx == nil {
		return Outcome{}, errors.New("recovery: nil context")
	}
	if !m.enabled {
		return Outcome{SessionID: sessionID}, sessionError(ErrDisabled, sessionID)
	}
	if err := claude.ValidateSessionID(sessionID); err != nil {
		return Outcome{SessionID: sessionID}, err
	}

	// Duplicate prevention, part one: only one goroutine may recover a session.
	if !m.locks.tryLock(sessionID) {
		return Outcome{SessionID: sessionID}, sessionError(ErrAlreadyRunning, sessionID)
	}
	defer m.locks.unlock(sessionID)

	// Duplicate prevention, part two: the concurrency budget.
	select {
	case m.sem <- struct{}{}:
		defer func() { <-m.sem }()
	default:
		return Outcome{SessionID: sessionID}, sessionError(ErrConcurrencyLimit, sessionID)
	}

	return m.run(ctx, req, sessionID)
}

// run is the state-machine walk, holding the per-session lock and a concurrency
// slot.
func (m *Manager) run(ctx context.Context, req Request, sessionID string) (Outcome, error) {
	state := &stateWalker{ctx: ctx, store: m.store, sessionID: sessionID}

	session, err := m.store.GetSession(ctx, sessionID)
	if err != nil {
		return Outcome{SessionID: sessionID}, fmt.Errorf("%w: %v", ErrNotEligible, err)
	}
	state.current = session.State

	// A disabled session is a decision, not a failure: record it and stop.
	if !session.Enabled {
		if err := state.move(StateDisabled, "recovery skipped: session is disabled", 0); err != nil {
			return Outcome{SessionID: sessionID, State: state.current}, err
		}
		return Outcome{SessionID: sessionID, State: state.current}, sessionError(ErrDisabled, sessionID)
	}
	if !session.Recoverable {
		reason := session.Reason
		if strings.TrimSpace(reason) == "" {
			reason = "ccresu has no verified way to resume this session"
		}
		if err := state.move(StateUnrecoverable, reason, 0); err != nil {
			return Outcome{SessionID: sessionID, State: state.current}, err
		}
		return Outcome{SessionID: sessionID, State: state.current}, sessionError(ErrUnrecoverable, sessionID)
	}
	if Settled(state.current) && state.current != StateResumed {
		return Outcome{SessionID: sessionID, State: state.current},
			stateError(sessionID, state.current, StateCheckingSession)
	}
	if state.current == StateResumed {
		return Outcome{SessionID: sessionID, State: state.current, Resumed: true},
			stateError(sessionID, state.current, StateCheckingSession)
	}
	attempts, err := m.attemptsThisCycle(ctx, sessionID)
	if err != nil {
		return Outcome{SessionID: sessionID, State: state.current}, err
	}
	if attempts >= m.maxAttempts && !req.Force {
		// The budget for this cycle is spent. The session goes back to
		// DISCOVERED rather than to a terminal failure state: the daemon
		// rediscovers it, re-reads the transcript and schedules the next reset,
		// which starts a fresh budget. An operator never has to run
		// `ccresu reset` to make that happen.
		reason := fmt.Sprintf("maximum attempts reached (%d); returned to discovery for the next reset", m.maxAttempts)
		if err := state.move(StateDiscovered, reason, 0); err != nil {
			return Outcome{SessionID: sessionID, State: state.current}, err
		}
		_ = m.store.SetSessionError(ctx, sessionID, reason)
		return Outcome{SessionID: sessionID, State: state.current},
			&cycleEndError{SessionID: sessionID, State: StateDiscovered,
				Cause: sessionError(ErrMaxAttempts, sessionID)}
	}

	// An attempt row with no finish time belongs to a process that died — a
	// restart or a reboot. It is closed as *interrupted*, and the pass ends
	// there: the supervisor may still be running the work, so handing off again
	// in the same pass would duplicate the recovery the supervisor is already
	// doing (PRD 3 R6).
	if closed, err := m.closeInterruptedAttempt(ctx, state, sessionID); err != nil {
		return Outcome{SessionID: sessionID, State: state.current}, err
	} else if closed {
		return Outcome{SessionID: sessionID, State: StateDiscovered, Interrupted: true},
			&cycleEndError{
				SessionID: sessionID,
				State:     StateDiscovered,
				Cause:     fmt.Errorf("%w; re-observing it before handing off again", ErrAttemptInterrupted),
			}
	}

	outcome := Outcome{SessionID: sessionID}
	baseline := BaselineFromSession(session)

	if err := state.move(StateCheckingSession, "checking the session before resuming", 0); err != nil {
		return Outcome{SessionID: sessionID, State: state.current}, err
	}

	// PRD 3 R1/R2: the supervisor is read once per pass, and its version is
	// compared with the CLI ccresu would hand off with. Three answers are
	// possible and each is its own outcome: no supervisor (normal, carry on), an
	// unreadable status (named failure, hand off nothing), and a version mismatch
	// (named condition with the documented remedy, hand off nothing). The last
	// two are reported through fail(), which records the cause on the session and
	// spends no attempt budget, because no continuation was issued.
	//
	// The read is bookkeeping, not a decision: what the pass decides about the
	// session still comes from the published listing. Recording the reading here
	// is what lets an operator see which supervisor the pass was talking to when
	// it made that decision.
	supervisor, supervisorErr := m.supervisors.Check(ctx)
	outcome.Supervisor = supervisor
	m.log.Info("read the background supervisor",
		"session", sessionID, "supervisor", SupervisorSummary(supervisor), "argv", supervisor.Argv)
	if supervisorErr != nil {
		return m.fail(ctx, state, Outcome{SessionID: sessionID, Supervisor: supervisor}, baseline,
			storage.RecoveryAttempt{}, Verification{}, supervisorErr)
	}

	status, err := m.provider.GetSessionStatus(ctx, sessionID)
	if err != nil {
		return m.fail(ctx, state, Outcome{SessionID: sessionID}, baseline, storage.RecoveryAttempt{},
			Verification{}, fmt.Errorf("check session %s: %w", sessionID, err))
	}
	if !status.Session.Recoverable && status.Session.ID != "" {
		reason := status.Session.Reason
		if strings.TrimSpace(reason) == "" {
			reason = "the session is no longer recoverable"
		}
		cause := fmt.Errorf("%w: %s", ErrUnrecoverable, reason)
		return m.fail(ctx, state, Outcome{SessionID: sessionID}, baseline, storage.RecoveryAttempt{},
			Verification{}, cause)
	}
	merged := mergeBaseline(baseline, BaselineFromStatus(status))

	// What the supervisor published decides what this session is. One read of
	// `claude agents --json --all` serves the whole pass (R1), and the mapping
	// takes "is a handoff outstanding" as an argument (R5) rather than inferring
	// it — here nothing has been handed off yet, so it is false.
	//
	// A read that failed is an outage, not a verdict (R8): no observation means
	// no decision, and the attempt stays open for the next pass.
	observation, obsErr := m.observations.Observe(ctx, sessionID)
	if obsErr != nil {
		return m.fail(ctx, state, Outcome{SessionID: sessionID}, baseline,
			storage.RecoveryAttempt{}, Verification{}, obsErr)
	}
	// stopIssuedByCCRESU is false because nothing in this path has issued a
	// stop; a `stopped` observation therefore keeps its documented retry
	// meaning instead of becoming the operator's decision.
	decision := Observe(observation, false, false)
	observed := Verification{
		LinesBefore: baseline.TranscriptLines,
		LinesAfter:  status.Transcript.Lines,
		SizeBefore:  baseline.TranscriptSize,
		SizeAfter:   status.Transcript.Size,
		Running:     status.Running,
		// Nothing in this path resumed anything, so the only identity fact to
		// check is that the status describes the session that was asked about.
		// An empty id is the CLI declining to name it, which is not a mismatch.
		SessionIDPreserved: status.Session.ID == "" || status.Session.ID == sessionID,
	}
	observed.ObservePublished(observation, decision)

	switch {
	case decision.NeedsInput:
		// A session waiting on a human is never handed a continuation: it would
		// queue behind a prompt nobody is there to answer (R4, R11). It parks
		// instead, and the operator is told once, with whatever reason the CLI
		// published — including none.
		m.recordTranscriptMetrics(ctx, sessionID, status)
		if err := state.move(StateWaitingForInput, decision.Reason, 0); err != nil {
			return Outcome{SessionID: sessionID, State: state.current}, err
		}
		outcome.State = state.current
		outcome.Verification = observed
		m.announceObservation(ctx, MilestoneNeedsInput, outcome, observation)
		return outcome, nil
	case decision.State == StateResumed:
		// It continued on its own once the window reset: a success, and one that
		// must be recognised without issuing a resume, because resuming a session
		// that is already working would duplicate its work.
		m.recordTranscriptMetrics(ctx, sessionID, status)
		if err := state.move(StateResumed, decision.Reason, 0); err != nil {
			return Outcome{SessionID: sessionID, State: state.current}, err
		}
		outcome.State = state.current
		outcome.Verification = observed
		outcome.Resumed = true
		// The supervisor says the conversation is alive, so this is a success
		// even though ccresu issued no resume: announcing it here is what keeps
		// "recovery succeeded" from meaning "ccresu ran a command".
		m.announce(ctx, MilestoneSucceeded, outcome, nil)
		return outcome, nil
	case decision.State == StateCompleted:
		// `done` with no handoff outstanding: the last turn finished what was
		// asked for, so there is nothing to continue and no continuation is sent
		// (R6). Handing a finished conversation a "continue" message is the
		// mistake this branch exists to prevent.
		m.recordTranscriptMetrics(ctx, sessionID, status)
		if err := state.move(StateCompleted, decision.Reason, 0); err != nil {
			return Outcome{SessionID: sessionID, State: state.current}, err
		}
		outcome.State = state.current
		outcome.Verification = observed
		return outcome, nil
	}
	// Every other observation — failed, stopped, an unknown value, or a session
	// the supervisor has not heard of — leaves the resume path as it was: the
	// limit has reset, and continuing the conversation is what recovery does.
	baseline = merged

	if err := state.move(StateCheckingLimit, "checking the usage limit has cleared", 0); err != nil {
		return Outcome{SessionID: sessionID, State: state.current}, err
	}
	if m.limitCheck != nil {
		cleared, err := m.limitCheck(ctx, sessionID)
		if err != nil {
			return m.fail(ctx, state, Outcome{SessionID: sessionID}, baseline, storage.RecoveryAttempt{},
				Verification{}, fmt.Errorf("check usage limit: %w", err))
		}
		if !cleared {
			if moveErr := state.move(StateWaitingForReset, "the usage limit is still in force", 0); moveErr != nil {
				return Outcome{SessionID: sessionID, State: state.current}, moveErr
			}
			return Outcome{SessionID: sessionID, State: state.current, Resumed: false},
				sessionError(ErrLimitNotCleared, sessionID)
		}
	}

	// R5: the session spend check runs here, before an attempt row exists, so a
	// refusal spends nothing — the same rule the attempt budget follows, where a
	// request that spends no attempt has started nothing. It is a pre-resume
	// check like the limit check above, so it ends the pass through fail() with
	// no attempt row behind it.
	if err := m.checkSessionCap(ctx, sessionID, req.Manual); err != nil {
		return m.fail(ctx, state, Outcome{SessionID: sessionID}, baseline,
			storage.RecoveryAttempt{}, Verification{}, err)
	}

	message := req.Message
	if strings.TrimSpace(message) == "" {
		message = m.message
	}
	if err := state.move(StateResuming, "resuming the original session", 0); err != nil {
		return Outcome{SessionID: sessionID, State: state.current}, err
	}
	attempt, err := m.store.StartAttempt(ctx, sessionID, req.UsageLimitID, commandPreview(sessionID, message))
	if err != nil {
		return Outcome{SessionID: sessionID, State: state.current}, fmt.Errorf("record attempt: %w", err)
	}
	outcome.Attempt = attempt
	// The attempt row exists, so a resume is genuinely under way. Announcing it
	// here rather than at the caller is what makes "started" true: the daemon
	// only sees outcomes, and a CLI `ccresu recover` never goes through the
	// daemon at all.
	m.announce(ctx, MilestoneStarted, outcome, nil)

	result, resumeErr := m.resume(ctx, session, status.Session, sessionID, message)
	// R4: what this attempt cost, as soon as the CLI has said. A nil cost is
	// *unknown*, not zero, and an unknown attempt neither counts toward a cap nor
	// blocks a resume (R10). Carrying it on the attempt value is what lets both
	// the success path and fail() record the same figure.
	attempt.CostUSD = knownCost(result.TotalCostUSD)
	if resumeErr != nil {
		return m.fail(ctx, state, outcome, baseline, attempt, Verification{},
			fmt.Errorf("resume session %s: %w", sessionID, resumeErr))
	}
	if result.IsError {
		return m.fail(ctx, state, outcome, baseline, attempt, Verification{},
			fmt.Errorf("resume session %s: %w: %s", sessionID, claude.ErrResumeFailed, firstLine(result.Result)))
	}

	// R6: one attempt over the per-attempt cap stops recovery. The check runs
	// after the cost is known and before the outcome is decided, and fail()
	// records the cost it judged rather than discarding it.
	if capErr := m.checkAttemptCap(attempt, attempt.CostUSD); capErr != nil {
		return m.fail(ctx, state, outcome, baseline, attempt, Verification{}, capErr)
	}

	verification, verifyErr := m.verifier.Verify(ctx, baseline, result)
	if verifyErr != nil {
		return m.fail(ctx, state, outcome, baseline, attempt, verification,
			fmt.Errorf("verify session %s: %w", sessionID, verifyErr))
	}

	// PRD 2 / R3: a resume that was not handed off is proven by what the
	// supervisor publishes about the conversation, and growth of the transcript
	// is a diagnostic that is reported but never believed. The pass read the
	// listing before the resume, so that read is ended here — an observation
	// taken before the continuation cannot attest to it.
	var published StateObservation
	if !verification.HandoffAccepted {
		m.observations.Reset()
		obs, obsErr := m.observations.Observe(ctx, sessionID)
		if obsErr != nil {
			return m.fail(ctx, state, outcome, baseline, attempt, verification,
				fmt.Errorf("observe session %s after resume: %w", sessionID, obsErr))
		}
		published = obs
		verification.ObservePublished(obs, Observe(obs, true, false))
	} else {
		// PRD 3 R7: acceptance is when the supervisor takes the session, not when
		// the work is safe. It idle-stops sessions after about an hour, restarts
		// itself across its own updates, and reports `stopped` for a session that
		// ended while it was off — none of which is a verdict about the
		// conversation, and all of which look identical from here unless the
		// listing is read again.
		//
		// So the pass asks once more, after the handoff. A session the supervisor
		// no longer lists (having listed it before) or lists as `stopped`,
		// `failed` or an unknown value is not proven resumed: the session is
		// parked in RETRY_WAIT with a scheduled retry that re-observes before
		// handing off again, and nothing about it is reported as a permanent
		// failure or "cured" with a respawn.
		m.observations.Reset()
		after, obsErr := m.observations.Observe(ctx, sessionID)
		if obsErr != nil {
			return m.fail(ctx, state, outcome, baseline, attempt, verification,
				fmt.Errorf("observe session %s after the handoff: %w", sessionID, obsErr))
		}
		if reason, lost := handoffLost(after, observation); lost {
			return m.retryHandoff(ctx, state, outcome, attempt, verification, after, reason)
		}
	}
	if !verification.Proven {
		return m.fail(ctx, state, outcome, baseline, attempt, verification,
			VerificationFailedError(sessionID, verification))
	}

	finished, err := m.store.FinishAttempt(ctx, attempt.ID, storage.AttemptResult{
		State:             StateResumed,
		Success:           boolPtr(true),
		Output:            truncate(result.Result),
		SessionIDVerified: boolPtr(verification.SessionIDPreserved),
		TranscriptGrew:    boolPtr(verification.TranscriptGrew),
		HandoffStateID:    verification.HandoffStateID,
		HandoffName:       verification.HandoffName,
		HandoffAt:         handoffTime(verification, m.now()),
		HandoffCopied:     handoffCopied(verification),
		CostUSD:           attempt.CostUSD,
		// R13: the state the supervisor published is stored verbatim, so a
		// decision can be re-examined later instead of being re-argued from a
		// transcript measurement. A handoff has no post-resume observation, so
		// the pass's pre-resume read is what the row carries.
		Observation: RecordedObservation(attemptObservation(observation, published)),
		FinishedAt:  m.now(),
	})
	if err != nil {
		return Outcome{SessionID: sessionID, State: state.current}, fmt.Errorf("finish attempt: %w", err)
	}
	outcome.Attempt = finished
	outcome.Verification = verification

	if verification.TranscriptGrew {
		if err := m.store.SetSessionTranscriptMetrics(ctx, sessionID, baseline.TranscriptPath,
			verification.SizeAfter, verification.LinesAfter); err != nil {
			m.log.Warn("record transcript metrics", "session", sessionID, "error", err)
		}
	}
	if err := m.store.SetSessionError(ctx, sessionID, ""); err != nil {
		m.log.Warn("clear session error", "session", sessionID, "error", err)
	}
	// The reason names what actually happened, because it is what an operator
	// reads in `ccresu history`. A handoff did not grow a transcript under
	// ccresu's process, and saying it did would teach the wrong mental model.
	reason := "the original session resumed and the conversation grew"
	if verification.HandoffAccepted {
		reason = fmt.Sprintf("the supervisor accepted the continuation as %s (%s)",
			verification.HandoffStateID, verification.HandoffName)
	}
	if err := state.move(StateResumed, reason, attempt.ID); err != nil {
		return Outcome{SessionID: sessionID, State: state.current}, err
	}
	outcome.State = state.current
	outcome.Resumed = true
	m.announce(ctx, MilestoneSucceeded, outcome, nil)
	return outcome, nil
}

// fail records a failed attempt and moves the session along the retry path.
//
// A retryable failure below the attempt budget goes to RETRY_WAIT with the
// computed delay. Everything else is rediscovered: the session returns to
// DISCOVERED, where the next cycle re-reads its transcript with a fresh budget.
// Only a proved-unrecoverable session stops there, because that verdict is a
// fact about the conversation rather than a count of attempts.
//
// A failure reported before the resume was issued — the pre-resume status check
// or the limit check — has no attempt row behind it, so it spends no budget and
// always rests in RETRY_WAIT (unless ccresu has proved the session
// unrecoverable). That is a decision about the truth, not about which edges the
// machine happens to define: rediscovery is where a spent budget leads, and this
// failure spent none of it.
func (m *Manager) fail(ctx context.Context, state *stateWalker, outcome Outcome, baseline Baseline,
	attempt storage.RecoveryAttempt, verification Verification, cause error) (Outcome, error) {
	class := Classify(cause)
	outcome.Verification = verification

	// A resume that failed may still have written to the transcript: the CLI
	// appends its refusal, its error or a partial turn before exiting. Those
	// lines are the attempt's own work, not evidence that the conversation
	// continued by itself, so the stored baseline is advanced past them before
	// anything is decided. Otherwise the next status check sees growth it
	// caused itself and takes the "the session had already resumed after the
	// limit reset" shortcut, which reports a refused resume as a success.
	if attempt.ID != 0 {
		m.absorbAttemptWrites(ctx, outcome.SessionID)
	}

	// A refusal is not a failed attempt. The CLI ran, said the usage window has
	// not reset, and named when it will: the whole answer is to park the session
	// until then. It is handled before the retry policy is consulted so no
	// backoff can turn a refusal into another resume.
	if class == ClassLimitInForce {
		return m.park(ctx, state, outcome, attempt, cause)
	}

	// A zero attempt id means no resume was issued: the failure came from a
	// pre-resume check, and the budget it did not spend is read back from the
	// store instead of copied from a row that does not exist.
	preResume := attempt.ID == 0
	attemptNumber := attempt.AttemptNumber
	if preResume {
		if count, err := m.attemptsThisCycle(ctx, outcome.SessionID); err == nil {
			attemptNumber = count
		}
	}

	target := targetFor(class, attemptNumber, m.maxAttempts)
	if preResume {
		target = preResumeTarget(target)
	}

	if !preResume {
		if _, err := m.store.FinishAttempt(ctx, attempt.ID, storage.AttemptResult{
			State:   target,
			Success: boolPtr(false),
			Error:   truncate(cause.Error()),
			// The cost is carried on the attempt the caller handed over: it is known
			// once the resume returns, whether that resume succeeded, failed or
			// overspent, and it is recorded even when it is the reason for stopping.
			CostUSD:           attempt.CostUSD,
			SessionIDVerified: boolPtr(verification.SessionIDPreserved),
			TranscriptGrew:    boolPtr(verification.TranscriptGrew),
			// R13: a failed attempt records what was seen as well, because that
			// is the fact an operator needs when asking why ccresu gave up.
			Observation: RecordedObservation(verification.Observation),
			FinishedAt:  m.now(),
		}); err != nil {
			m.log.Warn("record failed attempt", "session", outcome.SessionID, "error", err)
		}
	}

	// RETRY_WAIT is the one destination with a schedule to hand back, so the
	// persisted state and the wait the caller performs can never disagree. It is
	// also the only answer the machine gives for a failed pre-resume check, so a
	// clamped failure and a cancellation report the delay they will observe.
	retry := class.Retryable() && attemptNumber < m.maxAttempts
	if retry || target == StateRetryWait {
		delay := m.backoff.Delay(attemptNumber)
		reason := fmt.Sprintf("attempt %d/%d failed (%s); next attempt in %s: %v",
			attemptNumber, m.maxAttempts, class, delay, cause)
		if preResume {
			reason = fmt.Sprintf("the pre-resume check failed (%s); no attempt was spent, next check in %s: %v",
				class, delay, cause)
		}
		if err := state.move(StateRetryWait, truncate(reason), attempt.ID); err != nil {
			return Outcome{SessionID: outcome.SessionID, State: state.current}, err
		}
		// Persist the instant the wait ends, so `ccresu sessions` and
		// `ccresu inspect` can report when the next attempt is due instead of
		// parsing it back out of the human-readable reason above.
		if err := m.store.SetSessionRetryAt(ctx, outcome.SessionID, m.now().Add(delay)); err != nil {
			m.log.Warn("record next attempt", "session", outcome.SessionID, "error", err)
		}
		_ = m.store.SetSessionError(ctx, outcome.SessionID, truncate(cause.Error()))
		outcome.State = state.current
		outcome.Delay = delay
		// The session is in RETRY_WAIT and ccresu will try again by itself, so
		// this is the "attempt failed, still working on it" message. A refusal
		// never reaches here: park() handles ClassLimitInForce above.
		m.announce(ctx, MilestoneFailed, outcome, cause)
		return outcome, &RetryError{
			SessionID: outcome.SessionID, Attempt: attemptNumber, Max: m.maxAttempts,
			Delay: delay, Cause: cause,
		}
	}

	if err := state.move(target, truncate(cause.Error()), attempt.ID); err != nil {
		return Outcome{SessionID: outcome.SessionID, State: state.current}, err
	}
	_ = m.store.SetSessionError(ctx, outcome.SessionID, truncate(cause.Error()))
	outcome.State = state.current
	// Only a positively permanent failure is announced as "automatic recovery
	// stopped". A spent attempt budget also lands in DISCOVERED, but that is a
	// pause in a cycle — the next discovery re-reads the transcript and starts a
	// fresh budget — so calling it a verdict would raise a false alarm every
	// night. An unrecoverable session is not announced here either: it is a fact
	// about the conversation rather than a failed attempt, and the operator reads
	// it from the state.
	// R9: the spend stop uses the existing permanent-failure path, whose subject
	// is already "Automatic recovery stopped", rather than adding a milestone or a
	// notification event.
	if class == ClassPermanent || class == ClassSpendCapReached {
		m.announce(ctx, MilestonePermanent, outcome, cause)
	}
	return outcome, &cycleEndError{SessionID: outcome.SessionID, State: target, Cause: cause}
}

// park waits out a usage-limit refusal instead of retrying it.
//
// The refusal is the most informative failure ccresu can get: the CLI ran, the
// conversation is intact, and the banner usually names the instant the work may
// continue. So the session is parked in WAITING_FOR_RESET, the banner is what it
// records, and the wait is planned by the injected parker — the detector
// interprets the banner, the scheduler persists the wait, and the usage_limits
// row records what the CLI said. No attempt budget is consulted: what ends this
// wait is the reset, not a count of tries.
//
// A parker that cannot plan does not turn the refusal back into a retry. The
// session still waits, with the banner as its last error, because resuming it
// again would ask the same question a second time; the failure is logged, and
// the next discovery of the session re-plans it.
func (m *Manager) park(ctx context.Context, state *stateWalker, outcome Outcome,
	attempt storage.RecoveryAttempt, cause error) (Outcome, error) {
	banner := refusalBanner(cause)

	// The attempt the CLI answered is finished before the wait is planned: it ran,
	// it refused, and saying so is the truth about it. Leaving the row open would
	// make the next Recover close it as "interrupted by a process restart" and
	// record a RETRY_WAIT transition over a session that is parked, which is a
	// false account of an attempt that has already been answered.
	if attempt.ID != 0 {
		verification := outcome.Verification
		if _, err := m.store.FinishAttempt(ctx, attempt.ID, storage.AttemptResult{
			State:             StateWaitingForReset,
			Success:           boolPtr(false),
			Error:             truncate(banner),
			SessionIDVerified: boolPtr(verification.SessionIDPreserved),
			TranscriptGrew:    boolPtr(verification.TranscriptGrew),
			FinishedAt:        m.now(),
		}); err != nil {
			m.log.Warn("record the refused attempt", "session", outcome.SessionID, "error", err)
		}
	}

	var wait Wait
	if m.limitPark == nil {
		m.log.Warn("a resume was refused by a usage limit but no parker is configured; waiting without a schedule",
			"session", outcome.SessionID)
	} else if planned, err := m.limitPark(ctx, outcome.SessionID, banner); err != nil {
		m.log.Warn("plan the wait after a usage-limit refusal",
			"session", outcome.SessionID, "error", err)
	} else {
		wait = planned
	}

	reason := "the usage limit has not reset yet: " + truncate(banner)
	if err := state.move(StateWaitingForReset, reason, attempt.ID); err != nil {
		return Outcome{SessionID: outcome.SessionID, State: state.current}, err
	}
	_ = m.store.SetSessionError(ctx, outcome.SessionID, truncate(banner))
	outcome.State = state.current
	outcome.Wait = wait.Delay
	outcome.WaitUntil = wait.At
	return outcome, &LimitInForceError{
		SessionID: outcome.SessionID,
		Banner:    banner,
		ResetAt:   wait.At,
		Known:     wait.Known,
		Wait:      wait.Delay,
		Cause:     cause,
	}
}

// preResumeTarget is where a failure reported before the resume is issued ends.
//
// It is a policy, not a reachability trick: no attempt row exists yet, so no
// budget was spent, and sending the session back to DISCOVERED — which is what a
// spent budget leads to — would be a lie about this session. An unrecoverable verdict is left alone,
// because no retry can answer it and the machine allows that edge from both
// pre-resume states. Both of those states also reach RETRY_WAIT, so the move is
// always legal; if a future call site breaks that, move() says so loudly rather
// than the failure being swallowed.
func preResumeTarget(target State) State {
	// A spend stop and an unrecoverable verdict are destinations: repeating them
	// on a backoff would be the spending the cap exists to prevent. Everything
	// else a pre-resume check can decide is worth another look, so it waits.
	if target == StateUnrecoverable || target == StateSpendCapReached {
		return target
	}
	return StateRetryWait
}

// attemptsThisCycle counts the attempts spent since the session last entered
// DISCOVERED, which is what the attempt budget bounds.
//
// The budget belongs to a discovery cycle, not to the session's whole life: a
// session that spends its ten attempts returns to DISCOVERED, the daemon reads
// its transcript again and schedules the next reset, and the budget starts over
// from the attempt rows written after that return. Counting every attempt ever
// would make a rediscovered session fail its own budget check on sight and never
// resume, which is exactly the dead end this design exists to avoid.
func (m *Manager) attemptsThisCycle(ctx context.Context, sessionID string) (int, error) {
	transitions, err := m.store.ListTransitions(ctx, sessionID, 0)
	if err != nil {
		return 0, fmt.Errorf("list transitions for %s: %w", sessionID, err)
	}
	var since time.Time
	for i := len(transitions) - 1; i >= 0; i-- {
		if transitions[i].ToState == storage.StateDiscovered {
			since = transitions[i].OccurredAt
			break
		}
	}
	attempts, err := m.store.ListAttempts(ctx, sessionID, 0)
	if err != nil {
		return 0, fmt.Errorf("list attempts for %s: %w", sessionID, err)
	}
	if since.IsZero() {
		return len(attempts), nil
	}
	count := 0
	for _, attempt := range attempts {
		if attempt.StartedAt.After(since) {
			count++
		}
	}
	return count, nil
}

// targetFor decides the state a failed attempt leaves the session in.
func targetFor(class Class, attemptNumber, maxAttempts int) State {
	switch class {
	case ClassUnrecoverable:
		return StateUnrecoverable
	case ClassCanceled:
		return StateRetryWait
	}
	if class.Retryable() && attemptNumber >= maxAttempts {
		// The cycle's budget is spent. The session is rediscovered, not
		// abandoned, so this is the same target as any other non-unrecoverable
		// failure.
		return StateDiscovered
	}
	return class.State()
}

// ErrHandoffUnobserved reports that a continuation ccresu handed to the
// supervisor is not in the supervisor's listing any more, or is listed in a state
// that is not evidence the continuation ran (PRD 3 R7).
//
// It is retryable on purpose. An idle stop is expected, the conversation stays on
// disk, and the next pass re-observes the session before handing it off again —
// which is also how a session that ended while the supervisor was off comes back.
var ErrHandoffUnobserved = errors.New("recovery: the handed-off session is not being observed")

// handoffLost decides whether the supervisor says the handed-off continuation is
// gone, and why.
//
// Two readings mean it is gone:
//
//   - The session was in the listing before the handoff and is not in it after
//     (PRD 2 R8's HandoffUnobserved). A session that was never listed is a
//     different case and deliberately not a loss: a handoff issued a moment ago is
//     not in the listing yet, and the `backgrounded` block is what proves the
//     supervisor took it.
//   - It is listed with a state that is itself a retryable outcome — `stopped`,
//     `failed`, or a value ccresu does not recognise. None of them is evidence
//     that the continuation ran, and PRD 2's mapping already says so.
//
// A session still `working`, already `done`, or now `blocked` is not lost: the
// supervisor owns the work, and a blocked session is parked by the next pass on
// its published state rather than by a guess here.
func handoffLost(after, before StateObservation) (reason string, lost bool) {
	decision := Observe(after, true, false)
	switch {
	case decision.NeedsInput:
		return "", false
	case decision.HandoffUnobserved && before.InListing:
		return "the session vanished from the supervisor's listing after the handoff", true
	case decision.HandoffUnobserved:
		return "", false
	case decision.State == StateRetryWait:
		return fmt.Sprintf("the supervisor publishes %q for the handed-off session, which is not evidence that the continuation ran", after.State), true
	}
	return "", false
}

// retryHandoff records a handoff whose continuation the supervisor did not keep.
//
// The attempt is closed *unresolved* — `Success` stays nil, exactly as R6 closes
// an attempt whose child disappeared — because the handoff was accepted and what
// became of the work is unknown. Writing `false` would claim the continuation
// failed, and writing `true` would claim it ran; neither is something ccresu saw.
//
// The session is parked in RETRY_WAIT with a scheduled retry, so the next pass
// re-observes it before handing off again (R7). Nothing here is announced as a
// permanent failure and nothing is resolved with `respawn` (R4): an idle stop is
// expected, the conversation is on disk, and the retry continues it.
func (m *Manager) retryHandoff(ctx context.Context, state *stateWalker, outcome Outcome,
	attempt storage.RecoveryAttempt, verification Verification,
	observation StateObservation, reason string) (Outcome, error) {
	cause := fmt.Errorf("%w: %s", ErrHandoffUnobserved, reason)
	verification.ObservePublished(observation, Observe(observation, true, false))
	outcome.Verification = verification

	reasonText := fmt.Sprintf("%s; re-observing the session before the next handoff", reason)
	if _, err := m.store.FinishAttempt(ctx, attempt.ID, storage.AttemptResult{
		State:             StateRetryWait,
		Error:             truncate(reasonText),
		SessionIDVerified: boolPtr(verification.SessionIDPreserved),
		TranscriptGrew:    boolPtr(verification.TranscriptGrew),
		HandoffStateID:    verification.HandoffStateID,
		HandoffName:       verification.HandoffName,
		HandoffAt:         handoffTime(verification, m.now()),
		HandoffCopied:     handoffCopied(verification),
		Observation:       RecordedObservation(observation),
		FinishedAt:        m.now(),
	}); err != nil {
		m.log.Warn("record the handed-off attempt", "session", outcome.SessionID, "error", err)
	}

	attemptNumber := attempt.AttemptNumber
	delay := m.backoff.Delay(attemptNumber)
	if err := state.move(StateRetryWait, truncate(fmt.Sprintf("%s; next check in %s", reasonText, delay)), attempt.ID); err != nil {
		return Outcome{SessionID: outcome.SessionID, State: state.current, Supervisor: outcome.Supervisor}, err
	}
	if err := m.store.SetSessionRetryAt(ctx, outcome.SessionID, m.now().Add(delay)); err != nil {
		m.log.Warn("record next attempt", "session", outcome.SessionID, "error", err)
	}
	_ = m.store.SetSessionError(ctx, outcome.SessionID, truncate(cause.Error()))
	outcome.State = state.current
	outcome.Delay = delay
	return outcome, &RetryError{
		SessionID: outcome.SessionID, Attempt: attemptNumber, Max: m.maxAttempts,
		Delay: delay, Cause: cause,
	}
}

// closeInterruptedAttempt finishes an attempt left running by a process that died.
//
// The attempt is *interrupted*, not failed. Its child vanished because ccresu was
// restarted or the machine rebooted, and the supervisor — which owns the session
// now — may have kept the work running, restarted it, or idle-stopped it. The
// docs describe all three as normal, so "the child disappeared" is not evidence
// about the work at all (PRD 3 R6).
//
// Two consequences follow, and both are the point of this function:
//
//   - The row is closed with `Success` left nil. The pointer already means
//     "unresolved", which is exactly what an attempt nobody watched end is;
//     writing `false` claimed a verdict a restart cannot support and made the
//     operator read a failure for work that never stopped.
//   - The session returns to DISCOVERED rather than to RETRY_WAIT, so the next
//     pass re-observes it (PRD 2) instead of handing off again on top of a
//     session the supervisor may still be hosting. DISCOVERED is also what makes
//     the interruption free: the attempt budget counts the attempts started since
//     the last DISCOVERED transition, so an interrupted attempt can never spend a
//     cycle's budget.
func (m *Manager) closeInterruptedAttempt(ctx context.Context, state *stateWalker, sessionID string) (bool, error) {
	latest, err := m.store.LatestAttempt(ctx, sessionID)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if !latest.FinishedAt.IsZero() {
		return false, nil
	}
	m.log.Warn("closing an attempt interrupted by a restart", "session", sessionID, "attempt", latest.AttemptNumber)
	reason := "the attempt was interrupted by a process restart"
	// Success is left nil on purpose. The session itself is moved through the
	// walker below, so the persisted state and the transition history agree.
	if _, err := m.store.FinishAttempt(ctx, latest.ID, storage.AttemptResult{
		State:      StateDiscovered,
		FinishedAt: m.now(),
		Error:      reason + "; the outcome is unknown, so it is not recorded as a failure",
	}); err != nil {
		return false, fmt.Errorf("close interrupted attempt: %w", err)
	}
	if err := state.move(StateDiscovered, reason+"; re-observing the session before handing off again", latest.ID); err != nil {
		return true, fmt.Errorf("record interrupted attempt: %w", err)
	}
	return true, nil
}

// Pending returns the sessions a restarted daemon must continue.
func (m *Manager) Pending(ctx context.Context) ([]storage.Session, error) {
	enabled := true
	sessions, err := m.store.ListSessions(ctx, storage.SessionFilter{
		Enabled: &enabled,
		States:  PendingStates(),
	})
	if err != nil {
		return nil, err
	}
	return sessions, nil
}

// History is the persisted audit trail of one session.
type History struct {
	SessionID   string
	State       State
	Attempts    []storage.RecoveryAttempt
	Transitions []storage.Transition
}

// History returns a session's attempts and transitions, newest attempt first.
func (m *Manager) History(ctx context.Context, sessionID string) (History, error) {
	if strings.TrimSpace(sessionID) == "" {
		return History{}, ErrNoSession
	}
	session, err := m.store.GetSession(ctx, sessionID)
	if err != nil {
		return History{}, err
	}
	attempts, err := m.store.ListAttempts(ctx, sessionID, 0)
	if err != nil {
		return History{}, err
	}
	transitions, err := m.store.ListTransitions(ctx, sessionID, 0)
	if err != nil {
		return History{}, err
	}
	return History{
		SessionID:   session.ID,
		State:       session.State,
		Attempts:    attempts,
		Transitions: transitions,
	}, nil
}

// now reports the current UTC time from the injected clock.
func (m *Manager) now() time.Time { return m.clock.Now().UTC() }

// stateWalker moves one session through the machine, refusing illegal edges and
// keeping the persisted state, the audit trail and its own idea of the current
// state in agreement.
type stateWalker struct {
	ctx       context.Context
	store     Store
	sessionID string
	current   State
}

// move records a transition and updates the walker's current state.
//
// Moving to the state the session is already in is not a no-op: the audit row is
// still written, because "the limit was checked again and was still in force" is
// information worth keeping.
func (w *stateWalker) move(to State, reason string, attemptID int64) error {
	if w.current == to {
		return w.store.RecordTransition(w.ctx, storage.Transition{
			SessionID: w.sessionID,
			AttemptID: attemptID,
			FromState: w.current,
			ToState:   to,
			Reason:    reason,
		})
	}
	if err := ValidateTransition(w.current, to); err != nil {
		return fmt.Errorf("%w (session %s)", err, w.sessionID)
	}
	if _, err := w.store.TransitionSession(w.ctx, w.sessionID, to, reason, attemptID); err != nil {
		return err
	}
	w.current = to
	return nil
}

// sessionLocks is a keyed mutex: one lock per session id, reclaimed when the
// last holder releases, so a long-running daemon does not accumulate locks for
// sessions it will never see again.
type sessionLocks struct {
	mu    sync.Mutex
	locks map[string]*sessionLock
}

type sessionLock struct {
	mu   sync.Mutex
	refs int
}

func newSessionLocks() *sessionLocks {
	return &sessionLocks{locks: make(map[string]*sessionLock)}
}

// tryLock takes the session lock without blocking, reporting whether it got it.
func (l *sessionLocks) tryLock(sessionID string) bool {
	l.mu.Lock()
	entry, ok := l.locks[sessionID]
	if !ok {
		entry = &sessionLock{}
		l.locks[sessionID] = entry
	}
	entry.refs++
	l.mu.Unlock()

	if !entry.mu.TryLock() {
		l.release(sessionID, entry, false)
		return false
	}
	return true
}

// unlock releases a lock taken by tryLock.
func (l *sessionLocks) unlock(sessionID string) {
	l.mu.Lock()
	entry := l.locks[sessionID]
	l.mu.Unlock()
	if entry == nil {
		return
	}
	entry.mu.Unlock()
	l.release(sessionID, entry, true)
}

// release drops a reference and removes the entry when it is the last one.
func (l *sessionLocks) release(sessionID string, entry *sessionLock, unlocked bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry.refs--
	if entry.refs <= 0 {
		delete(l.locks, sessionID)
	}
	_ = unlocked
}

// held reports how many locks are alive, for tests.
func (l *sessionLocks) held() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.locks)
}

// mergeBaseline prefers the fresher status measurement and falls back to the
// persisted snapshot when the status could not read the transcript. Without this
// a session whose transcript is unreadable would silently lose its baseline and
// a later resume could never be proven.
func mergeBaseline(stored, fresh Baseline) Baseline {
	merged := stored
	if fresh.SessionID != "" {
		merged.SessionID = fresh.SessionID
	}
	if fresh.TranscriptPath != "" {
		merged.TranscriptPath = fresh.TranscriptPath
	}
	if fresh.TranscriptLines > 0 {
		merged.TranscriptLines = fresh.TranscriptLines
	}
	if fresh.TranscriptSize > 0 {
		merged.TranscriptSize = fresh.TranscriptSize
	}
	return merged
}

// grew reports whether a status shows more transcript than the baseline.
func grew(baseline Baseline, status claude.SessionStatus) bool {
	if baseline.TranscriptLines <= 0 {
		return false
	}
	return status.Transcript.Lines > baseline.TranscriptLines
}

// absorbAttemptWrites advances the stored transcript baseline past whatever the
// failed attempt itself wrote.
//
// A resume that fails still touches the transcript: the CLI appends its
// refusal, its error, or the beginning of a turn it then abandoned. Those lines
// belong to the attempt, not to the conversation continuing by itself, so the
// baseline is moved to the post-attempt observation and the "the session had
// already resumed after the limit reset" shortcut keeps meaning what it says.
//
// The re-read is deliberately a fresh status rather than an estimate: ccresu
// cannot know how many lines a refused command wrote, and guessing would either
// hide real growth or keep counting the attempt's own. A re-read that fails
// leaves the old baseline in place and says so, because a stale baseline that is
// reported is better than one that is silently rewritten.
func (m *Manager) absorbAttemptWrites(ctx context.Context, sessionID string) {
	status, err := m.provider.GetSessionStatus(ctx, sessionID)
	if err != nil {
		m.log.Warn("re-read the transcript after a failed attempt",
			"session", sessionID, "error", err)
		return
	}
	if status.Transcript.Lines <= 0 {
		// Nothing to absorb: an unreadable transcript proves nothing either way.
		return
	}
	path := status.Transcript.Path
	if path == "" {
		path = status.Session.TranscriptPath
	}
	if err := m.store.SetSessionTranscriptMetrics(ctx, sessionID, path,
		status.Transcript.Size, status.Transcript.Lines); err != nil {
		m.log.Warn("record the transcript after a failed attempt",
			"session", sessionID, "error", err)
	}
}

// commandPreview renders the handoff recorded with an attempt.
//
// The shape matches what is actually executed — `claude --bg --resume <id>
// --name <label> <message>` — because the command column is the audit trail an
// operator reads to answer "what did ccresu run?". Recording the retirement-era
// print-mode invocation here would describe a command that no longer happens,
// and would be the only place left in the binary that claims recovery uses
// print mode.
//
// The message itself is summarised rather than stored in full: the column is an
// audit aid, not a copy of the conversation.
func commandPreview(sessionID, message string) string {
	summary := "no message"
	if trimmed := strings.TrimSpace(message); trimmed != "" {
		summary = fmt.Sprintf("%d-byte continuation message", len(trimmed))
	}
	return fmt.Sprintf("claude --bg --resume %s --name %s <%s>",
		sessionID, claude.HandoffLabel(sessionID), summary)
}

// truncate caps a stored string so a huge CLI dump cannot bloat the database.
func truncate(s string) string {
	const limit = 2000
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…(truncated)"
}

// firstLine returns the first non-empty line of s, for compact error text.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return truncate(trimmed)
		}
	}
	return ""
}

// boolPtr returns a pointer to b, for the nullable attempt columns.
func boolPtr(b bool) *bool { return &b }

// handoffTime is when the handoff was accepted, or zero when there was none.
//
// It is the attempt's "not stale" bit, so it is only set for an accepted
// handoff: stamping it on a print-mode resume would tell a restarting ccresu
// that a supervisor owns work that never left this process.
func handoffTime(verification Verification, now time.Time) time.Time {
	if !verification.HandoffAccepted {
		return time.Time{}
	}
	return now.UTC()
}

// handoffCopied records the copy-or-in-place outcome, nil when there was no
// handoff, because "we did not ask" and "it continued in place" are different
// facts.
func handoffCopied(verification Verification) *bool {
	if !verification.HandoffAccepted {
		return nil
	}
	return boolPtr(verification.Copied)
}

// resume issues the handoff in the directory the session occupies.
//
// PRD 6 R3: the directory is resolved in a fixed order — the freshest
// observation's run directory, then the persisted run directory, then the
// project directory. The observation is the only source that knows a session
// moved into a worktree since the last pass; the persisted row is the only
// source after a restart for a session the CLI no longer lists. Neither is
// guessed, and an empty resolution is a named failure rather than an inherited
// cwd (R2).
//
// A provider that cannot be told a directory resolves it itself; that fallback
// keeps fakes and tests which never cared about directories working unchanged.
func (m *Manager) resume(ctx context.Context, session storage.Session, observed claude.Session, sessionID, message string) (claude.ResumeResult, error) {
	resumer, ok := m.provider.(claude.SessionResumerIn)
	if !ok {
		return m.provider.ResumeSession(ctx, sessionID, message)
	}
	workspace, err := claude.ResolveWorkspace(
		firstNonEmpty(observed.RunsInPath, session.RunsInPath),
		firstNonEmpty(observed.ProjectPath, session.ProjectPath),
	)
	if err != nil {
		return claude.ResumeResult{}, err
	}
	if workspace.FellBack {
		// R8: the fallback is announced rather than silent, because a recovery
		// that ran in the checkout instead of the worktree is exactly the
		// wrong-directory success this PRD exists to make visible.
		m.log.Warn("the session's recorded run directory is gone; resuming in the project directory",
			"session", sessionID, "missing", workspace.Missing, "using", workspace.Dir)
	}
	return resumer.ResumeSessionIn(ctx, sessionID, message, workspace.Dir)
}

// firstNonEmpty returns the first value that is not blank.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
