package recovery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
)

// seedSpentAttempt records a finished attempt whose cost the CLI reported. It is
// the row a spend cap is summed from, so a test that wants a session at a cap
// seeds the spend rather than staging a resume that overspends.
func seedSpentAttempt(t *testing.T, store *storage.Store, sessionID string, cost float64) storage.RecoveryAttempt {
	t.Helper()
	ctx := context.Background()
	attempt, err := store.StartAttempt(ctx, sessionID, 0, "claude --bg --resume <id>")
	if err != nil {
		t.Fatalf("start attempt: %v", err)
	}
	finished, err := store.FinishAttempt(ctx, attempt.ID, storage.AttemptResult{
		State:      StateResumed,
		Success:    boolPtr(true),
		CostUSD:    &cost,
		FinishedAt: time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("finish attempt: %v", err)
	}
	return finished
}

// TestUnsetSpendCapsChangeNothing pins R1/R12's "off means today's behaviour":
// with both caps at zero, a session whose recorded spend dwarfs any plausible
// cap still resumes, and no sum is consulted to decide it.
func TestUnsetSpendCapsChangeNothing(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	seedSpentAttempt(t, store, sessionA, 100)

	manager := testManager(t, store, growthProvider(10, 12), nil)
	if manager.Limits().Configured() {
		t.Fatalf("Limits() = %+v, want both caps off by default", manager.Limits())
	}

	outcome, err := manager.Recover(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if outcome.State != StateResumed || !outcome.Resumed {
		t.Errorf("outcome = %s resumed=%v, want RESUMED/true", outcome.State, outcome.Resumed)
	}
	attempts, err := store.ListAttempts(context.Background(), sessionA, 0)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 2 {
		t.Errorf("attempts = %d, want the seeded one plus the resume", len(attempts))
	}
}

// TestSessionCapRefusesBeforeSpending is R5's contract: the sum is compared
// before StartAttempt, so a refusal spends nothing, and the outcome is named
// rather than generic (R7).
func TestSessionCapRefusesBeforeSpending(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	seedSpentAttempt(t, store, sessionA, 0.5)
	provider := growthProvider(10, 12)

	manager := testManager(t, store, provider, func(cfg *Config) {
		cfg.Limits = SpendLimits{PerSession: 0.5}
	})

	outcome, recoverErr := manager.Recover(context.Background(), sessionA)
	if recoverErr == nil {
		t.Fatalf("Recover err = nil, want the session cap refusal; outcome = %+v", outcome)
	}
	if !errors.Is(recoverErr, ErrSpendCapReached) {
		t.Errorf("error = %v, want it to wrap ErrSpendCapReached", recoverErr)
	}
	if class := Classify(recoverErr); class != ClassSpendCapReached {
		t.Errorf("class = %s, want %s", class, ClassSpendCapReached)
	}
	if outcome.State != StateDisabled {
		t.Errorf("outcome state = %s, want %s", outcome.State, StateDisabled)
	}

	// R5: a refusal spends nothing, so no attempt row was created.
	attempts, err := store.ListAttempts(context.Background(), sessionA, 0)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Errorf("attempts = %d, want only the seeded row: a refusal must spend nothing", len(attempts))
	}
	if provider.resumeCount() != 0 {
		t.Errorf("resume calls = %d, want 0", provider.resumeCount())
	}

	// R7: the text names the knob, the cap and the observed spend, because that
	// text is what reaches the operator through EventData.Reason.
	for _, want := range []string{"max_cost_usd_per_session", "$0.5", "1 attempt"} {
		if !strings.Contains(recoverErr.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", recoverErr.Error(), want)
		}
	}

	stored, err := store.GetSession(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if stored.State != StateDisabled {
		t.Errorf("persisted state = %s, want %s", stored.State, StateDisabled)
	}
}

// TestAttemptCapStopsRecoveryAfterTheAttempt is R6: one expensive turn stops
// automatic recovery, and the cost it judged is still recorded (R4).
func TestAttemptCapStopsRecoveryAfterTheAttempt(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	provider := growthProvider(10, 12)
	provider.resumeFn = func(sessionID, _ string) (claude.ResumeResult, error) {
		result := handoffResult(sessionID)
		cost := 0.42
		result.TotalCostUSD = &cost
		return result, nil
	}

	manager := testManager(t, store, provider, func(cfg *Config) {
		cfg.Limits = SpendLimits{PerAttempt: 0.1}
	})

	outcome, err := manager.Recover(context.Background(), sessionA)
	if err == nil {
		t.Fatalf("Recover err = nil, want the per-attempt cap stop; outcome = %+v", outcome)
	}
	if class := Classify(err); class != ClassSpendCapReached {
		t.Errorf("class = %s, want %s", class, ClassSpendCapReached)
	}
	if outcome.State != StateDisabled {
		t.Errorf("outcome state = %s, want %s", outcome.State, StateDisabled)
	}
	for _, want := range []string{"max_cost_usd_per_attempt", "$0.1", "$0.42"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err.Error(), want)
		}
	}

	attempts, err := store.ListAttempts(context.Background(), sessionA, 0)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].CostUSD == nil || *attempts[0].CostUSD != 0.42 {
		t.Errorf("recorded cost = %v, want 0.42 even though it stopped recovery", attempts[0].CostUSD)
	}
}

// TestUnknownCostDoesNotBlockResume is R10: an unreported cost is recorded as
// unknown, does not count toward either cap, and never stops a resume.
func TestUnknownCostDoesNotBlockResume(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	provider := growthProvider(10, 12) // the default handoff result reports no cost

	manager := testManager(t, store, provider, func(cfg *Config) {
		cfg.Limits = SpendLimits{PerSession: 0.01, PerAttempt: 0.01}
	})

	outcome, err := manager.Recover(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("Recover: %v, an unknown cost must not block a resume", err)
	}
	if outcome.State != StateResumed || !outcome.Resumed {
		t.Errorf("outcome = %s resumed=%v, want RESUMED/true", outcome.State, outcome.Resumed)
	}

	spend, err := manager.Spend(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("Spend: %v", err)
	}
	if spend.KnownUSD != 0 || spend.Known != 0 || spend.Unknown != 1 {
		t.Errorf("spend = %+v, want one unknown attempt and no known dollars", spend)
	}
}

// TestSpendStopIsNotRediscovered is R8: a spend stop leaves the session in a
// state outside PendingStates, so no reconcile pass resumes it.
//
// The stop is DISABLED rather than a state of its own: "nothing may be spent
// until a human decides" is what DISABLED means, and `ccresu enable` is the way
// back — one command, and no daemon stage that could resume a session whose cap
// it just refused.
func TestSpendStopIsNotRediscovered(t *testing.T) {
	store := newTestStore(t)
	session := recoverableSession(sessionA)
	session.State = StateDisabled
	seedSession(t, store, session)

	manager := testManager(t, store, growthProvider(10, 12), func(cfg *Config) {
		cfg.Limits = SpendLimits{PerSession: 1}
	})

	for _, state := range PendingStates() {
		if state == StateDisabled {
			t.Fatalf("PendingStates() contains %s, so a pass would resume a session at its cap", state)
		}
	}
	pending, err := manager.Pending(context.Background())
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	for _, candidate := range pending {
		if candidate.ID == sessionA {
			t.Errorf("Pending() returned %s, want it left parked at the cap", sessionA)
		}
	}
}

// TestManualRecoveryIsExemptFromTheSessionCap is R11: the cap exists to replace
// an absent human, so it must not bind one who is present.
func TestManualRecoveryIsExemptFromTheSessionCap(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	seedSpentAttempt(t, store, sessionA, 0.5)
	provider := growthProvider(10, 12)

	manager := testManager(t, store, provider, func(cfg *Config) {
		cfg.Limits = SpendLimits{PerSession: 0.5}
	})

	outcome, err := manager.RecoverRequest(context.Background(), Request{SessionID: sessionA, Manual: true})
	if err != nil {
		t.Fatalf("RecoverRequest: %v, a manual recovery must be allowed past the cap", err)
	}
	if outcome.State != StateResumed || !outcome.Resumed {
		t.Errorf("outcome = %s resumed=%v, want RESUMED/true", outcome.State, outcome.Resumed)
	}
	if provider.resumeCount() != 1 {
		t.Errorf("resume calls = %d, want 1", provider.resumeCount())
	}
	attempts, err := store.ListAttempts(context.Background(), sessionA, 0)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 2 {
		t.Errorf("attempts = %d, want the seeded row plus the manual one", len(attempts))
	}
}
