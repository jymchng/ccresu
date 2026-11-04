package storage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/storage"
)

// TestScheduleRepository exercises the lifecycle of one pending reset wait:
// create, read back, replace on a second limit, order, and delete.
func TestScheduleRepository(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	session := mustSeedSession(t, store, "schedule")

	limit, err := store.RecordUsageLimit(ctx, storage.UsageLimit{
		SessionID: session.ID, LimitType: storage.LimitTypeFiveHour,
		RawMessage: "You've hit your 5-hour limit", ResetAt: fixedTime.Add(time.Hour),
		ResetTimezone: "Europe/Berlin", ResetRaw: "12pm (Europe/Berlin)",
	})
	if err != nil {
		t.Fatalf("record limit: %v", err)
	}

	reset := time.Date(2026, 3, 4, 11, 0, 0, 0, time.UTC)
	saved, err := store.SaveSchedule(ctx, storage.Schedule{
		SessionID: session.ID, UsageLimitID: limit.ID, ResetAt: reset,
		ResetTimezone: "Europe/Berlin", ResetRaw: "12pm (Europe/Berlin)",
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !saved.CreatedAt.Equal(fixedTime) || !saved.UpdatedAt.Equal(fixedTime) {
		t.Fatalf("timestamps not taken from the store clock: %+v", saved)
	}

	got, err := store.GetSchedule(ctx, session.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.ResetAt.Equal(reset) || got.ResetTimezone != "Europe/Berlin" ||
		got.ResetRaw != "12pm (Europe/Berlin)" || got.UsageLimitID != limit.ID {
		t.Fatalf("round trip lost data: %+v", got)
	}
	if got.Unknown {
		t.Fatalf("known schedule reported as unknown: %+v", got)
	}
	if !got.DueAt().Equal(reset) {
		t.Fatalf("DueAt = %s, want the reset time %s", got.DueAt(), reset)
	}

	// A second limit on the same session replaces the stale wait instead of
	// accumulating a row that would wake the session twice.
	replaced := reset.Add(24 * time.Hour)
	if _, err := store.SaveSchedule(ctx, storage.Schedule{
		SessionID: session.ID, ResetAt: replaced, ResetRaw: "11am (Europe/Berlin)",
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}
	newer, err := store.GetSchedule(ctx, session.ID)
	if err != nil {
		t.Fatalf("get after replace: %v", err)
	}
	if !newer.ResetAt.Equal(replaced) || newer.ResetRaw != "11am (Europe/Berlin)" {
		t.Fatalf("replace did not take effect: %+v", newer)
	}
	if newer.UsageLimitID != 0 {
		t.Fatalf("replace kept the stale usage limit id %d", newer.UsageLimitID)
	}
	if !newer.CreatedAt.Equal(fixedTime) || !newer.UpdatedAt.Equal(fixedTime) {
		t.Fatalf("replace rewrote the creation time: %+v", newer)
	}

	all, err := store.LoadSchedules(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(all) != 1 || all[0].SessionID != session.ID {
		t.Fatalf("load = %+v, want exactly the replaced row", all)
	}

	if err := store.DeleteSchedule(ctx, session.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.GetSchedule(ctx, session.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("get after delete = %v, want ErrNotFound", err)
	}

	// Deleting a wait that is already gone is not an error: the wait is over
	// either way.
	if err := store.DeleteSchedule(ctx, session.ID); err != nil {
		t.Fatalf("repeat delete: %v", err)
	}
	if err := store.DeleteSchedule(ctx, "no-such-session"); err != nil {
		t.Fatalf("delete for unknown session: %v", err)
	}
	if _, err := store.GetSchedule(ctx, "no-such-session"); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("get for unknown session = %v, want ErrNotFound", err)
	}
}

// TestLoadSchedulesOrdersByDeadline proves the scheduler drains the earliest
// wait first, and that a known reset outranks a poll for the same instant by
// session id so the order is deterministic.
func TestLoadSchedulesOrdersByDeadline(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	base := time.Date(2026, 3, 5, 9, 0, 0, 0, time.UTC)
	for _, sched := range []storage.Schedule{
		{SessionID: "third", ResetAt: base.Add(3 * time.Hour)},
		{SessionID: "first", ResetAt: base.Add(time.Hour)},
		{SessionID: "second-b", ResetAt: base.Add(2 * time.Hour)},
		{SessionID: "second-a", ResetAt: base.Add(2 * time.Hour)},
	} {
		mustSeedSession(t, store, sched.SessionID)
		if _, err := store.SaveSchedule(ctx, sched); err != nil {
			t.Fatalf("save %s: %v", sched.SessionID, err)
		}
	}

	loaded, err := store.LoadSchedules(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := []string{"first", "second-a", "second-b", "third"}
	if len(loaded) != len(want) {
		t.Fatalf("load returned %d rows, want %d", len(loaded), len(want))
	}
	for i, id := range want {
		if loaded[i].SessionID != id {
			t.Fatalf("row %d = %s, want %s (full order %v)", i, loaded[i].SessionID, id, order(loaded))
		}
	}
}

// order renders the loaded session ids for failure messages.
func order(schedules []storage.Schedule) []string {
	ids := make([]string, 0, len(schedules))
	for _, sched := range schedules {
		ids = append(ids, sched.SessionID)
	}
	return ids
}

// TestScheduleUnknownPolling covers the path taken when the banner carried no
// usable reset time: the row polls on a backoff and must survive a restart with
// its attempt count intact.
func TestScheduleUnknownPolling(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	session := mustSeedSession(t, store, "unknown")

	pollAt := fixedTime.Add(5 * time.Minute)
	if _, err := store.SaveSchedule(ctx, storage.Schedule{
		SessionID: session.ID, Unknown: true, NextPollAt: pollAt, Attempts: 3,
		ResetRaw: "try again later",
	}); err != nil {
		t.Fatalf("save unknown: %v", err)
	}

	got, err := store.GetSchedule(ctx, session.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Unknown || got.Attempts != 3 || !got.NextPollAt.Equal(pollAt) {
		t.Fatalf("unknown schedule did not round trip: %+v", got)
	}
	if !got.ResetAt.IsZero() {
		t.Fatalf("unknown schedule stored a reset time: %s", got.ResetAt)
	}
	if !got.DueAt().Equal(pollAt) {
		t.Fatalf("DueAt = %s, want the poll time %s", got.DueAt(), pollAt)
	}

	// The backoff survives a restart, so a daemon flush does not silently reset
	// the wait to its floor.
	if _, err := store.SaveSchedule(ctx, storage.Schedule{
		SessionID: session.ID, Unknown: true, NextPollAt: pollAt.Add(10 * time.Minute), Attempts: 4,
	}); err != nil {
		t.Fatalf("save replaced unknown: %v", err)
	}
	bumped, err := store.GetSchedule(ctx, session.ID)
	if err != nil {
		t.Fatalf("get replaced: %v", err)
	}
	if bumped.Attempts != 4 || !bumped.NextPollAt.Equal(pollAt.Add(10*time.Minute)) {
		t.Fatalf("attempt count or poll time not replaced: %+v", bumped)
	}
}

// TestLoadSchedulesSkipsRowsWithNoDeadline guards the ordering query: a row with
// neither a reset time nor a poll time can never become due, so honouring it
// would stall the scheduler forever.
func TestLoadSchedulesSkipsRowsWithNoDeadline(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	mustSeedSession(t, store, "deadline")
	mustSeedSession(t, store, "no-deadline")

	reset := time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)
	if _, err := store.SaveSchedule(ctx, storage.Schedule{SessionID: "deadline", ResetAt: reset}); err != nil {
		t.Fatalf("save deadline: %v", err)
	}
	if _, err := store.SaveSchedule(ctx, storage.Schedule{SessionID: "no-deadline", Unknown: true}); err != nil {
		t.Fatalf("save no-deadline: %v", err)
	}

	loaded, err := store.LoadSchedules(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != 1 || loaded[0].SessionID != "deadline" {
		t.Fatalf("load = %v, want only the schedulable row", order(loaded))
	}

	// The row is still there: it is skipped by the scheduler, not discarded, so
	// a later poll time can revive it.
	stranded, err := store.GetSchedule(ctx, "no-deadline")
	if err != nil {
		t.Fatalf("get stranded: %v", err)
	}
	if !stranded.DueAt().IsZero() {
		t.Fatalf("DueAt = %s, want the zero time", stranded.DueAt())
	}
}

// TestScheduleValidation covers the guards that keep a row honest: a schedule
// must name a session and must be either a known instant or an explicit unknown.
func TestScheduleValidation(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	session := mustSeedSession(t, store, "schedule-validation")

	reset := time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC)
	for name, sched := range map[string]storage.Schedule{
		"no session":     {ResetAt: reset},
		"blank session":  {SessionID: "   ", ResetAt: reset},
		"no reset":       {SessionID: session.ID},
		"unknown and at": {SessionID: session.ID, Unknown: true, ResetAt: reset},
		"negative tries": {SessionID: session.ID, ResetAt: reset, Attempts: -1},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.SaveSchedule(ctx, sched); !errors.Is(err, storage.ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got %v", err)
			}
		})
	}

	if _, err := store.GetSchedule(ctx, " "); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("get with blank id = %v, want ErrInvalid", err)
	}
	if err := store.DeleteSchedule(ctx, ""); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("delete with empty id = %v, want ErrInvalid", err)
	}
	if _, err := store.GetSchedule(ctx, session.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("get with no row = %v, want ErrNotFound", err)
	}

	// The foreign keys are enforced: a schedule cannot outlive its session, and
	// a bogus usage limit id is refused rather than silently stored.
	if _, err := store.SaveSchedule(ctx, storage.Schedule{
		SessionID: session.ID, UsageLimitID: 99999, ResetAt: reset,
	}); err == nil {
		t.Fatal("schedule with an unknown usage limit id was accepted")
	}
}

// TestScheduleCascadesWithSession proves deleting a session removes its wait,
// so a forgotten session cannot keep waking the daemon.
func TestScheduleCascadesWithSession(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	session := mustSeedSession(t, store, "cascade")

	if _, err := store.SaveSchedule(ctx, storage.Schedule{
		SessionID: session.ID, ResetAt: fixedTime.Add(time.Hour),
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := store.DeleteSession(ctx, session.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if _, err := store.GetSchedule(ctx, session.ID); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("schedule survived its session: %v", err)
	}
}

// TestScheduleSurvivesReopen is the persistence acceptance criterion at the
// storage layer: the wait is on disk, not in memory.
func TestScheduleSurvivesReopen(t *testing.T) {
	path := t.TempDir() + "/reopen.db"
	ctx := context.Background()

	store := openAt(t, path)
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	mustSeedSession(t, store, "reopen")
	reset := time.Date(2026, 3, 6, 8, 30, 0, 0, time.UTC)
	if _, err := store.SaveSchedule(ctx, storage.Schedule{
		SessionID: "reopen", ResetAt: reset, ResetTimezone: "UTC", ResetRaw: "8:30am (UTC)",
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := storage.Open(ctx, storage.Options{Path: path})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.GetSchedule(ctx, "reopen")
	if err != nil {
		t.Fatalf("get after reopen: %v", err)
	}
	if !got.ResetAt.Equal(reset) || got.ResetRaw != "8:30am (UTC)" || got.ResetTimezone != "UTC" {
		t.Fatalf("wait did not survive the restart: %+v", got)
	}
}
