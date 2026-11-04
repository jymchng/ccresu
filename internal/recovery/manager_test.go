package recovery

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/config"
	"github.com/jymchng/ccresu/internal/storage"
)

// providerStub is a claude.SessionProvider driven by functions, so each test
// states exactly what the CLI reports and when.
type providerStub struct {
	mu          sync.Mutex
	statusFn    func(call int, sessionID string) (claude.SessionStatus, error)
	resumeFn    func(sessionID, message string) (claude.ResumeResult, error)
	statusCalls int
	resumes     []resumeCall
	discovers   int
}

// resumeCall records one resume invocation.
type resumeCall struct {
	sessionID string
	message   string
}

func (p *providerStub) Discover(context.Context, claude.DiscoverFilter) ([]claude.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.discovers++
	return nil, nil
}

func (p *providerStub) GetSession(_ context.Context, sessionID string) (claude.Session, error) {
	return claude.Session{ID: sessionID, Recoverable: true}, nil
}

func (p *providerStub) GetSessionStatus(_ context.Context, sessionID string) (claude.SessionStatus, error) {
	p.mu.Lock()
	p.statusCalls++
	call := p.statusCalls
	fn := p.statusFn
	p.mu.Unlock()
	if fn == nil {
		return statusFor(sessionID, 0, 0), nil
	}
	return fn(call, sessionID)
}

func (p *providerStub) ResumeSession(_ context.Context, sessionID, message string) (claude.ResumeResult, error) {
	p.mu.Lock()
	p.resumes = append(p.resumes, resumeCall{sessionID: sessionID, message: message})
	fn := p.resumeFn
	p.mu.Unlock()
	if fn == nil {
		return goodResult(sessionID), nil
	}
	return fn(sessionID, message)
}

func (p *providerStub) resumeCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.resumes)
}

func (p *providerStub) statusCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.statusCalls
}

// growthProvider reports the baseline on the first status check and a grown
// transcript on every later one, which is what a real resume looks like.
func growthProvider(before, after int64) *providerStub {
	return &providerStub{statusFn: func(call int, sessionID string) (claude.SessionStatus, error) {
		if call == 1 {
			return statusFor(sessionID, before, before*20), nil
		}
		return statusFor(sessionID, after, after*20), nil
	}}
}

// staticProvider always reports the same transcript.
func staticProvider(lines, size int64) *providerStub {
	return &providerStub{statusFn: func(_ int, sessionID string) (claude.SessionStatus, error) {
		return statusFor(sessionID, lines, size), nil
	}}
}

// storeSeq makes every in-memory database distinct even when several are opened
// inside the same nanosecond.
var storeSeq atomic.Int64

// newTestStore opens a migrated, private in-memory store.
func newTestStore(t *testing.T) *storage.Store {
	t.Helper()
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	store, err := storage.Open(context.Background(), storage.Options{
		Memory: true,
		Clock: func() time.Time {
			return base.Add(time.Duration(storeSeq.Add(1)) * time.Microsecond)
		},
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

// seedSession records a session and returns the stored row.
func seedSession(t *testing.T, store *storage.Store, session storage.Session) storage.Session {
	t.Helper()
	if session.State == "" {
		session.State = StateResetTimeReached
	}
	if session.Type == "" {
		session.Type = storage.SessionTypeHeadless
	}
	if _, err := store.UpsertSession(context.Background(), session); err != nil {
		t.Fatalf("seed session %s: %v", session.ID, err)
	}
	stored, err := store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("read back session %s: %v", session.ID, err)
	}
	return stored
}

// recoverableSession is the shape of a session that just hit its limit.
func recoverableSession(id string) storage.Session {
	return storage.Session{
		ID:              id,
		ProjectPath:     "/home/u/project",
		TranscriptPath:  "/home/u/.claude/projects/p/" + id + ".jsonl",
		State:           StateResetTimeReached,
		Enabled:         true,
		Recoverable:     true,
		TranscriptSize:  200,
		TranscriptLines: 10,
	}
}

// testManager builds a Manager with deterministic clock, backoff and a verifier
// that checks once instead of waiting.
func testManager(t *testing.T, store Store, provider claude.SessionProvider, mutate func(*Config)) *Manager {
	t.Helper()
	cfg := Config{
		Store:               store,
		Provider:            provider,
		Clock:               clock.NewFake(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)),
		Enabled:             true,
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
		VerificationTimeout: -1,
		HealthCheckInterval: -1,
		Backoff:             Backoff{Initial: time.Second, Max: time.Minute, Multiplier: 2, Jitter: 0},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	manager, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return manager
}

func TestNewRejectsAnInvalidBackoffPolicy(t *testing.T) {
	// The policy is validated as supplied, not after defaulting, so New is the
	// place a misconfigured retry policy is reported instead of repaired.
	bad := Config{
		Store:    newTestStore(t),
		Provider: &providerStub{},
		Backoff:  Backoff{Initial: 10 * time.Minute, Max: time.Minute, Multiplier: 0.5, Jitter: -1},
	}
	manager, err := New(bad)
	if manager != nil {
		t.Fatal("New returned a manager for an invalid policy")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("New error = %v, want it to wrap ErrInvalidConfig", err)
	}
	if !strings.Contains(err.Error(), "multiplier 0.5") {
		t.Errorf("New error = %q, want it to name the offending field", err)
	}

	// Each rejection reaches the caller through New, not just Validate.
	for _, policy := range []Backoff{
		{Initial: 10 * time.Minute, Max: time.Minute, Multiplier: 2},
		{Initial: -time.Second},
		{Jitter: 1},
	} {
		cfg := Config{Store: newTestStore(t), Provider: &providerStub{}, Backoff: policy}
		if _, err := New(cfg); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("New with Backoff%+v = %v, want it to wrap ErrInvalidConfig", policy, err)
		}
	}

	// A policy the caller set is honoured as given, and a valid one passes.
	manager = testManager(t, newTestStore(t), &providerStub{}, nil)
	if manager.backoff.Initial != time.Second || manager.backoff.Max != time.Minute {
		t.Errorf("backoff = %+v, want the policy the test supplied", manager.backoff)
	}
}

func TestNewRequiresDependencies(t *testing.T) {
	if _, err := New(Config{Provider: &providerStub{}}); !errors.Is(err, ErrNoStore) {
		t.Errorf("New without a store = %v, want ErrNoStore", err)
	}
	if _, err := New(Config{Store: newTestStore(t)}); !errors.Is(err, ErrNoProvider) {
		t.Errorf("New without a provider = %v, want ErrNoProvider", err)
	}
}

func TestNewAppliesDocumentedDefaults(t *testing.T) {
	store := newTestStore(t)
	manager, err := New(Config{Store: store, Provider: &providerStub{}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if manager.Enabled() {
		t.Error("Enabled() = true, want false: automatic recovery must be opted into")
	}
	if manager.MaxAttempts() != DefaultMaxAttempts {
		t.Errorf("MaxAttempts() = %d, want %d", manager.MaxAttempts(), DefaultMaxAttempts)
	}
	if manager.ContinuationMessage() != DefaultContinuationMessage {
		t.Errorf("ContinuationMessage() = %q, want the default message", manager.ContinuationMessage())
	}
	backoff := manager.Backoff()
	if backoff.Initial != DefaultInitialDelay || backoff.Max != DefaultMaxDelay ||
		backoff.Multiplier != DefaultMultiplier || backoff.Jitter != DefaultJitter {
		t.Errorf("Backoff() = %+v, want the documented policy", backoff)
	}
	if got := cap(manager.sem); got != DefaultMaxConcurrent {
		t.Errorf("concurrency budget = %d, want %d", got, DefaultMaxConcurrent)
	}
	if manager.log == nil || manager.clock == nil {
		t.Error("logger and clock must always be set")
	}
}

func TestNewHonoursOverridesAndFillsTheVerifier(t *testing.T) {
	store := newTestStore(t)
	provider := &providerStub{}
	override := Verifier{Timeout: -1, Interval: -1}
	manager, err := New(Config{
		Store:               store,
		Provider:            provider,
		Enabled:             true,
		MaxAttempts:         4,
		MaxConcurrent:       2,
		ContinuationMessage: "keep going",
		Backoff:             Backoff{Initial: 2 * time.Second, Max: time.Minute, Multiplier: 2},
		Verifier:            &override,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !manager.Enabled() || manager.MaxAttempts() != 4 {
		t.Errorf("Enabled/MaxAttempts = %v/%d, want true/4", manager.Enabled(), manager.MaxAttempts())
	}
	if manager.ContinuationMessage() != "keep going" {
		t.Errorf("ContinuationMessage = %q, want the override", manager.ContinuationMessage())
	}
	if got := cap(manager.sem); got != 2 {
		t.Errorf("concurrency budget = %d, want 2", got)
	}
	if manager.verifier.Provider != provider {
		t.Error("the verifier must fall back to the manager's provider")
	}
	if manager.verifier.Clock == nil {
		t.Error("the verifier must fall back to the manager's clock")
	}
}

func TestFromConfig(t *testing.T) {
	if _, err := FromConfig(nil, newTestStore(t), &providerStub{}, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("FromConfig(nil) = %v, want ErrInvalidConfig", err)
	}

	cfg := &config.Config{}
	cfg.Recovery = config.Recovery{
		Enabled:                 true,
		MaxAttempts:             3,
		InitialDelay:            config.NewDuration(5 * time.Second),
		MaxDelay:                config.NewDuration(time.Minute),
		Multiplier:              2,
		Jitter:                  0.1,
		VerificationTimeout:     config.NewDuration(90 * time.Second),
		HealthCheckInterval:     config.NewDuration(5 * time.Second),
		MaxConcurrentRecoveries: 2,
	}
	cfg.Claude = config.Claude{ContinuationMessage: "carry on"}

	manager, err := FromConfig(cfg, newTestStore(t), &providerStub{}, clock.System())
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	if !manager.Enabled() || manager.MaxAttempts() != 3 {
		t.Errorf("Enabled/MaxAttempts = %v/%d, want true/3", manager.Enabled(), manager.MaxAttempts())
	}
	if manager.ContinuationMessage() != "carry on" {
		t.Errorf("ContinuationMessage = %q, want %q", manager.ContinuationMessage(), "carry on")
	}
	if manager.Backoff().Initial != 5*time.Second {
		t.Errorf("Backoff().Initial = %s, want 5s", manager.Backoff().Initial)
	}
	if manager.verifier.Timeout != 90*time.Second || manager.verifier.Interval != 5*time.Second {
		t.Errorf("verifier = %+v, want the configured timeout and interval", manager.verifier)
	}
	if got := cap(manager.sem); got != 2 {
		t.Errorf("concurrency budget = %d, want 2", got)
	}
}

func TestRecoverRequestGuards(t *testing.T) {
	store := newTestStore(t)
	provider := &providerStub{}
	manager := testManager(t, store, provider, nil)

	if _, err := manager.RecoverRequest(context.Background(), Request{}); !errors.Is(err, ErrNoSession) {
		t.Errorf("empty session id = %v, want ErrNoSession", err)
	}
	//nolint:staticcheck // a nil context is exactly what is being rejected here.
	if _, err := manager.RecoverRequest(nil, Request{SessionID: sessionA}); err == nil ||
		!strings.Contains(err.Error(), "nil context") {
		t.Errorf("nil context = %v, want a nil-context error", err)
	}
	if _, err := manager.RecoverRequest(context.Background(), Request{SessionID: "not-a-uuid"}); !errors.Is(err, claude.ErrInvalidSessionID) {
		t.Errorf("invalid session id = %v, want ErrInvalidSessionID", err)
	}
	if _, err := manager.RecoverRequest(context.Background(), Request{SessionID: sessionA}); !errors.Is(err, ErrNotEligible) {
		t.Errorf("unknown session = %v, want ErrNotEligible", err)
	}

	disabled := testManager(t, store, provider, func(cfg *Config) { cfg.Enabled = false })
	outcome, err := disabled.Recover(context.Background(), sessionA)
	if !errors.Is(err, ErrDisabled) {
		t.Errorf("disabled manager = %v, want ErrDisabled", err)
	}
	if outcome.SessionID != sessionA {
		t.Errorf("outcome session = %q, want %q", outcome.SessionID, sessionA)
	}
	if provider.statusCount() != 0 || provider.resumeCount() != 0 {
		t.Error("a disabled manager must not touch the provider")
	}
}

func TestRecoverResumesAndProvesTheConversationContinued(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	provider := growthProvider(10, 12)

	manager := testManager(t, store, provider, nil)
	outcome, err := manager.Recover(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if outcome.State != StateResumed || !outcome.Resumed {
		t.Errorf("outcome = %s resumed=%v, want RESUMED/true", outcome.State, outcome.Resumed)
	}
	if !outcome.Verification.Proven {
		t.Errorf("verification = %+v, want a proven resume", outcome.Verification)
	}
	if outcome.Attempt.Success == nil || !*outcome.Attempt.Success {
		t.Errorf("attempt success = %v, want true", outcome.Attempt.Success)
	}
	if outcome.Attempt.ID == 0 || outcome.Attempt.AttemptNumber != 1 {
		t.Errorf("attempt = %+v, want the first persisted attempt", outcome.Attempt)
	}
	if provider.resumeCount() != 1 {
		t.Errorf("resume calls = %d, want exactly one", provider.resumeCount())
	}

	stored, err := store.GetSession(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if stored.State != StateResumed {
		t.Errorf("persisted state = %s, want RESUMED", stored.State)
	}
	if stored.LastError != "" {
		t.Errorf("LastError = %q, want it cleared after success", stored.LastError)
	}
	if stored.TranscriptLines != 12 {
		t.Errorf("TranscriptLines = %d, want the post-resume measurement", stored.TranscriptLines)
	}

	history, err := manager.History(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history.Attempts) != 1 {
		t.Errorf("attempt history = %d rows, want 1", len(history.Attempts))
	}
	wantStates := []State{StateCheckingSession, StateCheckingLimit, StateResuming, StateResumed}
	if len(history.Transitions) < len(wantStates) {
		t.Fatalf("transitions = %d, want at least %d", len(history.Transitions), len(wantStates))
	}
	for i, want := range wantStates {
		if history.Transitions[i].ToState != want {
			t.Errorf("transition %d = %s, want %s", i, history.Transitions[i].ToState, want)
		}
	}
	if history.State != StateResumed {
		t.Errorf("history state = %s, want RESUMED", history.State)
	}
}

func TestRecoverUsesTheRequestMessageAndLimit(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	provider := growthProvider(10, 11)
	manager := testManager(t, store, provider, nil)

	limit, err := store.RecordUsageLimit(context.Background(), storage.UsageLimit{
		SessionID:  sessionA,
		LimitType:  storage.LimitTypeFiveHour,
		RawMessage: "You've hit your 5-hour limit · resets 12pm (Europe/Berlin)",
	})
	if err != nil {
		t.Fatalf("record usage limit: %v", err)
	}

	outcome, err := manager.RecoverRequest(context.Background(), Request{
		SessionID:    sessionA,
		UsageLimitID: limit.ID,
		Message:      "pick up where you left off",
	})
	if err != nil {
		t.Fatalf("RecoverRequest: %v", err)
	}
	if outcome.Attempt.UsageLimitID != limit.ID {
		t.Errorf("attempt usage limit = %d, want %d", outcome.Attempt.UsageLimitID, limit.ID)
	}
	if len(provider.resumes) != 1 || provider.resumes[0].message != "pick up where you left off" {
		t.Errorf("resume calls = %+v, want the request message", provider.resumes)
	}
	if !strings.Contains(outcome.Attempt.Command, "continuation message") {
		t.Errorf("attempt command = %q, want a redacted summary", outcome.Attempt.Command)
	}
}

func TestRecoverRecognisesASessionThatAlreadyContinued(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	provider := &providerStub{statusFn: func(_ int, sessionID string) (claude.SessionStatus, error) {
		status := statusFor(sessionID, 14, 280)
		status.Running = true
		return status, nil
	}}
	manager := testManager(t, store, provider, nil)

	outcome, err := manager.Recover(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if outcome.State != StateResumed || !outcome.Resumed || !outcome.Verification.Proven {
		t.Fatalf("outcome = %+v, want an already-proven resume", outcome)
	}
	if provider.resumeCount() != 0 {
		t.Error("an already-working session must not be resumed again")
	}
	if !containsNote(outcome.Verification.Notes, "already continued on its own") {
		t.Errorf("notes = %v, want the already-resumed explanation", outcome.Verification.Notes)
	}
	stored, err := store.GetSession(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if stored.TranscriptLines != 14 {
		t.Errorf("TranscriptLines = %d, want the observed measurement", stored.TranscriptLines)
	}
}

func TestRecoverWaitsWhenTheLimitIsStillInForce(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	provider := growthProvider(10, 12)
	manager := testManager(t, store, provider, func(cfg *Config) {
		cfg.LimitRecheck = func(context.Context, string) (bool, error) { return false, nil }
	})

	outcome, err := manager.Recover(context.Background(), sessionA)
	if !errors.Is(err, ErrLimitNotCleared) {
		t.Fatalf("Recover error = %v, want ErrLimitNotCleared", err)
	}
	if outcome.State != StateWaitingForReset {
		t.Errorf("outcome state = %s, want WAITING_FOR_RESET", outcome.State)
	}
	if outcome.Resumed {
		t.Error("Resumed = true, want false")
	}
	if provider.resumeCount() != 0 {
		t.Error("a session whose limit is still active must not be resumed")
	}
}

func TestRecoverRetriesWhenTheLimitCheckFails(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	manager := testManager(t, store, growthProvider(10, 12), func(cfg *Config) {
		cfg.LimitRecheck = func(context.Context, string) (bool, error) {
			return false, errors.New("connection reset by peer")
		}
	})

	outcome, err := manager.Recover(context.Background(), sessionA)
	var retry *RetryError
	if !errors.As(err, &retry) {
		t.Fatalf("Recover error = %v, want a RetryError", err)
	}
	if outcome.State != StateRetryWait || outcome.Delay != retry.Delay {
		t.Errorf("outcome = %+v, want RETRY_WAIT with the same delay %s", outcome, retry.Delay)
	}
}

func TestRecoverRecordsAPermanentPreResumeFailureAsARetryWait(t *testing.T) {
	store := newTestStore(t)
	session := recoverableSession(sessionA)
	session.State = StateRetryWait
	seedSession(t, store, session)
	ctx := context.Background()

	// A permanently classified *status check* failure: no resume was issued, so
	// no attempt was spent and PERMANENTLY_FAILED would be a lie the state
	// machine rightly refuses.
	provider := &providerStub{statusFn: func(int, string) (claude.SessionStatus, error) {
		return claude.SessionStatus{}, errors.New("authentication_error: invalid api key")
	}}
	manager := testManager(t, store, provider, nil)

	outcome, err := manager.Recover(ctx, sessionA)
	var retry *RetryError
	if !errors.As(err, &retry) {
		t.Fatalf("Recover error = %v, want a RetryError: the session is retryable and must say so", err)
	}
	var terminal *terminalError
	if errors.As(err, &terminal) {
		t.Errorf("error = %v, want no terminalError: the session is not permanently failed", err)
	}
	if outcome.State != StateRetryWait || outcome.Delay != retry.Delay {
		t.Errorf("outcome = %+v, want RETRY_WAIT with the same delay %s", outcome, retry.Delay)
	}
	if retry.Attempt != 0 || retry.Max != DefaultMaxAttempts {
		t.Errorf("retry = %+v, want no attempt spent out of %d", retry, DefaultMaxAttempts)
	}
	if provider.resumeCount() != 0 {
		t.Error("a failed status check must never reach a resume")
	}

	// The failure has to be on disk, or nobody can explain what happened.
	transitions, err := store.ListTransitions(ctx, sessionA, 0)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	last := transitions[len(transitions)-1]
	if last.FromState != StateCheckingSession || last.ToState != StateRetryWait {
		t.Errorf("last transition = %s -> %s, want CHECKING_SESSION -> RETRY_WAIT",
			last.FromState, last.ToState)
	}
	for _, want := range []string{"pre-resume check failed", "permanent", "no attempt was spent"} {
		if !strings.Contains(last.Reason, want) {
			t.Errorf("transition reason = %q, want it to mention %q", last.Reason, want)
		}
	}
	stored, err := store.GetSession(ctx, sessionA)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if stored.State != StateRetryWait {
		t.Errorf("stored state = %s, want RETRY_WAIT", stored.State)
	}
	if !strings.Contains(stored.LastError, "invalid api key") {
		t.Errorf("stored LastError = %q, want the cause recorded", stored.LastError)
	}
	attempts, err := store.ListAttempts(ctx, sessionA, 0)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 0 {
		t.Errorf("attempts = %d, want 0: a status check failure is not a resume attempt", len(attempts))
	}
}

func TestRecoverRecordsAFailedPreResumeCheckWithTheBudgetSpent(t *testing.T) {
	store := newTestStore(t)
	session := recoverableSession(sessionA)
	session.State = StateRetryWait
	seedSession(t, store, session)

	ctx := context.Background()
	for i := 0; i < DefaultMaxAttempts; i++ {
		if _, err := store.StartAttempt(ctx, sessionA, 0, "claude -p --resume"); err != nil {
			t.Fatalf("start attempt %d: %v", i+1, err)
		}
	}

	provider := &providerStub{statusFn: func(int, string) (claude.SessionStatus, error) {
		return claude.SessionStatus{}, errors.New("claude timed out")
	}}
	manager := testManager(t, store, provider, nil)

	// The budget is spent, so the run has to be forced to reach the check at all.
	outcome, err := manager.RecoverRequest(ctx, Request{SessionID: sessionA, Force: true})
	var retry *RetryError
	if !errors.As(err, &retry) {
		t.Fatalf("Recover error = %v, want a RetryError, not a rejected transition", err)
	}
	if outcome.State != StateRetryWait {
		t.Errorf("outcome state = %s, want RETRY_WAIT", outcome.State)
	}
	transitions, err := store.ListTransitions(ctx, sessionA, 0)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	last := transitions[len(transitions)-1]
	if last.FromState != StateCheckingSession || last.ToState != StateRetryWait {
		t.Errorf("last transition = %s -> %s, want CHECKING_SESSION -> RETRY_WAIT",
			last.FromState, last.ToState)
	}
}

func TestRecoverRecordsAFailedLimitCheckAsARetryWait(t *testing.T) {
	store := newTestStore(t)
	session := recoverableSession(sessionA)
	session.State = StateRetryWait
	seedSession(t, store, session)
	ctx := context.Background()

	manager := testManager(t, store, growthProvider(10, 12), func(cfg *Config) {
		cfg.LimitRecheck = func(context.Context, string) (bool, error) {
			return false, errors.New("invalid api key")
		}
	})

	outcome, err := manager.Recover(ctx, sessionA)
	var retry *RetryError
	if !errors.As(err, &retry) {
		t.Fatalf("Recover error = %v, want a RetryError, not a rejected transition", err)
	}
	if outcome.State != StateRetryWait {
		t.Errorf("outcome state = %s, want RETRY_WAIT", outcome.State)
	}
	transitions, err := store.ListTransitions(ctx, sessionA, 0)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	last := transitions[len(transitions)-1]
	if last.FromState != StateCheckingLimit || last.ToState != StateRetryWait {
		t.Errorf("last transition = %s -> %s, want CHECKING_LIMIT -> RETRY_WAIT",
			last.FromState, last.ToState)
	}
}

func TestRecoverSchedulesARetryAfterATransientFailure(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	provider := growthProvider(10, 12)
	provider.resumeFn = func(string, string) (claude.ResumeResult, error) {
		return claude.ResumeResult{}, errors.New("dial tcp: connection reset by peer")
	}
	manager := testManager(t, store, provider, nil)

	outcome, err := manager.Recover(context.Background(), sessionA)
	var retry *RetryError
	if !errors.As(err, &retry) {
		t.Fatalf("Recover error = %v, want a RetryError", err)
	}
	if retry.Attempt != 1 || retry.Max != DefaultMaxAttempts {
		t.Errorf("retry = %d/%d, want 1/%d", retry.Attempt, retry.Max, DefaultMaxAttempts)
	}
	if retry.Delay != time.Second {
		t.Errorf("retry delay = %s, want the first backoff step", retry.Delay)
	}
	if outcome.State != StateRetryWait || outcome.Delay != time.Second {
		t.Errorf("outcome = %+v, want RETRY_WAIT in %s", outcome, time.Second)
	}
	if !strings.Contains(retry.Error(), "retryable") {
		t.Errorf("error %q does not name the classification", retry.Error())
	}

	stored, err := store.GetSession(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if stored.State != StateRetryWait || stored.LastError == "" {
		t.Errorf("persisted session = %s/%q, want RETRY_WAIT with the error recorded", stored.State, stored.LastError)
	}
}

func TestRecoverStopsOnANonRetryableFailure(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	provider := growthProvider(10, 12)
	provider.resumeFn = func(string, string) (claude.ResumeResult, error) {
		return claude.ResumeResult{}, claude.ErrCLIUnavailable
	}
	manager := testManager(t, store, provider, nil)

	outcome, err := manager.Recover(context.Background(), sessionA)
	if !errors.Is(err, claude.ErrCLIUnavailable) {
		t.Fatalf("Recover error = %v, want the CLI error", err)
	}
	if outcome.State != StatePermanentlyFailed {
		t.Errorf("outcome state = %s, want PERMANENTLY_FAILED", outcome.State)
	}
	var terminal *terminalError
	if !errors.As(err, &terminal) || terminal.State != StatePermanentlyFailed {
		t.Errorf("error = %v, want a terminalError in PERMANENTLY_FAILED", err)
	}

	stored, err := store.GetSession(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if stored.State != StatePermanentlyFailed {
		t.Errorf("persisted state = %s, want PERMANENTLY_FAILED", stored.State)
	}
	attempts, err := store.ListAttempts(context.Background(), sessionA, 0)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Success == nil || *attempts[0].Success {
		t.Errorf("attempts = %+v, want one recorded failure", attempts)
	}
	if attempts[0].FinishedAt.IsZero() {
		t.Error("the failed attempt must be finished, not left running")
	}
}

func TestRecoverTreatsAnErrorResultAsAFailure(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	provider := growthProvider(10, 12)
	provider.resumeFn = func(sessionID, _ string) (claude.ResumeResult, error) {
		return claude.ResumeResult{RequestedSessionID: sessionID, IsError: true, Result: "still limited\nsecond line"}, nil
	}
	manager := testManager(t, store, provider, nil)

	outcome, err := manager.Recover(context.Background(), sessionA)
	if err == nil {
		t.Fatal("Recover = nil error, want the resume failure")
	}
	if !errors.Is(err, claude.ErrResumeFailed) {
		t.Errorf("error = %v, want ErrResumeFailed", err)
	}
	if outcome.State != StateRetryWait {
		t.Errorf("outcome state = %s, want RETRY_WAIT", outcome.State)
	}
}

func TestRecoverMarksASessionUnrecoverable(t *testing.T) {
	t.Run("flagged by the store", func(t *testing.T) {
		store := newTestStore(t)
		session := recoverableSession(sessionA)
		session.Recoverable = false
		session.Reason = "the session type cannot be resumed"
		seedSession(t, store, session)
		provider := &providerStub{}
		manager := testManager(t, store, provider, nil)

		outcome, err := manager.Recover(context.Background(), sessionA)
		if !errors.Is(err, ErrUnrecoverable) {
			t.Fatalf("Recover error = %v, want ErrUnrecoverable", err)
		}
		if outcome.State != StateUnrecoverable {
			t.Errorf("outcome state = %s, want UNRECOVERABLE", outcome.State)
		}
		if provider.statusCount() != 0 {
			t.Error("an unrecoverable session must not be checked")
		}
	})

	t.Run("flagged by the provider", func(t *testing.T) {
		store := newTestStore(t)
		seedSession(t, store, recoverableSession(sessionA))
		provider := &providerStub{statusFn: func(_ int, sessionID string) (claude.SessionStatus, error) {
			return claude.SessionStatus{
				Session: claude.Session{ID: sessionID, Recoverable: false, Reason: "no resumable transcript"},
			}, nil
		}}
		manager := testManager(t, store, provider, nil)

		outcome, err := manager.Recover(context.Background(), sessionA)
		if !errors.Is(err, ErrUnrecoverable) {
			t.Fatalf("Recover error = %v, want ErrUnrecoverable", err)
		}
		if outcome.State != StateUnrecoverable {
			t.Errorf("outcome state = %s, want UNRECOVERABLE", outcome.State)
		}
		if provider.resumeCount() != 0 {
			t.Error("an unrecoverable session must not be resumed")
		}
	})
}

func TestRecoverRecordsADisabledSessionWithoutWork(t *testing.T) {
	store := newTestStore(t)
	session := recoverableSession(sessionA)
	session.Enabled = false
	seedSession(t, store, session)
	provider := &providerStub{}
	manager := testManager(t, store, provider, nil)

	outcome, err := manager.Recover(context.Background(), sessionA)
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("Recover error = %v, want ErrDisabled", err)
	}
	if outcome.State != StateDisabled {
		t.Errorf("outcome state = %s, want DISABLED", outcome.State)
	}
	stored, err := store.GetSession(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if stored.State != StateDisabled {
		t.Errorf("persisted state = %s, want DISABLED", stored.State)
	}
	if provider.statusCount() != 0 {
		t.Error("a disabled session must not be checked")
	}
}

func TestRecoverRefusesSettledSessions(t *testing.T) {
	cases := []State{StateCompleted, StateUserTerminated, StateUnrecoverable}
	for _, state := range cases {
		t.Run(string(state), func(t *testing.T) {
			store := newTestStore(t)
			session := recoverableSession(sessionA)
			session.State = state
			seedSession(t, store, session)
			manager := testManager(t, store, &providerStub{}, nil)

			outcome, err := manager.Recover(context.Background(), sessionA)
			if !errors.Is(err, ErrNotEligible) {
				t.Fatalf("Recover error = %v, want ErrNotEligible", err)
			}
			if outcome.State != state {
				t.Errorf("outcome state = %s, want it unchanged as %s", outcome.State, state)
			}
		})
	}
}

func TestRecoverReportsAnAlreadyResumedSession(t *testing.T) {
	store := newTestStore(t)
	session := recoverableSession(sessionA)
	session.State = StateResumed
	seedSession(t, store, session)
	manager := testManager(t, store, &providerStub{}, nil)

	outcome, err := manager.Recover(context.Background(), sessionA)
	if !errors.Is(err, ErrNotEligible) {
		t.Fatalf("Recover error = %v, want ErrNotEligible", err)
	}
	if !outcome.Resumed || outcome.State != StateResumed {
		t.Errorf("outcome = %+v, want it to report the existing resume", outcome)
	}
}

// exhaustBudget records n finished, failed attempts for a session.
func exhaustBudget(t *testing.T, store *storage.Store, sessionID string, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		attempt, err := store.StartAttempt(ctx, sessionID, 0, "claude -p --resume")
		if err != nil {
			t.Fatalf("start attempt %d: %v", i+1, err)
		}
		if _, err := store.FinishAttempt(ctx, attempt.ID, storage.AttemptResult{
			State:      StateRetryWait,
			Success:    false,
			Error:      "previous failure",
			FinishedAt: time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
		}); err != nil {
			t.Fatalf("finish attempt %d: %v", i+1, err)
		}
	}
}

func TestRecoverRecordsAnExhaustedBudgetFromEveryLiveState(t *testing.T) {
	// The budget guard has to be able to write PERMANENTLY_FAILED from wherever
	// the session actually stands when the budget runs out: the scheduler parks a
	// session in WAITING_FOR_RESET while the limit is still in force, and a crash
	// mid-check leaves it in CHECKING_SESSION. Asking for an edge the machine does
	// not define loses both the terminal state and the reason for it, and reports
	// ErrMaxAttempts as an ordinary failure.
	ctx := context.Background()
	states := []State{
		StateDiscovered,
		StateRunning,
		StateUsageLimitDetected,
		StateWaitingForReset,
		StateResetTimeReached,
		StateCheckingSession,
		StateCheckingLimit,
		StateResuming,
		StateRetryWait,
	}
	for _, from := range states {
		t.Run(string(from), func(t *testing.T) {
			store := newTestStore(t)
			session := recoverableSession(sessionA)
			session.State = from
			seedSession(t, store, session)
			exhaustBudget(t, store, sessionA, DefaultMaxAttempts)

			manager := testManager(t, store, growthProvider(10, 12), nil)
			outcome, err := manager.Recover(ctx, sessionA)
			if !errors.Is(err, ErrMaxAttempts) {
				t.Fatalf("Recover error = %v, want it to satisfy errors.Is(err, ErrMaxAttempts)", err)
			}
			var terminal *terminalError
			if !errors.As(err, &terminal) || terminal.State != StatePermanentlyFailed {
				t.Fatalf("error = %v (%T), want a terminal PERMANENTLY_FAILED error", err, err)
			}
			if outcome.State != StatePermanentlyFailed {
				t.Errorf("outcome state = %s, want PERMANENTLY_FAILED", outcome.State)
			}

			// The verdict must be on disk, not only in the returned error: that is
			// what stops the next daemon run from trying again.
			stored, readErr := store.GetSession(ctx, sessionA)
			if readErr != nil {
				t.Fatalf("read back session: %v", readErr)
			}
			if stored.State != StatePermanentlyFailed {
				t.Errorf("persisted state = %s, want PERMANENTLY_FAILED", stored.State)
			}
			// The persisted reason carries the budget; the returned error is the
			// sentinel notice, and both must agree the budget is what ran out.
			want := "maximum attempts reached (10)"
			if stored.LastError != want {
				t.Errorf("persisted error = %q, want %q", stored.LastError, want)
			}
			if !strings.Contains(err.Error(), "maximum attempts reached") {
				t.Errorf("error = %q, want it to mention the exhausted budget", err)
			}

			// And the move itself is in the history, from the state it really left.
			transitions, listErr := store.ListTransitions(ctx, sessionA, 0)
			if listErr != nil {
				t.Fatalf("list transitions: %v", listErr)
			}
			found := false
			for _, tr := range transitions {
				if tr.ToState == StatePermanentlyFailed && tr.FromState == from {
					found = true
					if tr.Reason != want {
						t.Errorf("transition reason = %q, want %q", tr.Reason, want)
					}
				}
			}
			if !found {
				t.Errorf("no recorded transition %s -> PERMANENTLY_FAILED in %+v", from, transitions)
			}
		})
	}
}

func TestRecoverHonoursTheAttemptBudgetAndForce(t *testing.T) {
	store := newTestStore(t)
	session := recoverableSession(sessionA)
	session.State = StateRetryWait
	seedSession(t, store, session)

	ctx := context.Background()
	for i := 0; i < DefaultMaxAttempts; i++ {
		attempt, err := store.StartAttempt(ctx, sessionA, 0, "claude -p --resume")
		if err != nil {
			t.Fatalf("start attempt %d: %v", i+1, err)
		}
		if _, err := store.FinishAttempt(ctx, attempt.ID, storage.AttemptResult{
			State:      StateRetryWait,
			Success:    false,
			Error:      "previous failure",
			FinishedAt: time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
		}); err != nil {
			t.Fatalf("finish attempt %d: %v", i+1, err)
		}
	}

	manager := testManager(t, store, growthProvider(10, 12), nil)
	outcome, err := manager.Recover(ctx, sessionA)
	if !errors.Is(err, ErrMaxAttempts) {
		t.Fatalf("Recover error = %v, want ErrMaxAttempts", err)
	}
	if outcome.State != StatePermanentlyFailed {
		t.Errorf("outcome state = %s, want PERMANENTLY_FAILED", outcome.State)
	}
	var terminal *terminalError
	if !errors.As(err, &terminal) || terminal.State != StatePermanentlyFailed {
		t.Errorf("error = %v, want a terminalError", err)
	}

	forced, err := manager.Retry(ctx, sessionA)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if forced.State != StateResumed || !forced.Resumed {
		t.Errorf("forced outcome = %+v, want a resume", forced)
	}
	if forced.Attempt.AttemptNumber != DefaultMaxAttempts+1 {
		t.Errorf("forced attempt number = %d, want %d", forced.Attempt.AttemptNumber, DefaultMaxAttempts+1)
	}
}

func TestRecoverRefusesPermanentlyFailedWithoutForce(t *testing.T) {
	store := newTestStore(t)
	session := recoverableSession(sessionA)
	session.State = StatePermanentlyFailed
	seedSession(t, store, session)
	provider := growthProvider(10, 12)
	manager := testManager(t, store, provider, nil)

	outcome, err := manager.Recover(context.Background(), sessionA)
	if !errors.Is(err, ErrNotEligible) {
		t.Fatalf("Recover error = %v, want ErrNotEligible", err)
	}
	if outcome.State != StatePermanentlyFailed {
		t.Errorf("outcome state = %s, want PERMANENTLY_FAILED", outcome.State)
	}
	if provider.statusCount() != 0 {
		t.Error("a spent session must not be checked without force")
	}

	if _, err := manager.Retry(context.Background(), sessionA); err != nil {
		t.Fatalf("Retry: %v", err)
	}
}

func TestRecoverPreventsDuplicateRecovery(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	provider := growthProvider(10, 12)
	manager := testManager(t, store, provider, nil)

	if !manager.locks.tryLock(sessionA) {
		t.Fatal("could not take the session lock")
	}
	defer manager.locks.unlock(sessionA)

	outcome, err := manager.Recover(context.Background(), sessionA)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Recover error = %v, want ErrAlreadyRunning", err)
	}
	if outcome.SessionID != sessionA {
		t.Errorf("outcome session = %q, want %q", outcome.SessionID, sessionA)
	}
	if provider.statusCount() != 0 || provider.resumeCount() != 0 {
		t.Error("a duplicate recovery must not touch the provider")
	}
}

func TestRecoverEnforcesTheConcurrencyLimit(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	seedSession(t, store, recoverableSession(sessionB))
	provider := growthProvider(10, 12)
	manager := testManager(t, store, provider, func(cfg *Config) { cfg.MaxConcurrent = 1 })

	// Hold the only slot, using a different session so the per-session lock is
	// not what rejects the call.
	manager.sem <- struct{}{}
	defer func() { <-manager.sem }()

	outcome, err := manager.Recover(context.Background(), sessionA)
	if !errors.Is(err, ErrConcurrencyLimit) {
		t.Fatalf("Recover error = %v, want ErrConcurrencyLimit", err)
	}
	if outcome.SessionID != sessionA {
		t.Errorf("outcome session = %q, want %q", outcome.SessionID, sessionA)
	}
	if provider.statusCount() != 0 {
		t.Error("a rejected recovery must not touch the provider")
	}
}

func TestRecoverClosesAnAttemptInterruptedByARestart(t *testing.T) {
	store := newTestStore(t)
	session := recoverableSession(sessionA)
	session.State = StateRetryWait
	seedSession(t, store, session)
	ctx := context.Background()

	interrupted, err := store.StartAttempt(ctx, sessionA, 0, "claude -p --resume")
	if err != nil {
		t.Fatalf("start attempt: %v", err)
	}

	manager := testManager(t, store, growthProvider(10, 12), nil)
	outcome, err := manager.Recover(ctx, sessionA)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if outcome.State != StateResumed {
		t.Fatalf("outcome state = %s, want RESUMED", outcome.State)
	}

	attempts, err := store.ListAttempts(ctx, sessionA, 0)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want the interrupted one plus the new one", len(attempts))
	}
	var closed storage.RecoveryAttempt
	for _, attempt := range attempts {
		if attempt.ID == interrupted.ID {
			closed = attempt
		}
	}
	if closed.FinishedAt.IsZero() {
		t.Error("the interrupted attempt must be closed, not left running")
	}
	if closed.Success == nil || *closed.Success {
		t.Errorf("interrupted attempt success = %v, want a recorded failure", closed.Success)
	}
	if !strings.Contains(closed.Error, "interrupted by a process restart") {
		t.Errorf("interrupted attempt error = %q, want the restart explanation", closed.Error)
	}

	transitions, err := store.ListTransitions(ctx, sessionA, 0)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	found := false
	for _, transition := range transitions {
		if transition.ToState == StateRetryWait && strings.Contains(transition.Reason, "interrupted") {
			found = true
		}
	}
	if !found {
		t.Errorf("transitions = %+v, want the restart recorded", transitions)
	}
}

func TestRecoverKeepsGoingAfterAnUnprovenResume(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	provider := growthProvider(10, 12)
	provider.resumeFn = func(_ string, _ string) (claude.ResumeResult, error) {
		return claude.ResumeResult{RequestedSessionID: sessionA, SessionID: sessionB, Verified: false}, nil
	}
	manager := testManager(t, store, provider, nil)

	outcome, err := manager.Recover(context.Background(), sessionA)
	if !errors.Is(err, ErrVerificationFailed) {
		t.Fatalf("Recover error = %v, want ErrVerificationFailed", err)
	}
	if outcome.Verification.Proven {
		t.Error("Proven = true, want false")
	}
	if outcome.State != StateRetryWait {
		t.Errorf("outcome state = %s, want RETRY_WAIT for a retryable verification failure", outcome.State)
	}
}

func TestRecoverRetriesWhenThePostResumeCheckFails(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	provider := &providerStub{statusFn: func(call int, sessionID string) (claude.SessionStatus, error) {
		if call == 1 {
			return statusFor(sessionID, 10, 200), nil
		}
		return claude.SessionStatus{}, errors.New("transcript vanished")
	}}
	manager := testManager(t, store, provider, nil)

	outcome, err := manager.Recover(context.Background(), sessionA)
	if err == nil {
		t.Fatal("Recover = nil error, want the verification failure")
	}
	if outcome.State != StateRetryWait {
		t.Errorf("outcome state = %s, want RETRY_WAIT", outcome.State)
	}
	attempts, listErr := store.ListAttempts(context.Background(), sessionA, 0)
	if listErr != nil {
		t.Fatalf("list attempts: %v", listErr)
	}
	if len(attempts) != 1 || attempts[0].FinishedAt.IsZero() {
		t.Errorf("attempts = %+v, want one finished attempt", attempts)
	}
	if attempts[0].SessionIDVerified == nil || !*attempts[0].SessionIDVerified {
		t.Errorf("session id verified = %v, want true: the CLI did report the right id", attempts[0].SessionIDVerified)
	}
	if attempts[0].TranscriptGrew == nil || *attempts[0].TranscriptGrew {
		t.Errorf("transcript grew = %v, want false: growth could not be measured", attempts[0].TranscriptGrew)
	}
}

func TestRecoverRetriesWhenThePreResumeCheckFails(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	provider := &providerStub{statusFn: func(int, string) (claude.SessionStatus, error) {
		return claude.SessionStatus{}, errors.New("claude timed out")
	}}
	manager := testManager(t, store, provider, nil)

	outcome, err := manager.Recover(context.Background(), sessionA)
	var retry *RetryError
	if !errors.As(err, &retry) {
		t.Fatalf("Recover error = %v, want a RetryError", err)
	}
	if outcome.State != StateRetryWait {
		t.Errorf("outcome state = %s, want RETRY_WAIT", outcome.State)
	}
	if retry.Attempt != 0 || retry.Delay != time.Second {
		t.Errorf("retry = %+v, want the first backoff step with no attempt spent", retry)
	}
}

func TestRecoverRecordsARepeatedWaitWithoutChangingState(t *testing.T) {
	store := newTestStore(t)
	session := recoverableSession(sessionA)
	session.State = StateCheckingSession
	seedSession(t, store, session)
	manager := testManager(t, store, growthProvider(10, 12), nil)

	outcome, err := manager.Recover(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if outcome.State != StateResumed {
		t.Errorf("outcome state = %s, want RESUMED", outcome.State)
	}
	transitions, err := store.ListTransitions(context.Background(), sessionA, 0)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	if len(transitions) == 0 || transitions[0].FromState != StateCheckingSession || transitions[0].ToState != StateCheckingSession {
		t.Errorf("first transition = %+v, want the same-state re-entry recorded", transitions)
	}
}

func TestPendingReturnsOnlySessionsThatWantWork(t *testing.T) {
	store := newTestStore(t)
	waiting := recoverableSession(sessionA)
	waiting.State = StateWaitingForReset
	seedSession(t, store, waiting)

	retrying := recoverableSession(sessionB)
	retrying.State = StateRetryWait
	seedSession(t, store, retrying)

	done := recoverableSession(sessionC)
	done.State = StateCompleted
	seedSession(t, store, done)

	disabledSession := recoverableSession("44444444-4444-4444-8444-444444444444")
	disabledSession.State = StateWaitingForReset
	disabledSession.Enabled = false
	seedSession(t, store, disabledSession)

	manager := testManager(t, store, &providerStub{}, nil)
	pending, err := manager.Pending(context.Background())
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("Pending returned %d sessions (%+v), want 2", len(pending), pending)
	}
	for _, session := range pending {
		if !Pending(session.State) {
			t.Errorf("session %s is in %s, which is not pending", session.ID, session.State)
		}
		if session.ID == done.ID || session.ID == disabledSession.ID {
			t.Errorf("session %s must not be pending", session.ID)
		}
	}
}

func TestHistoryGuards(t *testing.T) {
	store := newTestStore(t)
	manager := testManager(t, store, &providerStub{}, nil)

	if _, err := manager.History(context.Background(), "  "); !errors.Is(err, ErrNoSession) {
		t.Errorf("empty id = %v, want ErrNoSession", err)
	}
	if _, err := manager.History(context.Background(), sessionA); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("unknown session = %v, want storage.ErrNotFound", err)
	}
}

func TestTargetFor(t *testing.T) {
	cases := []struct {
		class   Class
		attempt int
		max     int
		want    State
	}{
		{ClassUnrecoverable, 1, 10, StateUnrecoverable},
		{ClassCanceled, 3, 10, StateRetryWait},
		{ClassPermanent, 1, 10, StatePermanentlyFailed},
		{ClassRetryable, 1, 10, StatePermanentlyFailed},
		{ClassRetryable, 10, 10, StatePermanentlyFailed},
	}
	for _, tc := range cases {
		if got := targetFor(tc.class, tc.attempt, tc.max); got != tc.want {
			t.Errorf("targetFor(%s, %d, %d) = %s, want %s", tc.class, tc.attempt, tc.max, got, tc.want)
		}
	}
}

func TestMergeBaselinePrefersTheFresherMeasurement(t *testing.T) {
	stored := Baseline{SessionID: sessionA, TranscriptPath: "/old", TranscriptSize: 100, TranscriptLines: 5}
	fresh := Baseline{SessionID: sessionB, TranscriptPath: "/new", TranscriptSize: 200, TranscriptLines: 9}
	merged := mergeBaseline(stored, fresh)
	if merged.SessionID != sessionB || merged.TranscriptPath != "/new" ||
		merged.TranscriptSize != 200 || merged.TranscriptLines != 9 {
		t.Errorf("mergeBaseline = %+v, want the fresh values", merged)
	}

	merged = mergeBaseline(stored, Baseline{})
	if merged.SessionID != sessionA || merged.TranscriptPath != "/old" ||
		merged.TranscriptSize != 100 || merged.TranscriptLines != 5 {
		t.Errorf("mergeBaseline = %+v, want the stored values kept", merged)
	}

	zero := mergeBaseline(Baseline{}, Baseline{SessionID: sessionB})
	if zero.SessionID != sessionB || zero.TranscriptLines != 0 || zero.TranscriptSize != 0 {
		t.Errorf("mergeBaseline = %+v, want only the identity filled in", zero)
	}
}

func TestGrew(t *testing.T) {
	baseline := Baseline{TranscriptLines: 5}
	if grew(baseline, statusFor(sessionA, 6, 0)) != true {
		t.Error("more lines must count as growth")
	}
	if grew(baseline, statusFor(sessionA, 5, 900)) {
		t.Error("an unchanged line count must not count here")
	}
	if grew(Baseline{}, statusFor(sessionA, 9, 900)) {
		t.Error("without a baseline nothing can be proven to have grown")
	}
}

func TestCommandPreviewAndFormattingHelpers(t *testing.T) {
	preview := commandPreview(sessionA, "continue please")
	if !strings.Contains(preview, "claude -p --resume "+sessionA) {
		t.Errorf("commandPreview = %q, want the resume command", preview)
	}
	if !strings.Contains(preview, "15-byte continuation message") {
		t.Errorf("commandPreview = %q, want the message summarised", preview)
	}
	if got := commandPreview(sessionA, "   "); !strings.Contains(got, "no message") {
		t.Errorf("commandPreview = %q, want the empty-message summary", got)
	}

	long := strings.Repeat("x", 5000)
	truncated := truncate(long)
	if !strings.HasSuffix(truncated, "…(truncated)") {
		t.Errorf("truncate did not mark the truncation: %q", truncated[len(truncated)-32:])
	}
	if len(truncated) > 2100 {
		t.Errorf("truncate returned %d bytes, want it capped", len(truncated))
	}
	if got := truncate("  short  "); got != "short" {
		t.Errorf("truncate = %q, want it trimmed", got)
	}

	if got := firstLine("\n\n  first  \nsecond\n"); got != "first" {
		t.Errorf("firstLine = %q, want the first non-empty line", got)
	}
	if got := firstLine("   \n\n"); got != "" {
		t.Errorf("firstLine = %q, want an empty string", got)
	}
	if got := firstLine(long); !strings.HasSuffix(got, "…(truncated)") {
		t.Error("firstLine must truncate a huge line")
	}

	value := boolPtr(true)
	if value == nil || !*value {
		t.Error("boolPtr did not return a pointer to true")
	}
	if got := boolPtr(false); got == nil || *got {
		t.Errorf("boolPtr(false) = %v, want a pointer to false", got)
	}
}

func TestSessionLocksAreReclaimed(t *testing.T) {
	locks := newSessionLocks()
	if !locks.tryLock(sessionA) {
		t.Fatal("the first lock must succeed")
	}
	if locks.held() != 1 {
		t.Errorf("held = %d, want 1", locks.held())
	}
	if locks.tryLock(sessionA) {
		t.Fatal("a second lock of the same session must fail")
	}
	locks.unlock(sessionA)
	if locks.held() != 0 {
		t.Errorf("held = %d, want the entry reclaimed", locks.held())
	}
	// Unlocking an unknown session must be harmless.
	locks.unlock(sessionB)

	if !locks.tryLock(sessionB) {
		t.Fatal("the lock must be reusable after release")
	}
	if !locks.tryLock(sessionA) {
		t.Fatal("different sessions must lock independently")
	}
	locks.unlock(sessionA)
	locks.unlock(sessionB)
	if locks.held() != 0 {
		t.Errorf("held = %d, want every entry reclaimed", locks.held())
	}
}
