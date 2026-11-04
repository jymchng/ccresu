package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/detector"
	"github.com/jymchng/ccresu/internal/scheduler"
	"github.com/jymchng/ccresu/internal/storage"
)

// openStore returns a migrated store on a temporary file. A file rather than
// :memory: is required so the test can close and reopen it, which is what a
// restart looks like.
func openStore(t *testing.T, path string) *storage.Store {
	t.Helper()
	store, err := storage.Open(context.Background(), storage.Options{Path: path})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	if _, err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// seedSession creates the parent row a schedule's foreign key needs.
func seedSession(t *testing.T, store *storage.Store, id string) {
	t.Helper()
	if _, err := store.UpsertSession(context.Background(), storage.Session{
		ID:           id,
		ProjectPath:  "/tmp/project",
		Type:         storage.SessionTypeHeadless,
		State:        storage.StateWaitingForReset,
		Enabled:      true,
		Recoverable:  true,
		DiscoveredAt: time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC),
		LastSeenAt:   time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
}

// TestScheduleSurvivesRestart is the persistence acceptance criterion: a
// resolved reset time written by one process is still there, with its zone and
// original phrase, after the database is closed and reopened.
func TestScheduleSurvivesRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := t.TempDir() + "/ccresu.db"
	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)

	// First run: detect, plan and persist.
	store := openStore(t, path)
	seedSession(t, store, "sess-1")
	first, err := scheduler.New(scheduler.Config{
		Clock: clock.NewFake(start),
		Store: scheduler.NewStorageStore(store),
	})
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}

	event := detector.Analyze("You've hit your 5-hour limit \u00b7 resets 12pm (Europe/Berlin)")
	if !event.Detected {
		t.Fatalf("detector did not detect the banner: %v", event.Notes)
	}
	planned, _, err := first.PlanEvent("sess-1", event, start)
	if err != nil {
		t.Fatalf("PlanEvent: %v", err)
	}
	if _, err := first.Save(ctx, planned); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Second run: a fresh process reading the same database.
	store = openStore(t, path)
	second, err := scheduler.New(scheduler.Config{
		Clock: clock.NewFake(start.Add(30 * time.Minute)),
		Store: scheduler.NewStorageStore(store),
	})
	if err != nil {
		t.Fatalf("scheduler.New (restart): %v", err)
	}
	restored, err := second.Restore(ctx)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("restored %d schedules after restart, want 1", len(restored))
	}
	got := restored[0]
	if want := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC); !got.ResetAt.Equal(want) {
		t.Fatalf("restored reset = %s, want %s", got.ResetAt.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	if got.ResetTimezone != "Europe/Berlin" {
		t.Fatalf("restored timezone = %q", got.ResetTimezone)
	}
	if got.ResetRaw != "12pm (Europe/Berlin)" {
		t.Fatalf("restored phrase = %q", got.ResetRaw)
	}
	if got.Unknown {
		t.Fatal("a known reset must not come back marked unknown")
	}
	// Half an hour after detection the reset is still 30 minutes away, so the
	// restarted daemon must keep waiting rather than resume immediately.
	if got.Due(start.Add(30 * time.Minute)) {
		t.Fatal("the schedule must not be due yet after a restart")
	}
	if remaining, ok := got.Wait(start.Add(30 * time.Minute)); !ok || remaining != 30*time.Minute {
		t.Fatalf("remaining wait after restart = %s, %v; want 30m", remaining, ok)
	}
}

// TestPollBackoffSurvivesRestart proves the poll counter is durable: a restart
// must not reset the backoff to five minutes and start hammering the CLI.
func TestPollBackoffSurvivesRestart(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := t.TempDir() + "/ccresu.db"
	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)

	store := openStore(t, path)
	seedSession(t, store, "sess-1")
	first, err := scheduler.New(scheduler.Config{
		Clock: clock.NewFake(start),
		Store: scheduler.NewStorageStore(store),
	})
	if err != nil {
		t.Fatalf("scheduler.New: %v", err)
	}

	// A message with no reset time at all: the polling path.
	planned, res, err := first.Plan("sess-1", 0, detector.ResetPhrase{}, start)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.Known {
		t.Fatal("the empty phrase should not have resolved")
	}
	if _, err := first.Save(ctx, planned); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Spend three polls: gaps of 5m, 10m and 20m.
	now := start
	current := planned
	for i := 0; i < 3; i++ {
		now = now.Add(first.IntervalFor(current))
		current, err = first.Advance(ctx, current, now)
		if err != nil {
			t.Fatalf("Advance: %v", err)
		}
	}
	if current.Attempts != 3 {
		t.Fatalf("attempts = %d, want 3", current.Attempts)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	store = openStore(t, path)
	second, err := scheduler.New(scheduler.Config{
		Clock: clock.NewFake(now),
		Store: scheduler.NewStorageStore(store),
	})
	if err != nil {
		t.Fatalf("scheduler.New (restart): %v", err)
	}
	restored, err := second.Restore(ctx)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("restored %d schedules, want 1", len(restored))
	}
	if restored[0].Attempts != 3 {
		t.Fatalf("restored attempts = %d, want 3", restored[0].Attempts)
	}
	if !restored[0].Unknown {
		t.Fatal("restored schedule lost its unknown flag")
	}
	if got, want := second.IntervalFor(restored[0]), 30*time.Minute; got != want {
		t.Fatalf("next interval after restart = %s, want %s", got, want)
	}
	if restored[0].NextPollAt.IsZero() {
		t.Fatal("restored schedule lost its next poll time")
	}
}

// TestStorageRepositoryValidation exercises the SQL layer's own guards, which
// keep an unusable row out of the database.
func TestStorageRepositoryValidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openStore(t, t.TempDir()+"/ccresu.db")
	seedSession(t, store, "sess-1")
	now := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)

	t.Run("missing session id", func(t *testing.T) {
		if _, err := store.SaveSchedule(ctx, storage.Schedule{}); err == nil {
			t.Fatal("an empty session id must be rejected")
		}
	})

	t.Run("known schedule needs an instant", func(t *testing.T) {
		if _, err := store.SaveSchedule(ctx, storage.Schedule{SessionID: "sess-1"}); err == nil {
			t.Fatal("a known schedule with no reset time must be rejected")
		}
	})

	t.Run("unknown schedule must not carry an instant", func(t *testing.T) {
		if _, err := store.SaveSchedule(ctx, storage.Schedule{
			SessionID: "sess-1", Unknown: true, ResetAt: now,
		}); err == nil {
			t.Fatal("an unknown schedule carrying a reset time must be rejected")
		}
	})

	t.Run("negative attempts", func(t *testing.T) {
		if _, err := store.SaveSchedule(ctx, storage.Schedule{
			SessionID: "sess-1", ResetAt: now, Attempts: -1,
		}); err == nil {
			t.Fatal("a negative attempt count must be rejected")
		}
	})

	t.Run("foreign key", func(t *testing.T) {
		if _, err := store.SaveSchedule(ctx, storage.Schedule{
			SessionID: "no-such-session", ResetAt: now,
		}); err == nil {
			t.Fatal("a schedule for an unknown session must be rejected")
		}
	})

	t.Run("get missing", func(t *testing.T) {
		if _, err := store.GetSchedule(ctx, "sess-1"); err == nil {
			t.Fatal("GetSchedule on an empty table must report not found")
		}
	})

	t.Run("round trip", func(t *testing.T) {
		saved, err := store.SaveSchedule(ctx, storage.Schedule{
			SessionID: "sess-1", ResetAt: now, ResetTimezone: "UTC", ResetRaw: "12pm",
		})
		if err != nil {
			t.Fatalf("SaveSchedule: %v", err)
		}
		if !saved.CreatedAt.Equal(now) && saved.CreatedAt.IsZero() {
			t.Fatal("CreatedAt was not set")
		}
		got, err := store.GetSchedule(ctx, "sess-1")
		if err != nil {
			t.Fatalf("GetSchedule: %v", err)
		}
		if !got.ResetAt.Equal(now) || got.ResetRaw != "12pm" {
			t.Fatalf("round trip = %+v", got)
		}
		if got.DueAt().IsZero() {
			t.Fatal("DueAt returned the zero time for a known schedule")
		}
	})
}

// TestLoadSchedulesSkipsRowsWithNoDeadline guards the ordering query: a row with
// neither a reset time nor a poll time can never become due and must not stall
// the scheduler.
func TestLoadSchedulesSkipsRowsWithNoDeadline(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openStore(t, t.TempDir()+"/ccresu.db")
	seedSession(t, store, "sess-1")
	seedSession(t, store, "sess-2")
	now := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)

	// An unknown schedule whose poll time was never set is written directly,
	// because the repository API cannot produce one.
	if _, err := store.DB().ExecContext(ctx, `
		INSERT INTO schedules (session_id, unknown_reset, attempts, created_at, updated_at)
		VALUES ('sess-2', 1, 0, ?, ?)`, now.UnixMilli(), now.UnixMilli()); err != nil {
		t.Fatalf("insert dateless schedule: %v", err)
	}
	if _, err := store.SaveSchedule(ctx, storage.Schedule{SessionID: "sess-1", ResetAt: now}); err != nil {
		t.Fatalf("SaveSchedule: %v", err)
	}

	loaded, err := store.LoadSchedules(ctx)
	if err != nil {
		t.Fatalf("LoadSchedules: %v", err)
	}
	if len(loaded) != 1 || loaded[0].SessionID != "sess-1" {
		t.Fatalf("LoadSchedules = %+v, want only the schedulable row", loaded)
	}
}

// TestDeleteScheduleIsIdempotent proves removing a wait twice is harmless, which
// matters because recovery completion and user termination can both try.
func TestDeleteScheduleIsIdempotent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openStore(t, t.TempDir()+"/ccresu.db")
	seedSession(t, store, "sess-1")

	for i := 0; i < 2; i++ {
		if err := store.DeleteSchedule(ctx, "sess-1"); err != nil {
			t.Fatalf("DeleteSchedule attempt %d: %v", i, err)
		}
	}
	if _, err := store.GetSchedule(ctx, "sess-1"); err == nil {
		t.Fatal("GetSchedule after delete must report not found")
	}
}

// TestScheduleCascadesWithSession proves a deleted session takes its pending
// wait with it, so a removed session cannot wake up later.
func TestScheduleCascadesWithSession(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openStore(t, t.TempDir()+"/ccresu.db")
	seedSession(t, store, "sess-1")
	now := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)

	if _, err := store.SaveSchedule(ctx, storage.Schedule{SessionID: "sess-1", ResetAt: now}); err != nil {
		t.Fatalf("SaveSchedule: %v", err)
	}
	if err := store.DeleteSession(ctx, "sess-1"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	loaded, err := store.LoadSchedules(ctx)
	if err != nil {
		t.Fatalf("LoadSchedules: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("%d schedules survived their session", len(loaded))
	}
}

// TestClaudeSessionIDShape documents why the schedule is keyed by session ID: it
// is what `claude --resume` accepts, so the wait can be attributed to a real
// session rather than a process.
func TestClaudeSessionIDShape(t *testing.T) {
	t.Parallel()

	provider := claude.NewFakeProvider()
	sessions, err := provider.Discover(context.Background(), claude.DiscoverFilter{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) == 0 {
		t.Skip("the fake provider discovered no sessions")
	}
	for _, session := range sessions {
		if session.ID == "" {
			t.Fatal("a discovered session must have an ID")
		}
	}
}
