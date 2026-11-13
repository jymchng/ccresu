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
		store:       cfg.Store,
		provider:    cfg.Provider,
		clock:       clk,
		log:         logger,
		enabled:     cfg.Enabled,
		maxAttempts: maxAttempts,
		backoff:     backoff,
		message:     message,
		verifier:    verifier,
		limitCheck:  cfg.LimitRecheck,
		limitPark:   cfg.LimitPark,
		locks:       newSessionLocks(),
		sem:         make(chan struct{}, maxConcurrent),
	}, nil
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
}

// Recover attempts one recovery of a session.
func (m *Manager) Recover(ctx context.Context, sessionID string) (Outcome, error) {
	return m.RecoverRequest(ctx, Request{SessionID: sessionID})
}

// Retry attempts a recovery of a session whose budget is spent.
func (m *Manager) Retry(ctx context.Context, sessionID string) (Outcome, error) {
	return m.RecoverRequest(ctx, Request{SessionID: sessionID, Force: true})
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
	// restart or a reboot. Close it truthfully rather than leaving a phantom
	// "running" attempt that hides the next one.
	if _, err := m.closeStaleAttempt(ctx, sessionID); err != nil {
		return Outcome{SessionID: sessionID, State: state.current}, err
	}

	outcome := Outcome{SessionID: sessionID}
	baseline := BaselineFromSession(session)

	if err := state.move(StateCheckingSession, "checking the session before resuming", 0); err != nil {
		return Outcome{SessionID: sessionID, State: state.current}, err
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

	// It may have continued on its own once the window reset. That is a
	// success, and it must be recognised without issuing a resume — resuming a
	// session that is already working would duplicate its work.
	//
	// The comparison uses the snapshot taken *before* the merge: merging copies
	// the fresh line count over the stored one, which would make every status
	// look unchanged. Only the persisted snapshot can prove movement.
	if status.Running && grew(baseline, status) {
		verification := Verification{
			SessionIDPreserved: true,
			TranscriptGrew:     true,
			Proven:             true,
			LinesBefore:        baseline.TranscriptLines,
			LinesAfter:         status.Transcript.Lines,
			SizeBefore:         baseline.TranscriptSize,
			SizeAfter:          status.Transcript.Size,
			Running:            true,
			Notes: []string{
				"the session had already continued on its own, so no resume was needed",
			},
		}
		if err := m.store.SetSessionTranscriptMetrics(ctx, sessionID, status.Transcript.Path,
			status.Transcript.Size, status.Transcript.Lines); err != nil {
			m.log.Warn("record transcript metrics", "session", sessionID, "error", err)
		}
		if err := state.move(StateResumed, "session had already resumed after the limit reset", 0); err != nil {
			return Outcome{SessionID: sessionID, State: state.current}, err
		}
		outcome.State = state.current
		outcome.Verification = verification
		outcome.Resumed = true
		return outcome, nil
	}
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

	result, resumeErr := m.provider.ResumeSession(ctx, sessionID, message)
	if resumeErr != nil {
		return m.fail(ctx, state, outcome, baseline, attempt, Verification{},
			fmt.Errorf("resume session %s: %w", sessionID, resumeErr))
	}
	if result.IsError {
		return m.fail(ctx, state, outcome, baseline, attempt, Verification{},
			fmt.Errorf("resume session %s: %w: %s", sessionID, claude.ErrResumeFailed, firstLine(result.Result)))
	}

	verification, verifyErr := m.verifier.Verify(ctx, baseline, result)
	if verifyErr != nil {
		return m.fail(ctx, state, outcome, baseline, attempt, verification,
			fmt.Errorf("verify session %s: %w", sessionID, verifyErr))
	}
	if !verification.Proven {
		return m.fail(ctx, state, outcome, baseline, attempt, verification,
			VerificationFailedError(sessionID, verification))
	}

	finished, err := m.store.FinishAttempt(ctx, attempt.ID, storage.AttemptResult{
		State:             StateResumed,
		Success:           true,
		Output:            truncate(result.Result),
		SessionIDVerified: boolPtr(true),
		TranscriptGrew:    boolPtr(true),
		FinishedAt:        m.now(),
	})
	if err != nil {
		return Outcome{SessionID: sessionID, State: state.current}, fmt.Errorf("finish attempt: %w", err)
	}
	outcome.Attempt = finished
	outcome.Verification = verification

	if err := m.store.SetSessionTranscriptMetrics(ctx, sessionID, baseline.TranscriptPath,
		verification.SizeAfter, verification.LinesAfter); err != nil {
		m.log.Warn("record transcript metrics", "session", sessionID, "error", err)
	}
	if err := m.store.SetSessionError(ctx, sessionID, ""); err != nil {
		m.log.Warn("clear session error", "session", sessionID, "error", err)
	}
	if err := state.move(StateResumed, "the original session resumed and the conversation grew", attempt.ID); err != nil {
		return Outcome{SessionID: sessionID, State: state.current}, err
	}
	outcome.State = state.current
	outcome.Resumed = true
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
			State:             target,
			Success:           false,
			Error:             truncate(cause.Error()),
			SessionIDVerified: boolPtr(verification.SessionIDPreserved),
			TranscriptGrew:    boolPtr(verification.TranscriptGrew),
			FinishedAt:        m.now(),
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
			Success:           false,
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
	if target == StateUnrecoverable {
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

// closeStaleAttempt finishes an attempt left running by a process that died.
//
// This is what makes a restart safe: the interrupted attempt is recorded as a
// failure — never as a success — and the next attempt starts with the truth on
// disk.
func (m *Manager) closeStaleAttempt(ctx context.Context, sessionID string) (bool, error) {
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
	if _, err := m.store.FinishAttempt(ctx, latest.ID, storage.AttemptResult{
		State:             StateRetryWait,
		Success:           false,
		Error:             reason,
		SessionIDVerified: boolPtr(false),
		TranscriptGrew:    boolPtr(false),
		FinishedAt:        m.now(),
	}); err != nil {
		return false, fmt.Errorf("close interrupted attempt: %w", err)
	}
	if err := m.store.RecordTransition(ctx, storage.Transition{
		SessionID:  sessionID,
		AttemptID:  latest.ID,
		FromState:  latest.State,
		ToState:    StateRetryWait,
		Reason:     reason,
		OccurredAt: m.now(),
	}); err != nil {
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

// commandPreview renders the resume command recorded with an attempt.
//
// The continuation message is summarised rather than stored in full: the
// command column is an audit aid, not a copy of the conversation.
func commandPreview(sessionID, message string) string {
	summary := "no message"
	if trimmed := strings.TrimSpace(message); trimmed != "" {
		summary = fmt.Sprintf("%d-byte continuation message", len(trimmed))
	}
	return fmt.Sprintf("claude -p --resume %s --output-format json <%s>", sessionID, summary)
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
