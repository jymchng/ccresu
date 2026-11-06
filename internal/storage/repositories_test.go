package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/storage"
)

func TestUpsertSessionSemantics(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	created, err := store.UpsertSession(ctx, storage.Session{
		ID: "semantics", ProjectPath: "/a", Type: storage.SessionTypeInteractive, Enabled: true,
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if !created {
		t.Fatal("first upsert must report created=true")
	}

	// Rediscovery must refresh discovery columns but never touch state/enabled.
	if _, err := store.TransitionSession(ctx, "semantics", storage.StateWaitingForReset, "limit", 0); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if err := store.SetSessionEnabled(ctx, "semantics", false); err != nil {
		t.Fatalf("disable: %v", err)
	}

	created, err = store.UpsertSession(ctx, storage.Session{
		ID: "semantics", ProjectPath: "/b", TranscriptPath: "/t.jsonl",
		Type: storage.SessionTypeHeadless, Enabled: true, Recoverable: false,
		Reason: "unsupported session type", PID: 55, PPID: 2, ProcessStartTime: 999,
		TranscriptSize: 2048, TranscriptLines: 40,
	})
	if err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if created {
		t.Fatal("second upsert must report created=false")
	}

	got, err := store.GetSession(ctx, "semantics")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ProjectPath != "/b" || got.TranscriptPath != "/t.jsonl" ||
		got.Type != storage.SessionTypeHeadless {
		t.Fatalf("discovery columns not refreshed: %+v", got)
	}
	if got.PID != 55 || got.PPID != 2 || got.ProcessStartTime != 999 {
		t.Fatalf("identity not refreshed: %+v", got)
	}
	if got.Recoverable || got.Reason != "unsupported session type" {
		t.Fatalf("recoverability not refreshed: %+v", got)
	}
	if got.TranscriptSize != 2048 || got.TranscriptLines != 40 {
		t.Fatalf("transcript metrics not stored: %+v", got)
	}
	// A rediscovery must not resurrect a disabled session or reset its state.
	if got.Enabled {
		t.Fatal("rediscovery re-enabled a disabled session")
	}
	if got.State != storage.StateDisabled {
		t.Fatalf("state = %s, want DISABLED", got.State)
	}
}

func TestUpsertSessionValidation(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	for name, session := range map[string]storage.Session{
		"empty id":  {},
		"blank id":  {ID: "   "},
		"bad state": {ID: "x", State: storage.State("TELEPORTED")},
		"bad type":  {ID: "x", Type: storage.SessionType("telepathy")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.UpsertSession(ctx, session); !errors.Is(err, storage.ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got %v", err)
			}
		})
	}

	// Defaults are filled in rather than rejected.
	if _, err := store.UpsertSession(ctx, storage.Session{ID: "defaults"}); err != nil {
		t.Fatalf("minimal session: %v", err)
	}
	got, err := store.GetSession(ctx, "defaults")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != storage.StateDiscovered || got.Type != storage.SessionTypeUnknown ||
		got.Provider != "claude" {
		t.Fatalf("defaults not applied: %+v", got)
	}

	if _, err := store.GetSession(ctx, "missing"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing session = %v", err)
	}
	if _, err := store.GetSession(ctx, ""); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("empty id = %v", err)
	}
}

func TestListSessionsFiltersAndOrdering(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	for i, id := range []string{"s1", "s2", "s3", "s4"} {
		if _, err := store.UpsertSession(ctx, storage.Session{
			ID: id, Enabled: i%2 == 0, LastSeenAt: fixedTime.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	for i, state := range []storage.State{storage.StateRunning, storage.StateWaitingForReset, storage.StateRunning, storage.StateUnrecoverable} {
		if _, err := store.TransitionSession(ctx, []string{"s1", "s2", "s3", "s4"}[i], state, "seed", 0); err != nil {
			t.Fatalf("transition: %v", err)
		}
	}

	all, err := store.ListSessions(ctx, storage.SessionFilter{})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("list all returned %d, want 4", len(all))
	}
	if all[0].ID != "s1" || all[3].ID != "s4" {
		t.Fatalf("ascending order wrong: %s .. %s", all[0].ID, all[3].ID)
	}

	newest, err := store.ListSessions(ctx, storage.SessionFilter{NewestFirst: true, Limit: 2})
	if err != nil {
		t.Fatalf("list newest: %v", err)
	}
	if len(newest) != 2 || newest[0].ID != "s4" || newest[1].ID != "s3" {
		t.Fatalf("newest/limit wrong: %+v", newest)
	}

	enabled := true
	onlyEnabled, err := store.ListSessions(ctx, storage.SessionFilter{Enabled: &enabled})
	if err != nil {
		t.Fatalf("list enabled: %v", err)
	}
	if len(onlyEnabled) != 2 {
		t.Fatalf("enabled filter returned %d, want 2", len(onlyEnabled))
	}

	running, err := store.ListSessions(ctx, storage.SessionFilter{States: []storage.State{storage.StateRunning}})
	if err != nil {
		t.Fatalf("list by state: %v", err)
	}
	if len(running) != 2 || running[0].ID != "s1" || running[1].ID != "s3" {
		t.Fatalf("state filter wrong: %+v", running)
	}

	if _, err := store.ListSessions(ctx, storage.SessionFilter{States: []storage.State{storage.State("BOGUS")}}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("invalid state filter = %v", err)
	}

	recoverable := false
	if _, err := store.ListSessions(ctx, storage.SessionFilter{Recoverable: &recoverable}); err != nil {
		t.Fatalf("recoverable filter: %v", err)
	}

	one, err := store.ListSessions(ctx, storage.SessionFilter{ID: "s2"})
	if err != nil || len(one) != 1 || one[0].ID != "s2" {
		t.Fatalf("id filter = %+v, %v", one, err)
	}

	count, err := store.CountSessions(ctx, storage.SessionFilter{})
	if err != nil || count != 4 {
		t.Fatalf("CountSessions = %d, %v", count, err)
	}
}

func TestSessionMutationsReportMissingSessions(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	missing := "does-not-exist"

	checks := map[string]error{
		"enable":             store.SetSessionEnabled(ctx, missing, true),
		"disable":            store.SetSessionEnabled(ctx, missing, false),
		"transition":         func() error { _, err := store.TransitionSession(ctx, missing, storage.StateRunning, "", 0); return err }(),
		"identity":           store.SetSessionIdentity(ctx, missing, 1, 2, 3),
		"transcript metrics": store.SetSessionTranscriptMetrics(ctx, missing, "/t", 1, 2),
		"error":              store.SetSessionError(ctx, missing, "boom"),
		"delete":             store.DeleteSession(ctx, missing),
	}
	for name, err := range checks {
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("%s on missing session = %v, want ErrNotFound", name, err)
		}
	}

	if _, err := store.TransitionSession(ctx, missing, storage.State("BOGUS"), "", 0); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("invalid transition state = %v", err)
	}
}

func TestEnableDisableRecordsTransitions(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	session := mustSeedSession(t, store, "toggle")

	if err := store.SetSessionEnabled(ctx, session.ID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	got, err := store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || got.State != storage.StateDisabled {
		t.Fatalf("after disable: enabled=%t state=%s", got.Enabled, got.State)
	}

	if err := store.SetSessionEnabled(ctx, session.ID, true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	got, err = store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.State != storage.StateDiscovered {
		t.Fatalf("after re-enable: enabled=%t state=%s", got.Enabled, got.State)
	}

	// Disabling an already enabled session and re-enabling an enabled session
	// must not spam the audit trail.
	if err := store.SetSessionEnabled(ctx, session.ID, true); err != nil {
		t.Fatalf("idempotent enable: %v", err)
	}
	transitions, err := store.ListTransitions(ctx, session.ID, 0)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	if len(transitions) != 2 {
		t.Fatalf("expected 2 transitions (disable, enable), got %d: %+v", len(transitions), transitions)
	}
	if transitions[0].FromState != storage.StateDiscovered || transitions[0].ToState != storage.StateDisabled {
		t.Fatalf("unexpected first transition: %+v", transitions[0])
	}
	if transitions[0].Reason == "" {
		t.Fatal("transition reason is empty")
	}
}

func TestSessionAuxiliaryUpdates(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	session := mustSeedSession(t, store, "aux")

	if err := store.SetSessionIdentity(ctx, session.ID, 4242, 7, 123456); err != nil {
		t.Fatalf("set identity: %v", err)
	}
	if err := store.SetSessionTranscriptMetrics(ctx, session.ID, "/new/transcript.jsonl", 4096, 88); err != nil {
		t.Fatalf("set transcript metrics: %v", err)
	}
	if err := store.SetSessionError(ctx, session.ID, "resume failed: exit status 1"); err != nil {
		t.Fatalf("set error: %v", err)
	}

	got, err := store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != 4242 || got.PPID != 7 || got.ProcessStartTime != 123456 {
		t.Fatalf("identity not applied: %+v", got)
	}
	if got.TranscriptPath != "/new/transcript.jsonl" || got.TranscriptSize != 4096 || got.TranscriptLines != 88 {
		t.Fatalf("transcript metrics not applied: %+v", got)
	}
	if got.LastError != "resume failed: exit status 1" {
		t.Fatalf("error not stored: %q", got.LastError)
	}
}

func TestUsageLimitRepository(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	session := mustSeedSession(t, store, "limits")

	reset := time.Date(2026, 3, 4, 11, 0, 0, 0, time.UTC)
	first, err := store.RecordUsageLimit(ctx, storage.UsageLimit{
		SessionID: session.ID, LimitType: storage.LimitTypeFiveHour,
		RawMessage: "You've hit your 5-hour limit · resets 12pm (Europe/Berlin)",
		ResetAt:    reset, ResetTimezone: "Europe/Berlin", ResetRaw: "12pm (Europe/Berlin)",
		Confidence: 0.97, PausedGoal: true,
	})
	if err != nil {
		t.Fatalf("record first: %v", err)
	}
	if first.ID == 0 {
		t.Fatal("recorded limit has no ID")
	}
	if !first.DetectedAt.Equal(fixedTime) {
		t.Fatalf("detected_at = %s, want the store clock %s", first.DetectedAt, fixedTime)
	}

	// A limit with no reset time must round-trip as a zero time, not as 1970.
	second, err := store.RecordUsageLimit(ctx, storage.UsageLimit{
		SessionID: session.ID, LimitType: storage.LimitTypeWeekly, RawMessage: "weekly limit",
		Confidence: 0.4, DetectedAt: fixedTime.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("record second: %v", err)
	}

	latest, err := store.LatestUsageLimit(ctx, session.ID)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if latest.ID != second.ID {
		t.Fatalf("latest = %d, want %d", latest.ID, second.ID)
	}
	if !latest.ResetAt.IsZero() {
		t.Fatalf("unknown reset time stored as %s, want zero", latest.ResetAt)
	}

	byID, err := store.GetUsageLimit(ctx, first.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !byID.ResetAt.Equal(reset) || byID.ResetTimezone != "Europe/Berlin" ||
		byID.ResetRaw != "12pm (Europe/Berlin)" || !byID.PausedGoal || byID.Confidence != 0.97 {
		t.Fatalf("round trip lost data: %+v", byID)
	}

	all, err := store.ListUsageLimits(ctx, session.ID, 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("list = %d, %v", len(all), err)
	}

	// Resolving a reset time later must persist.
	if err := store.SetUsageLimitResetAt(ctx, second.ID, reset.Add(time.Hour), "Europe/Berlin", "1pm (Europe/Berlin)"); err != nil {
		t.Fatalf("set reset: %v", err)
	}
	updated, err := store.GetUsageLimit(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.ResetAt.Equal(reset.Add(time.Hour)) || updated.ResetRaw != "1pm (Europe/Berlin)" {
		t.Fatalf("reset not updated: %+v", updated)
	}

	// Clearing it must store NULL again.
	if err := store.SetUsageLimitResetAt(ctx, second.ID, time.Time{}, "", ""); err != nil {
		t.Fatalf("clear reset: %v", err)
	}
	cleared, err := store.GetUsageLimit(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !cleared.ResetAt.IsZero() {
		t.Fatalf("cleared reset = %s", cleared.ResetAt)
	}

	if _, err := store.GetUsageLimit(ctx, 99999); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing limit = %v", err)
	}
	if _, err := store.LatestUsageLimit(ctx, "no-limits"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("latest for session without limits = %v", err)
	}
	if err := store.SetUsageLimitResetAt(ctx, 99999, reset, "", ""); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("set reset on missing limit = %v", err)
	}
}

func TestUsageLimitValidation(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	session := mustSeedSession(t, store, "limit-validation")

	for name, limit := range map[string]storage.UsageLimit{
		"no session":     {},
		"bad type":       {SessionID: session.ID, LimitType: storage.LimitType("monthly")},
		"confidence > 1": {SessionID: session.ID, Confidence: 1.5},
		"confidence < 0": {SessionID: session.ID, Confidence: -0.1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.RecordUsageLimit(ctx, limit); !errors.Is(err, storage.ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got %v", err)
			}
		})
	}

	// An empty limit type defaults to "other" rather than failing.
	limit, err := store.RecordUsageLimit(ctx, storage.UsageLimit{SessionID: session.ID})
	if err != nil {
		t.Fatalf("default limit type: %v", err)
	}
	if limit.LimitType != storage.LimitTypeOther {
		t.Fatalf("limit type = %q, want other", limit.LimitType)
	}
}

func TestAttemptRepository(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	session := mustSeedSession(t, store, "attempts")
	limit, err := store.RecordUsageLimit(ctx, storage.UsageLimit{SessionID: session.ID, Confidence: 0.9})
	if err != nil {
		t.Fatal(err)
	}

	first, err := store.StartAttempt(ctx, session.ID, limit.ID, "claude -p --resume attempts hello")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if first.AttemptNumber != 1 || first.State != storage.StateResuming {
		t.Fatalf("unexpected first attempt: %+v", first)
	}
	if first.UsageLimitID != limit.ID {
		t.Fatalf("usage limit not linked: %+v", first)
	}
	if first.Success != nil || first.FinishedAt.IsZero() == false {
		t.Fatalf("running attempt should have no outcome: %+v", first)
	}

	yes := true
	no := false
	finished, err := store.FinishAttempt(ctx, first.ID, storage.AttemptResult{
		State: storage.StateResumed, Success: true, Output: "ok",
		SessionIDVerified: &yes, TranscriptGrew: &no,
		FinishedAt: fixedTime.Add(30 * time.Second),
	})
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if finished.State != storage.StateResumed || finished.Success == nil || !*finished.Success {
		t.Fatalf("outcome not stored: %+v", finished)
	}
	if finished.SessionIDVerified == nil || !*finished.SessionIDVerified {
		t.Fatalf("session verification not stored: %+v", finished)
	}
	if finished.TranscriptGrew == nil || *finished.TranscriptGrew {
		t.Fatalf("transcript growth not stored: %+v", finished)
	}
	if !finished.FinishedAt.Equal(fixedTime.Add(30 * time.Second)) {
		t.Fatalf("finished_at = %s", finished.FinishedAt)
	}

	second, err := store.StartAttempt(ctx, session.ID, 0, "retry")
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	if second.AttemptNumber != 2 {
		t.Fatalf("second attempt number = %d, want 2", second.AttemptNumber)
	}
	if second.UsageLimitID != 0 {
		t.Fatalf("zero usage limit should stay unlinked: %+v", second)
	}

	count, err := store.CountAttempts(ctx, session.ID)
	if err != nil || count != 2 {
		t.Fatalf("CountAttempts = %d, %v", count, err)
	}

	latest, err := store.LatestAttempt(ctx, session.ID)
	if err != nil || latest.ID != second.ID {
		t.Fatalf("LatestAttempt = %+v, %v", latest, err)
	}

	all, err := store.ListAttempts(ctx, session.ID, 0)
	if err != nil || len(all) != 2 {
		t.Fatalf("ListAttempts = %d, %v", len(all), err)
	}
	limited, err := store.ListAttempts(ctx, session.ID, 1)
	if err != nil || len(limited) != 1 {
		t.Fatalf("ListAttempts(limit 1) = %d, %v", len(limited), err)
	}

	if err := store.DeleteAttempt(ctx, second.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.DeleteAttempt(ctx, second.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("double delete = %v", err)
	}
	if _, err := store.GetAttempt(ctx, second.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("get deleted = %v", err)
	}
	if _, err := store.GetAttempt(ctx, 99999); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("get missing = %v", err)
	}
	if _, err := store.LatestAttempt(ctx, "no-attempts"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("latest for session without attempts = %v", err)
	}
}

func TestAttemptValidation(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	session := mustSeedSession(t, store, "attempt-validation")

	if _, err := store.StartAttempt(ctx, "", 0, "x"); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("empty session id = %v", err)
	}
	if _, err := store.StartAttempt(ctx, "ghost-session", 0, "x"); err == nil {
		t.Fatal("attempt for a nonexistent session was accepted")
	}
	attempt, err := store.StartAttempt(ctx, session.ID, 0, "x")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinishAttempt(ctx, attempt.ID, storage.AttemptResult{State: storage.State("NOPE")}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("invalid result state = %v", err)
	}
	if _, err := store.FinishAttempt(ctx, 99999, storage.AttemptResult{State: storage.StateResumed}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("finish missing attempt = %v", err)
	}

	// Check constraints back up the Go-side validation.
	if _, err := store.DB().ExecContext(ctx,
		`INSERT INTO recovery_attempts (session_id, attempt_number, state, started_at) VALUES (?, 0, 'RESUMING', 0)`,
		session.ID); err == nil {
		t.Fatal("attempt_number 0 was accepted by the schema")
	}
	if _, err := store.DB().ExecContext(ctx,
		`INSERT INTO recovery_attempts (session_id, attempt_number, state, started_at, success) VALUES (?, 9, 'RESUMING', 0, 7)`,
		session.ID); err == nil {
		t.Fatal("success=7 was accepted by the schema")
	}
}

func TestTransitionRepository(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	session := mustSeedSession(t, store, "history")
	attempt, err := store.StartAttempt(ctx, session.ID, 0, "x")
	if err != nil {
		t.Fatal(err)
	}

	path := []storage.State{
		storage.StateUsageLimitDetected,
		storage.StateWaitingForReset,
		storage.StateResetTimeReached,
		storage.StateCheckingSession,
		storage.StateCheckingLimit,
		storage.StateResuming,
	}
	for _, state := range path {
		if _, err := store.TransitionSession(ctx, session.ID, state, "step", attempt.ID); err != nil {
			t.Fatalf("transition to %s: %v", state, err)
		}
	}

	transitions, err := store.ListTransitions(ctx, session.ID, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(transitions) != len(path) {
		t.Fatalf("got %d transitions, want %d", len(transitions), len(path))
	}
	for i, transition := range transitions {
		if transition.ToState != path[i] {
			t.Fatalf("transition %d = %s, want %s", i, transition.ToState, path[i])
		}
		wantFrom := storage.StateDiscovered
		if i > 0 {
			wantFrom = path[i-1]
		}
		if transition.FromState != wantFrom {
			t.Fatalf("transition %d from = %s, want %s", i, transition.FromState, wantFrom)
		}
		if transition.AttemptID != attempt.ID {
			t.Fatalf("transition %d attempt = %d, want %d", i, transition.AttemptID, attempt.ID)
		}
	}

	limited, err := store.ListTransitions(ctx, session.ID, 2)
	if err != nil || len(limited) != 2 {
		t.Fatalf("limited list = %d, %v", len(limited), err)
	}

	byAttempt, err := store.ListTransitionsForAttempt(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("by attempt: %v", err)
	}
	if len(byAttempt) != len(path) {
		t.Fatalf("by attempt returned %d, want %d", len(byAttempt), len(path))
	}

	// RecordTransition appends without moving the session state.
	current, err := store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTransition(ctx, storage.Transition{
		SessionID: session.ID, FromState: current.State, ToState: storage.StateResuming,
		Reason: "observed",
	}); err != nil {
		t.Fatalf("record transition: %v", err)
	}
	after, err := store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != current.State {
		t.Fatalf("RecordTransition changed state: %s -> %s", current.State, after.State)
	}
	if all, err := store.ListTransitions(ctx, session.ID, 0); err != nil || len(all) != len(path)+1 {
		t.Fatalf("after record: %d, %v", len(all), err)
	}

	if err := store.RecordTransition(ctx, storage.Transition{ToState: storage.StateResuming}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("missing session id = %v", err)
	}
	if err := store.RecordTransition(ctx, storage.Transition{SessionID: session.ID, ToState: storage.State("NOPE")}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("bad target state = %v", err)
	}
	if err := store.RecordTransition(ctx, storage.Transition{SessionID: session.ID, FromState: storage.State("NOPE"), ToState: storage.StateRunning}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("bad source state = %v", err)
	}
	// occurred_at defaults to the store clock when the caller omits it.
	if err := store.RecordTransition(ctx, storage.Transition{SessionID: session.ID, ToState: storage.StateRunning}); err != nil {
		t.Fatalf("default occurred_at: %v", err)
	}
}

func TestNotificationRepository(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	session := mustSeedSession(t, store, "notifications")

	notification, created, err := store.EnqueueNotification(ctx, storage.Notification{
		SessionID: session.ID, Event: storage.EventLimitDetected,
		DedupeKey: "limit:notifications:2026-03-04", Body: "5-hour limit detected",
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if !created {
		t.Fatal("first enqueue must report created=true")
	}
	if notification.Status != storage.NotificationPending || notification.Attempts != 0 {
		t.Fatalf("unexpected initial state: %+v", notification)
	}

	// The same logical event again must not create a second row.
	duplicate, created, err := store.EnqueueNotification(ctx, storage.Notification{
		SessionID: session.ID, Event: storage.EventLimitDetected,
		DedupeKey: "limit:notifications:2026-03-04", Body: "different body",
	})
	if err != nil {
		t.Fatalf("duplicate enqueue: %v", err)
	}
	if created {
		t.Fatal("duplicate enqueue reported created=true")
	}
	if duplicate.ID != notification.ID || duplicate.Body != "5-hour limit detected" {
		t.Fatalf("duplicate did not return the existing row: %+v", duplicate)
	}

	pending, err := store.ListPendingNotifications(ctx, 0)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %d, %v", len(pending), err)
	}

	if err := store.MarkNotificationFailed(ctx, notification.ID, "429 rate limited", storage.NotificationPending); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	afterFailure, err := store.GetNotification(ctx, notification.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterFailure.Status != storage.NotificationPending || afterFailure.Attempts != 1 ||
		afterFailure.LastError != "429 rate limited" {
		t.Fatalf("failure not recorded: %+v", afterFailure)
	}

	if err := store.MarkNotificationSent(ctx, notification.ID, fixedTime.Add(time.Second)); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	afterSent, err := store.GetNotification(ctx, notification.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterSent.Status != storage.NotificationSent || afterSent.Attempts != 2 {
		t.Fatalf("send not recorded: %+v", afterSent)
	}
	if !afterSent.SentAt.Equal(fixedTime.Add(time.Second)) {
		t.Fatalf("sent_at = %s", afterSent.SentAt)
	}
	if afterSent.LastError != "" {
		t.Fatalf("last_error should be cleared on success: %q", afterSent.LastError)
	}

	// Marking an already-sent notification is refused rather than double counted.
	if err := store.MarkNotificationSent(ctx, notification.ID, time.Time{}); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("double send = %v", err)
	}

	if _, err := store.GetNotificationByKey(ctx, "limit:notifications:2026-03-04"); err != nil {
		t.Fatalf("get by key: %v", err)
	}
	if _, err := store.GetNotificationByKey(ctx, "absent"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing key = %v", err)
	}
	if _, err := store.GetNotification(ctx, 99999); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("missing id = %v", err)
	}

	forSession, err := store.ListNotificationsForSession(ctx, session.ID, 0)
	if err != nil || len(forSession) != 1 {
		t.Fatalf("for session = %d, %v", len(forSession), err)
	}

	// A dropped notification is recorded as such.
	dropped, _, err := store.EnqueueNotification(ctx, storage.Notification{
		Event: storage.EventWaiting, DedupeKey: "waiting:1", Body: "waiting",
	})
	if err != nil {
		t.Fatalf("enqueue without session: %v", err)
	}
	if dropped.SessionID != "" {
		t.Fatalf("sessionless notification gained a session: %+v", dropped)
	}
	if err := store.DropNotification(ctx, dropped.ID, "telegram disabled"); err != nil {
		t.Fatalf("drop: %v", err)
	}
	afterDrop, err := store.GetNotification(ctx, dropped.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterDrop.Status != storage.NotificationDropped || afterDrop.LastError != "telegram disabled" {
		t.Fatalf("drop not recorded: %+v", afterDrop)
	}
}

func TestNotificationValidation(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	for name, notification := range map[string]storage.Notification{
		"no dedupe key": {Event: storage.EventWaiting},
		"bad event":     {Event: storage.NotificationEvent("teleported"), DedupeKey: "k1"},
		"bad status":    {Event: storage.EventWaiting, DedupeKey: "k2", Status: storage.NotificationStatus("maybe")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := store.EnqueueNotification(ctx, notification); !errors.Is(err, storage.ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got %v", err)
			}
		})
	}

	if err := store.MarkNotificationFailed(ctx, 1, "x", storage.NotificationStatus("maybe")); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("invalid failure status = %v", err)
	}
	if err := store.MarkNotificationFailed(ctx, 99999, "x", storage.NotificationFailed); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("mark missing failed = %v", err)
	}

	// Oversized bodies are truncated to the Telegram limit rather than stored.
	long := make([]byte, 10000)
	for i := range long {
		long[i] = 'a'
	}
	notification, _, err := store.EnqueueNotification(ctx, storage.Notification{
		Event: storage.EventWaiting, DedupeKey: "long-body", Body: string(long),
	})
	if err != nil {
		t.Fatalf("enqueue long body: %v", err)
	}
	if len(notification.Body) != 4096 {
		t.Fatalf("body length = %d, want 4096", len(notification.Body))
	}
}

func TestSettingsRepository(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	value, ok, err := store.GetSetting(ctx, storage.SettingGlobalEnabled)
	if err != nil || ok || value != "" {
		t.Fatalf("missing setting = %q, %t, %v", value, ok, err)
	}

	if err := store.SetSetting(ctx, storage.SettingGlobalEnabled, "true"); err != nil {
		t.Fatalf("set: %v", err)
	}
	value, ok, err = store.GetSetting(ctx, storage.SettingGlobalEnabled)
	if err != nil || !ok || value != "true" {
		t.Fatalf("get = %q, %t, %v", value, ok, err)
	}

	// Upsert overwrites rather than failing.
	if err := store.SetSetting(ctx, storage.SettingGlobalEnabled, "false"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if value, _, _ := store.GetSetting(ctx, storage.SettingGlobalEnabled); value != "false" {
		t.Fatalf("overwrite lost: %q", value)
	}

	if err := store.SetSetting(ctx, storage.SettingAPITokenFingerprint, "abc"); err != nil {
		t.Fatalf("second setting: %v", err)
	}
	all, err := store.ListSettings(ctx)
	if err != nil || len(all) != 2 {
		t.Fatalf("list = %d, %v", len(all), err)
	}
	if all[0].Key != storage.SettingAPITokenFingerprint || all[1].Key != storage.SettingGlobalEnabled {
		t.Fatalf("settings not ordered by key: %+v", all)
	}
	if all[0].UpdatedAt.IsZero() {
		t.Fatal("setting has no updated_at")
	}

	if err := store.DeleteSetting(ctx, storage.SettingGlobalEnabled); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.DeleteSetting(ctx, storage.SettingGlobalEnabled); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("double delete = %v", err)
	}
	if _, ok, _ := store.GetSetting(ctx, storage.SettingGlobalEnabled); ok {
		t.Fatal("setting survived delete")
	}

	if err := store.SetSetting(ctx, "  ", "x"); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("blank key = %v", err)
	}
	if _, _, err := store.GetSetting(ctx, ""); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("empty key get = %v", err)
	}
}

func TestModelValidationHelpers(t *testing.T) {
	for _, state := range []storage.State{
		storage.StateDiscovered, storage.StateRunning, storage.StateUsageLimitDetected,
		storage.StateWaitingForReset, storage.StateResetTimeReached, storage.StateCheckingSession,
		storage.StateCheckingLimit, storage.StateResuming, storage.StateResumed,
		storage.StateRetryWait, storage.StateCompleted,
		storage.StateUserTerminated, storage.StateDisabled, storage.StateUnrecoverable,
	} {
		if !state.Valid() {
			t.Errorf("state %s should be valid", state)
		}
	}
	if storage.State("nope").Valid() {
		t.Error("unknown state reported valid")
	}
	for _, sessionType := range []storage.SessionType{
		storage.SessionTypeInteractive, storage.SessionTypeHeadless, storage.SessionTypeUnknown,
	} {
		if !sessionType.Valid() {
			t.Errorf("session type %s should be valid", sessionType)
		}
	}
	if storage.SessionType("telepathy").Valid() {
		t.Error("unknown session type reported valid")
	}
	for _, limitType := range []storage.LimitType{
		storage.LimitTypeFiveHour, storage.LimitTypeWeekly, storage.LimitTypeOther,
	} {
		if !limitType.Valid() {
			t.Errorf("limit type %s should be valid", limitType)
		}
	}
	if storage.LimitType("monthly").Valid() {
		t.Error("unknown limit type reported valid")
	}
	for _, status := range []storage.NotificationStatus{
		storage.NotificationPending, storage.NotificationSent,
		storage.NotificationFailed, storage.NotificationDropped,
	} {
		if !status.Valid() {
			t.Errorf("notification status %s should be valid", status)
		}
	}
	if storage.NotificationStatus("maybe").Valid() {
		t.Error("unknown notification status reported valid")
	}
	for _, event := range []storage.NotificationEvent{
		storage.EventLimitDetected, storage.EventWaiting, storage.EventRecoveryStarted,
		storage.EventRecoverySuccess, storage.EventRecoveryFailure,
		storage.EventPermanentFailure, storage.EventSessionCompleted,
	} {
		if !event.Valid() {
			t.Errorf("event %s should be valid", event)
		}
	}
	if storage.NotificationEvent("party").Valid() {
		t.Error("unknown event reported valid")
	}
}
