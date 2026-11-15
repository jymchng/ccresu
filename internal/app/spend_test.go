package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/storage"
)

// seedCostedAttempt records one finished attempt with the cost the CLI reported,
// or with no cost at all when cost is nil.
func seedCostedAttempt(t *testing.T, h *harness, cost *float64) storage.RecoveryAttempt {
	t.Helper()
	ctx := context.Background()
	attempt, err := h.store.StartAttempt(ctx, canonicalSessionID, 0, "claude --bg --resume <id>")
	if err != nil {
		t.Fatalf("start attempt: %v", err)
	}
	finished, err := h.store.FinishAttempt(ctx, attempt.ID, storage.AttemptResult{
		State:      storage.StateResumed,
		Success:    successPtr(true),
		CostUSD:    cost,
		FinishedAt: time.Date(2026, 1, 5, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("finish attempt: %v", err)
	}
	return finished
}

// TestInspectShowsSpendAndTheConfiguredCaps is PRD 5's "surfaced in status": the
// figure the cap is compared against, the caps themselves, and what the latest
// attempt cost — including "unknown" for a cost the CLI never reported.
func TestInspectShowsSpendAndTheConfiguredCaps(t *testing.T) {
	h := newHarnessWith(t, recovery.SpendLimits{PerSession: 5, PerAttempt: 2})
	h.seedSession(t, canonicalSessionID, nil)
	cost := 0.42
	seedCostedAttempt(t, h, &cost)
	seedCostedAttempt(t, h, nil)

	code, stdout, stderr := h.exec(t, "inspect", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("inspect exit = %d (stderr %s)", code, stderr)
	}
	for _, want := range []string{
		"spend:",
		"$0.42 recorded",
		"2 attempt(s), 1 with no reported cost",
		"session cap $5",
		"attempt cap $2",
		"cost:",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("inspect output is missing %q:\n%s", want, stdout)
		}
	}

	code, stdout, stderr = h.exec(t, "inspect", canonicalSessionID, "--json")
	if code != ExitOK {
		t.Fatalf("inspect --json exit = %d (stderr %s)", code, stderr)
	}
	for _, want := range []string{`"spend"`, `"known_usd": 0.42`, `"unknown": 1`, `"session_cap_usd": 5`, `"attempt_cap_usd": 2`} {
		if !strings.Contains(stdout, want) {
			t.Errorf("inspect --json is missing %q:\n%s", want, stdout)
		}
	}
}

// TestInspectSaysACapIsOffRatherThanZero pins the distinction: an unset cap must
// not read as a cap of $0 that every attempt has already passed.
func TestInspectSaysACapIsOffRatherThanZero(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)

	code, stdout, stderr := h.exec(t, "inspect", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("inspect exit = %d (stderr %s)", code, stderr)
	}
	if !strings.Contains(stdout, "session cap off") || !strings.Contains(stdout, "attempt cap off") {
		t.Errorf("inspect output does not report the caps as off:\n%s", stdout)
	}
}

// TestHistoryShowsWhatEachAttemptCost is PRD 5's "surfaced in history": one row
// per attempt carries its cost, and an unreported cost reads as unknown.
func TestHistoryShowsWhatEachAttemptCost(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, nil)
	cost := 0.42
	seedCostedAttempt(t, h, &cost)
	seedCostedAttempt(t, h, nil)

	code, stdout, stderr := h.exec(t, "history", canonicalSessionID)
	if code != ExitOK {
		t.Fatalf("history exit = %d (stderr %s)", code, stderr)
	}
	if !strings.Contains(stdout, "COST") {
		t.Errorf("history has no COST column:\n%s", stdout)
	}
	if !strings.Contains(stdout, "$0.42") {
		t.Errorf("history does not show the recorded cost:\n%s", stdout)
	}
	if !strings.Contains(stdout, "unknown") {
		t.Errorf("history does not say unknown for an unreported cost:\n%s", stdout)
	}

	code, stdout, stderr = h.exec(t, "history", canonicalSessionID, "--json")
	if code != ExitOK {
		t.Fatalf("history --json exit = %d (stderr %s)", code, stderr)
	}
	if !strings.Contains(stdout, `"known_usd": 0.42`) || !strings.Contains(stdout, `"unknown": 1`) {
		t.Errorf("history --json is missing the spend summary:\n%s", stdout)
	}
}

// successPtr is the nullable `Success` column's pointer, local to these tests so
// the helper name cannot collide with a production symbol.
func successPtr(v bool) *bool { return &v }
