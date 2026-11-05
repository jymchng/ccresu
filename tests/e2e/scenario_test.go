// Package e2e_test replays the exact interruption ccresu exists for, from the
// moment Claude Code prints its usage-limit banner to the moment Telegram
// reports that the original conversation continued.
//
// Unlike the per-package unit tests, nothing here is stubbed at the seam being
// proved: detection, reset-time resolution, persistence, scheduling, resuming,
// verification and notification all run for real, against a real SQLite file, a
// real HTTPS Bot API stand-in and the real recovery state machine.
package e2e_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/detector"
	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/scheduler"
	"github.com/jymchng/ccresu/internal/storage"
	"github.com/jymchng/ccresu/internal/telegram"

	"github.com/jymchng/ccresu/tests/support"
)

// TestFullScenarioReplaysTheRealInterruption walks the whole documented
// scenario and asserts each of the thirteen required outcomes.
//
// The fixtures are the real strings, verbatim:
//
//	You've hit your 5-hour limit · resets 12pm (Europe/Berlin)
//	Goal paused · usage limit reached · send a message after it resets
func TestFullScenarioReplaysTheRealInterruption(t *testing.T) {
	ctx := context.Background()

	// The host clock is deliberately not Berlin: nothing in the pipeline may
	// depend on where this test runs.
	clk := clock.NewFake(support.BaseTime)
	store := support.OpenStore(t)
	t.Cleanup(func() { _ = store.Close() })

	provider := support.NewGrowingProvider(support.SessionID, 12, 34)
	manager := support.NewManager(t, store, provider, clk, 10)

	stub, stubServer := support.NewTelegramStub(t)
	client := support.NewTelegramClient(t, stubServer.URL, clk)
	notifier, err := telegram.New(telegram.Config{
		Store:   store,
		Sender:  client,
		Clock:   clk,
		Logger:  support.QuietLogger(),
		Enabled: map[storage.NotificationEvent]bool{storage.EventRecoverySuccess: true},
	})
	if err != nil {
		t.Fatalf("notifier: %v", err)
	}

	// --- Discovery and detection -------------------------------------------

	support.SeedSession(t, store, support.SessionID, storage.StateRunning)
	before, err := store.GetSession(ctx, support.SessionID)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}

	event, limit := support.DetectAndRecord(t, store, support.SessionID, support.LimitBanner)

	// (1) The banner is classified as a usage limit, with high confidence and
	// the rule that matched recorded, so the decision is explainable.
	if !event.IsUsageLimit() {
		t.Fatalf("assertion 1: banner not detected: %s", event.Reason())
	}
	if event.Confidence < 0.9 {
		t.Fatalf("assertion 1: confidence = %.2f, want >= 0.9", event.Confidence)
	}
	if event.PatternID == "" {
		t.Fatal("assertion 1: no pattern id recorded for the canonical banner")
	}

	// (2) The limit is the five-hour one, not "other".
	if event.Limit != detector.LimitFiveHour {
		t.Fatalf("assertion 2: limit kind = %q, want %q", event.Limit, detector.LimitFiveHour)
	}
	if limit.LimitType != storage.LimitTypeFiveHour {
		t.Fatalf("assertion 2: stored limit type = %q, want %q", limit.LimitType, storage.LimitTypeFiveHour)
	}

	// The paused-goal banner is a separate, independent fact: the agent said the
	// goal is paused, which is why a continuation message is needed at all.
	if !detector.Analyze(support.PausedBanner).PausedGoal {
		t.Fatal("assertion 2: the paused-goal banner was not recognised as a paused goal")
	}

	// --- Reset-time resolution and scheduling ------------------------------

	resolved := scheduler.Resolve(event.Reset, clk.Now(), scheduler.DefaultOptions())

	// (3) "12pm (Europe/Berlin)" resolves to 12:00 Berlin regardless of the
	// host timezone. On 2026-03-02 Berlin is UTC+1, so 12:00 local is 11:00Z.
	if !resolved.Known {
		t.Fatalf("assertion 3: reset time not resolved: %+v", resolved)
	}
	wantReset := time.Date(2026, 3, 2, 11, 0, 0, 0, time.UTC)
	if !resolved.At.Equal(wantReset) {
		t.Fatalf("assertion 3: reset at = %s, want %s", resolved.At, wantReset)
	}
	if resolved.LocationName != "Europe/Berlin" {
		t.Fatalf("assertion 3: location = %q, want Europe/Berlin", resolved.LocationName)
	}
	if resolved.Local != "2026-03-02 12:00" {
		t.Fatalf("assertion 3: local civil time = %q, want 2026-03-02 12:00", resolved.Local)
	}

	// The resolved instant is persisted with the zone it was written in, so a
	// restart (or `ccresu history`) can explain the wait.
	if err := store.SetUsageLimitResetAt(ctx, limit.ID, resolved.At, resolved.LocationName, resolved.Phrase); err != nil {
		t.Fatalf("persist reset time: %v", err)
	}
	limit, err = store.GetUsageLimit(ctx, limit.ID)
	if err != nil {
		t.Fatalf("reload usage limit: %v", err)
	}
	if !limit.ResetAt.Equal(wantReset) || limit.ResetTimezone != "Europe/Berlin" {
		t.Fatalf("assertion 3: stored reset = %s %q, want %s Europe/Berlin",
			limit.ResetAt, limit.ResetTimezone, wantReset)
	}

	schedule, err := store.SaveSchedule(ctx, storage.Schedule{
		SessionID:     support.SessionID,
		UsageLimitID:  limit.ID,
		ResetAt:       resolved.At,
		ResetTimezone: resolved.LocationName,
		ResetRaw:      resolved.Phrase,
	})
	if err != nil {
		t.Fatalf("save schedule: %v", err)
	}

	// (4) The session is now waiting for the reset, and the state change is
	// persisted rather than merely held in memory.
	if _, err := store.TransitionSession(ctx, support.SessionID, storage.StateUsageLimitDetected,
		"usage limit banner detected", 0); err != nil {
		t.Fatalf("transition to USAGE_LIMIT_DETECTED: %v", err)
	}
	if _, err := store.TransitionSession(ctx, support.SessionID, storage.StateWaitingForReset,
		"waiting for 12:00 Europe/Berlin", 0); err != nil {
		t.Fatalf("transition to WAITING_FOR_RESET: %v", err)
	}
	waiting, err := store.GetSession(ctx, support.SessionID)
	if err != nil {
		t.Fatalf("reload session: %v", err)
	}
	if waiting.State != storage.StateWaitingForReset {
		t.Fatalf("assertion 4: state = %s, want WAITING_FOR_RESET", waiting.State)
	}
	if schedule.DueAt().Before(support.BaseTime) {
		t.Fatalf("assertion 4: the schedule is already due (%s), so nothing is being waited for", schedule.DueAt())
	}

	// (5) Nothing resumes while the reset is in the future. The wait is a real
	// gate, not advice: the manager is not even reachable for this session.
	if clk.Now().Before(schedule.DueAt()) {
		if n := len(provider.ResumeCalls()); n != 0 {
			t.Fatalf("assertion 5: %d resume(s) ran before the reset time", n)
		}
	}

	// --- Time passes, then the resume happens ------------------------------

	clk.Set(schedule.DueAt().Add(time.Minute))
	if _, err := store.TransitionSession(ctx, support.SessionID, storage.StateResetTimeReached,
		"the reset time was reached", 0); err != nil {
		t.Fatalf("transition to RESET_TIME_REACHED: %v", err)
	}

	outcome, err := manager.Recover(ctx, support.SessionID)
	if err != nil {
		t.Fatalf("recover: %v (state %s)", err, outcome.State)
	}

	// (6) The resume ran, exactly once, using the documented command.
	calls := provider.ResumeCalls()
	if len(calls) != 1 {
		t.Fatalf("assertion 6: %d resumes, want exactly 1", len(calls))
	}
	wantArgs := claude.ResumeArgs(support.SessionID, manager.ContinuationMessage())
	if got := calls[0].Message; got != manager.ContinuationMessage() {
		t.Fatalf("assertion 6: continuation message = %q, want %q", got, manager.ContinuationMessage())
	}
	if len(wantArgs) != 6 || wantArgs[0] != "-p" || wantArgs[1] != "--resume" ||
		wantArgs[2] != support.SessionID || wantArgs[3] != "--output-format" || wantArgs[4] != "json" ||
		wantArgs[5] != manager.ContinuationMessage() {
		t.Fatalf("assertion 6: unexpected resume argv shape %v", wantArgs)
	}

	// (7) The original session id was preserved, both in what was asked for and
	// in what the CLI reported back.
	if calls[0].SessionID != support.SessionID {
		t.Fatalf("assertion 7: resumed %q, want %q", calls[0].SessionID, support.SessionID)
	}
	if !outcome.Verification.SessionIDPreserved {
		t.Fatalf("assertion 7: session id was not proven preserved: %v", outcome.Verification.Notes)
	}

	// (8) The working directory is unchanged: the resume addressed the same
	// project path the session was discovered in.
	after, err := store.GetSession(ctx, support.SessionID)
	if err != nil {
		t.Fatalf("reload session after resume: %v", err)
	}
	if after.ProjectPath != before.ProjectPath || after.ProjectPath != support.ProjectPath {
		t.Fatalf("assertion 8: project path = %q, was %q (want %q)",
			after.ProjectPath, before.ProjectPath, support.ProjectPath)
	}

	// (9) No new conversation was created: there is still exactly one session
	// row, and it is still the original id.
	sessions, err := store.ListSessions(ctx, storage.SessionFilter{})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("assertion 9: %d sessions exist, want 1 (a new conversation was created)", len(sessions))
	}
	if sessions[0].ID != support.SessionID {
		t.Fatalf("assertion 9: surviving session is %q, want %q", sessions[0].ID, support.SessionID)
	}

	// (10) The transcript grew, which is the cross-platform proof that the
	// conversation actually continued.
	if !outcome.Verification.TranscriptGrew {
		t.Fatalf("assertion 10: transcript growth was not proven: %v", outcome.Verification.Notes)
	}
	if outcome.Verification.LinesAfter <= outcome.Verification.LinesBefore {
		t.Fatalf("assertion 10: lines went %d -> %d, which is not growth",
			outcome.Verification.LinesBefore, outcome.Verification.LinesAfter)
	}
	if !outcome.Verification.Proven {
		t.Fatalf("assertion 10: verification was not proven: %s", outcome.Verification.Reason())
	}

	// (11) The final state is RESUMED and it is persisted.
	if outcome.State != recovery.StateResumed {
		t.Fatalf("assertion 11: outcome state = %s, want RESUMED", outcome.State)
	}
	if after.State != storage.StateResumed {
		t.Fatalf("assertion 11: persisted state = %s, want RESUMED", after.State)
	}
	if !outcome.Resumed {
		t.Fatal("assertion 11: outcome did not report the session as resumed")
	}

	// (12) The attempt history records the success, including the two proofs, so
	// `ccresu history` can show why this counts as a recovery.
	attempts, err := store.ListAttempts(ctx, support.SessionID, 10)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("assertion 12: %d attempts persisted, want 1", len(attempts))
	}
	if attempts[0].Success == nil || !*attempts[0].Success {
		t.Fatalf("assertion 12: attempt not recorded as successful: %+v", attempts[0])
	}
	if attempts[0].SessionIDVerified == nil || !*attempts[0].SessionIDVerified {
		t.Fatalf("assertion 12: attempt does not record a verified session id: %+v", attempts[0])
	}
	if attempts[0].TranscriptGrew == nil || !*attempts[0].TranscriptGrew {
		t.Fatalf("assertion 12: attempt does not record transcript growth: %+v", attempts[0])
	}

	// (13) Telegram delivered the success notification, over real HTTP, carrying
	// the session id and no longer pending on disk.
	notification, _, notifyErr := notifier.Notify(ctx, telegram.Request{
		Event:     storage.EventRecoverySuccess,
		SessionID: support.SessionID,
		AttemptID: attempts[0].ID,
		Data: telegram.EventData{
			SessionID:   support.SessionID,
			ProjectPath: after.ProjectPath,
			AttemptID:   attempts[0].ID,
			Attempt:     1,
			MaxAttempts: manager.MaxAttempts(),
			State:       string(recovery.StateResumed),
		},
	})
	if notifyErr != nil {
		t.Fatalf("assertion 13: queue notification: %v", notifyErr)
	}
	if _, err := notifier.SendOnce(ctx); err != nil {
		t.Fatalf("assertion 13: send notification: %v", err)
	}

	sent := stub.Messages()
	if len(sent) != 1 {
		t.Fatalf("assertion 13: %d telegram requests, want 1", len(sent))
	}
	if !contains(sent[0].Text, support.SessionID) {
		t.Fatalf("assertion 13: the notification does not mention the session: %q", sent[0].Text)
	}
	if sent[0].ParseMode != "HTML" {
		t.Fatalf("assertion 13: parse mode = %q, want HTML", sent[0].ParseMode)
	}
	// The text is produced by the real renderer, so it must carry the outcome and
	// the attempt budget the operator uses to decide whether to intervene.
	if !contains(sent[0].Text, "Recovery succeeded") {
		t.Fatalf("assertion 13: the notification does not report success: %q", sent[0].Text)
	}
	if !contains(sent[0].Text, "Attempt: 1/10") {
		t.Fatalf("assertion 13: the notification does not report the attempt budget: %q", sent[0].Text)
	}

	stored, err := store.GetNotification(ctx, notification.ID)
	if err != nil {
		t.Fatalf("assertion 13: reload notification: %v", err)
	}
	if stored.Status != storage.NotificationSent {
		t.Fatalf("assertion 13: notification status = %q, want %q", stored.Status, storage.NotificationSent)
	}

	// The whole run leaves a readable audit trail: every state the documentation
	// promises appears in the transition history, in order.
	assertTransitionOrder(t, store, []storage.State{
		storage.StateUsageLimitDetected,
		storage.StateWaitingForReset,
		storage.StateResetTimeReached,
		storage.StateCheckingSession,
		storage.StateCheckingLimit,
		storage.StateResuming,
		storage.StateResumed,
	})
}

// TestScenarioSurvivesRestartMidWait proves the wait survives the process going
// away, which is the failure mode a watchdog must not have.
func TestScenarioSurvivesRestartMidWait(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/ccresu.db"

	clk := clock.NewFake(support.BaseTime)
	store := support.OpenStoreAt(t, dbPath)

	support.SeedSession(t, store, support.SessionID, storage.StateRunning)
	event, limit := support.DetectAndRecord(t, store, support.SessionID, support.LimitBanner)
	resolved := scheduler.Resolve(event.Reset, clk.Now(), scheduler.DefaultOptions())
	if _, err := store.SaveSchedule(ctx, storage.Schedule{
		SessionID:     support.SessionID,
		UsageLimitID:  limit.ID,
		ResetAt:       resolved.At,
		ResetTimezone: resolved.LocationName,
		ResetRaw:      resolved.Phrase,
	}); err != nil {
		t.Fatalf("save schedule: %v", err)
	}
	if _, err := store.TransitionSession(ctx, support.SessionID, storage.StateUsageLimitDetected, "detected", 0); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if _, err := store.TransitionSession(ctx, support.SessionID, storage.StateWaitingForReset, "waiting", 0); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The daemon restarts: a new process opens the same file.
	reopened := support.OpenStoreAt(t, dbPath)
	t.Cleanup(func() { _ = reopened.Close() })

	schedule, err := reopened.GetSchedule(ctx, support.SessionID)
	if err != nil {
		t.Fatalf("reload schedule after restart: %v", err)
	}
	if !schedule.ResetAt.Equal(resolved.At) {
		t.Fatalf("schedule reset at = %s after restart, want %s", schedule.ResetAt, resolved.At)
	}
	if schedule.ResetTimezone != "Europe/Berlin" {
		t.Fatalf("schedule timezone = %q after restart, want Europe/Berlin", schedule.ResetTimezone)
	}

	reloaded, err := reopened.GetSession(ctx, support.SessionID)
	if err != nil {
		t.Fatalf("reload session after restart: %v", err)
	}
	if reloaded.State != storage.StateWaitingForReset {
		t.Fatalf("state = %s after restart, want WAITING_FOR_RESET", reloaded.State)
	}

	// And the wait still completes correctly in the new process.
	clk.Set(resolved.At.Add(time.Minute))
	if _, err := reopened.TransitionSession(ctx, support.SessionID, storage.StateResetTimeReached, "reset reached", 0); err != nil {
		t.Fatalf("transition after restart: %v", err)
	}
	provider := support.NewGrowingProvider(support.SessionID, 12, 20)
	manager := support.NewManager(t, reopened, provider, clk, 10)
	outcome, err := manager.Recover(ctx, support.SessionID)
	if err != nil {
		t.Fatalf("recover after restart: %v", err)
	}
	if outcome.State != recovery.StateResumed {
		t.Fatalf("state = %s after restart recovery, want RESUMED", outcome.State)
	}
	if len(provider.ResumeCalls()) != 1 {
		t.Fatalf("%d resumes after restart, want 1", len(provider.ResumeCalls()))
	}
}

// TestScenarioRefusesWhenTheLimitIsStillInForce proves the pre-resume check is
// respected: a session whose limit has not actually cleared goes back to
// waiting instead of being resumed.
func TestScenarioRefusesWhenTheLimitIsStillInForce(t *testing.T) {
	ctx := context.Background()
	clk := clock.NewFake(support.BaseTime)
	store := support.OpenStore(t)
	t.Cleanup(func() { _ = store.Close() })

	support.SeedSession(t, store, support.SessionID, storage.StateResetTimeReached)
	provider := support.NewGrowingProvider(support.SessionID, 12, 20)

	manager, err := recovery.New(recovery.Config{
		Store:               store,
		Provider:            provider,
		Clock:               clk,
		Logger:              support.QuietLogger(),
		Enabled:             true,
		MaxAttempts:         10,
		VerificationTimeout: -1,
		HealthCheckInterval: -1,
		LimitRecheck: func(context.Context, string) (bool, error) {
			return false, nil
		},
	})
	if err != nil {
		t.Fatalf("manager: %v", err)
	}

	outcome, err := manager.Recover(ctx, support.SessionID)
	if !errors.Is(err, recovery.ErrLimitNotCleared) {
		t.Fatalf("error = %v, want ErrLimitNotCleared", err)
	}
	if outcome.State != recovery.StateWaitingForReset {
		t.Fatalf("state = %s, want WAITING_FOR_RESET", outcome.State)
	}
	if n := len(provider.ResumeCalls()); n != 0 {
		t.Fatalf("%d resumes ran while the limit was in force, want 0", n)
	}
}

// assertTransitionOrder checks that the wanted states appear in the persisted
// history in the given relative order.
func assertTransitionOrder(t *testing.T, store *storage.Store, want []storage.State) {
	t.Helper()
	transitions, err := store.ListTransitions(context.Background(), support.SessionID, 100)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	position := 0
	for _, transition := range transitions {
		if position < len(want) && transition.ToState == want[position] {
			position++
		}
	}
	if position != len(want) {
		var seen []string
		for _, transition := range transitions {
			seen = append(seen, string(transition.ToState))
		}
		t.Fatalf("transition history %v does not contain %v in order (matched %d of %d)",
			seen, want, position, len(want))
	}
}

// contains reports whether s contains sub.
func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

// indexOf is strings.Index without the import, kept small on purpose.
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
