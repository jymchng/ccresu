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
// session is waiting for the reset instead of resuming now.
//
// It has exactly one producer: a resume the CLI refused. That refusal is
// reported as *LimitInForceError, which unwraps to the adapter's
// claude.ErrUsageLimitInForce and reports this sentinel through Is, so a caller
// that only knows the sentinel still learns that the session is waiting rather
// than broken. There is no other way to learn it: a usage window is only ever
// announced by a `claude` run, so a pass that has not run the command cannot
// know, and there is no pre-resume check that could report it instead.
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
	// GetSchedule reads the session's pending wait, or a storage.ErrNotFound
	// when it has none. A search uses it to tell a corrected reset time from one
	// that already matched, so the history records only real changes.
	GetSchedule(ctx context.Context, sessionID string) (storage.Schedule, error)
}

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
	// Recorder overrides the default post-resume measurement, for tests and
	// for hosts that read the transcript differently. Nil measures through
	// Provider.
	Recorder *Recorder
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
	recorder    Recorder
	limitPark   LimitPark
	limits      SpendLimits
	observer    Observer

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

	recorder := Recorder{Provider: cfg.Provider}
	if cfg.Recorder != nil {
		recorder = *cfg.Recorder
		if recorder.Provider == nil {
			recorder.Provider = cfg.Provider
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
		recorder:    recorder,
		limits:      cfg.Limits,
		limitPark:   cfg.LimitPark,
		observer:    cfg.Observer,
		locks:       newSessionLocks(),
		sem:         make(chan struct{}, maxConcurrent),
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

// recordTranscriptMetrics stores the transcript measurement a status check
// observed, so the next pass compares against what is actually on disk.
//
// It is a diagnostic and nothing else (R3): no decision is read back out of it,
// and a failure to record is logged rather than returned. A session that was
// observed, parked or resumed still owes the operator its state even if the
// measurement could not be stored.
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
	// set only when a failed attempt was parked in WAITING_FOR_RESET for a retry.
	Delay time.Duration
	// Wait is how long the session must wait before ccresu looks again. It is
	// set only when State is WAITING_FOR_RESET because a resume was refused by a
	// usage limit: the refusal names when the work may continue, so there is
	// nothing to retry in the meantime.
	Wait time.Duration
	// WaitUntil is the instant Wait ends: the reset the refusal named, or the
	// first poll when it named none. Zero when no wait was planned.
	WaitUntil time.Time
	// Resumed reports that the original conversation continued: ccresu ran the
	// resume and its turn finished.
	Resumed bool
	// Interrupted reports that the pass ended because an attempt left running by
	// a process that died was closed as interrupted. It is not a failure:
	// nothing was resumed, no budget was spent, and the session returns to
	// DISCOVERED for the next pass to look at again.
	Interrupted bool
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
	// A settled session is left alone: RESUMED because it is already alive and
	// the banner that parked it must not drive a second resume, COMPLETED because
	// there is nothing to continue, and the two verdicts because neither is
	// ccresu's to undo.
	if Settled(state.current) {
		return Outcome{SessionID: sessionID, State: state.current, Resumed: state.current == StateResumed},
			stateError(sessionID, state.current, StateResuming)
	}
	attempts, err := m.attemptsThisCycle(ctx, sessionID)
	if err != nil {
		return Outcome{SessionID: sessionID, State: state.current}, err
	}
	if attempts >= m.maxAttempts && !req.Force {
		// The budget for this cycle is spent. The session goes back to
		// DISCOVERED rather than to a terminal failure state: the daemon
		// rediscovers it, re-reads the transcript and schedules the next reset,
		// which starts a fresh budget. No operator command is needed to make that
		// happen; `ccresu retry` is only for an operator who would rather spend
		// one more attempt now.
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
	// there: the resume that row belongs to may still be running, so sending a
	// second continuation in the same pass would duplicate the recovery that turn
	// is already doing (PRD 3 R6).
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

	// Everything below until the resume is pre-flight: the status read and the
	// spend cap. It runs in the state the pass found the session in
	// — DISCOVERED, or WAITING_FOR_RESET for a session woken by its schedule —
	// because the machine has no separate "checking" beat. RESUMING is entered
	// once, immediately before the continuation is sent, so every attempt row
	// sits under exactly one RESUMING transition and the audit trail says what
	// was spent.
	//
	// Nothing here tries to predict what the resume will do. There is no
	// published-state listing to consult and no supervisor to ask, and there is
	// no usage-limit recheck either: a window can only be announced by a claude
	// run, so the resume is the check. The command runs the turn and reports
	// through its exit status, so the pass's job before it runs is only to
	// establish that a continuation is allowed to be sent.
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
	// The status is read even though nothing is decided from it any more: it says
	// whether the session is still recoverable, and it is the freshest transcript
	// measurement available before the turn. What happens next is settled by the
	// resume itself, not by a listing of published state.
	m.recordTranscriptMetrics(ctx, sessionID, status)
	baseline = mergeBaseline(baseline, BaselineFromStatus(status))

	// R5: the session spend check runs here, before an attempt row exists, so a
	// refusal spends nothing — the same rule the attempt budget follows, where a
	// request that spends no attempt has started nothing. It is a pre-resume
	// check, so it ends the pass through fail() with no attempt row behind it.
	if err := m.checkSessionCap(ctx, sessionID, req.Manual); err != nil {
		return m.fail(ctx, state, Outcome{SessionID: sessionID}, baseline,
			storage.RecoveryAttempt{}, Verification{}, err)
	}

	message := req.Message
	if strings.TrimSpace(message) == "" {
		message = m.message
	}
	// Every pre-flight check has passed, so the continuation is about to be
	// handed over: this is the one beat that spends an attempt, and the attempt
	// row is written under it.
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
	// R6: one attempt over the per-attempt cap stops recovery. The check runs
	// after the cost is known and before the outcome is decided, and fail()
	// records the cost it judged rather than discarding it.
	if capErr := m.checkAttemptCap(attempt, attempt.CostUSD); capErr != nil {
		return m.fail(ctx, state, outcome, baseline, attempt, Verification{}, capErr)
	}

	// The provider has already turned a non-zero exit into an error, so a result
	// that reaches here ran the turn to its end. What follows measures the
	// transcript. Nothing it finds can revise that verdict, and a measurement
	// that cannot be taken is recorded as absent rather than promoted into a
	// second opinion about work that has already finished.
	verification := m.recorder.Measure(ctx, baseline, result)
	for _, note := range verification.Notes {
		m.log.Info("post-resume measurement", "session", sessionID, "note", note)
	}

	finished, err := m.store.FinishAttempt(ctx, attempt.ID, storage.AttemptResult{
		State:          StateResumed,
		Success:        boolPtr(true),
		TranscriptGrew: boolPtr(verification.TranscriptGrew),
		CostUSD:        attempt.CostUSD,
		FinishedAt:     m.now(),
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
	// reads in `ccresu history`: the resume's turn finished, and the transcript
	// did or did not grow while it ran.
	if err := state.move(StateResumed, verification.Reason(), attempt.ID); err != nil {
		return Outcome{SessionID: sessionID, State: state.current}, err
	}
	outcome.State = state.current
	outcome.Resumed = true
	m.announce(ctx, MilestoneSucceeded, outcome, nil)
	return outcome, nil
}

// fail records a failed attempt and moves the session along the retry path.
//
// A retryable failure below the attempt budget is parked in WAITING_FOR_RESET
// with the computed delay on retry_at. Everything else is rediscovered: the
// session returns to DISCOVERED, where the next cycle re-reads its transcript
// with a fresh budget. Only a proved-unrecoverable session stops there, because
// that verdict is a fact about the conversation rather than a count of attempts.
//
// A failure reported before the resume was issued — the pre-resume status check
// or the spend cap — has no attempt row behind it, so it spends no budget. A
// spend stop and an unrecoverable verdict are still destinations,
// because repeating either on a backoff would be the spending or the loop the
// verdict exists to prevent; everything else a pre-resume check can decide is
// worth another look, so it waits.
func (m *Manager) fail(ctx context.Context, state *stateWalker, outcome Outcome, baseline Baseline,
	attempt storage.RecoveryAttempt, verification Verification, cause error) (Outcome, error) {
	class := Classify(cause)
	outcome.Verification = verification

	// A resume that failed may still have written to the transcript: the CLI
	// appends its refusal, its error or a partial turn before exiting. Those
	// lines are the failed attempt's own work, not the turn's, so the stored
	// baseline is advanced past them before anything is decided. Otherwise the
	// next pass would measure growth the failure caused itself and record it
	// beside a later attempt as though the conversation had advanced in between,
	// which is how a refused resume used to be reported as a success.
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
			CostUSD:        attempt.CostUSD,
			TranscriptGrew: boolPtr(verification.TranscriptGrew),
			FinishedAt:     m.now(),
		}); err != nil {
			m.log.Warn("record failed attempt", "session", outcome.SessionID, "error", err)
		}
	}

	// WAITING_FOR_RESET is the park, and it is the one destination with a wait to
	// hand back, so the persisted state and the delay the caller observes can
	// never disagree. The session stays there rather than returning to
	// DISCOVERED: entering DISCOVERED resets the attempt budget, so a failure that
	// looped back there could spend an unlimited number of attempts on a flaky
	// network. The instant the retry is due goes on retry_at, which is a schedule
	// and not a state.
	retry := class.Retryable() && attemptNumber < m.maxAttempts
	if retry || target == StateWaitingForReset {
		delay := m.backoff.Delay(attemptNumber)
		reason := fmt.Sprintf("attempt %d/%d failed (%s); next attempt in %s: %v",
			attemptNumber, m.maxAttempts, class, delay, cause)
		if preResume {
			reason = fmt.Sprintf("the pre-resume check failed (%s); no attempt was spent, next check in %s: %v",
				class, delay, cause)
		}
		if err := state.move(StateWaitingForReset, truncate(reason), attempt.ID); err != nil {
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
		// The session is parked with a scheduled retry and ccresu will try again
		// by itself, so this is the "attempt failed, still working on it"
		// message. A refusal never reaches here: park() handles ClassLimitInForce
		// above.
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
	// record a retry transition over a session that is parked, which is a false
	// account of an attempt that has already been answered.
	if attempt.ID != 0 {
		verification := outcome.Verification
		if _, err := m.store.FinishAttempt(ctx, attempt.ID, storage.AttemptResult{
			State:          StateWaitingForReset,
			Success:        boolPtr(false),
			Error:          truncate(banner),
			TranscriptGrew: boolPtr(verification.TranscriptGrew),
			FinishedAt:     m.now(),
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
// budget was spent. A spend stop and an unrecoverable verdict are destinations —
// repeating either on a backoff would be the spending, or the loop, that the
// verdict exists to prevent — and both are legal edges from every state a
// pre-resume check can run in. Everything else waits: WAITING_FOR_RESET is the
// machine's only park, and a check that failed is exactly what a retry instant
// describes. If a future call site breaks that, move() says so loudly rather than
// the failure being swallowed.
func preResumeTarget(target State) State {
	if target == StateUnrecoverable || target == StateDisabled {
		return target
	}
	return StateWaitingForReset
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
//
// The class already names the destination (see Class.State); the only thing
// decided here is the attempt budget. A retryable failure with the budget spent
// is rediscovered rather than parked, so the next cycle re-reads the transcript
// and starts a fresh budget instead of backing off forever on a session that has
// already been tried ten times.
func targetFor(class Class, attemptNumber, maxAttempts int) State {
	if class.Retryable() && attemptNumber >= maxAttempts {
		// The cycle's budget is spent. The session is rediscovered, not
		// abandoned, so this is the same target as any other non-unrecoverable
		// failure.
		return StateDiscovered
	}
	return class.State()
}

// closeInterruptedAttempt finishes an attempt left running by a process that died.
//
// The attempt is *interrupted*, not failed. Its child vanished because ccresu was
// restarted or the machine rebooted, so what became of the turn is unknown: it
// may have finished, been cut off mid-answer, or never started. "The child
// disappeared" is therefore not evidence about the work at all, and the row says
// so rather than guessing.
//
// Two consequences follow, and both are the point of this function:
//
//   - The row is closed with `Success` left nil. The pointer already means
//     "unresolved", which is exactly what an attempt nobody watched end is;
//     writing `false` claimed a verdict a restart cannot support and made the
//     operator read a failure for work that never stopped.
//   - The session returns to DISCOVERED rather than being parked, so the next
//     pass reads it again instead of resuming on top of a turn that may still be
//     running. DISCOVERED is also what makes the interruption free: the attempt
//     budget counts the attempts started since the last DISCOVERED transition, so
//     an interrupted attempt can never spend a cycle's budget.
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
// still written, because "this pass failed and parked the session it found
// parked" is information worth keeping.
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
// baseline is moved to the post-attempt observation and the next diagnostic
// compares against what the attempt actually left behind.
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

// commandPreview renders the resume recorded with an attempt.
//
// The shape matches what is actually executed — `claude --resume <id> <message>`
// — because the command column is the audit trail an operator reads to answer
// "what did ccresu run?".
//
// The message itself is summarised rather than stored in full: the column is an
// audit aid, not a copy of the conversation.
func commandPreview(sessionID, message string) string {
	summary := "no message"
	if trimmed := strings.TrimSpace(message); trimmed != "" {
		summary = fmt.Sprintf("%d-byte continuation message", len(trimmed))
	}
	return fmt.Sprintf("claude --resume %s <%s>", sessionID, summary)
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

// resume issues the resume in the directory the session occupies.
//
// PRD 6 R3: the directory is resolved in a fixed order — the freshest status's
// run directory, then the persisted run directory, then the project directory.
// The status is the only source that knows a session moved into a worktree since
// the last pass; the persisted row is the only source after a restart for a
// session the CLI no longer lists. Neither is guessed, and an empty resolution is
// a named failure rather than an inherited cwd (R2).
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
