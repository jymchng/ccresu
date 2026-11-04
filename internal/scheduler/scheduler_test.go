package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/clock"
	"github.com/jymchng/ccresu/internal/config"
	"github.com/jymchng/ccresu/internal/detector"
)

// newTestScheduler builds a scheduler on a fake clock frozen at start.
func newTestScheduler(t *testing.T, start time.Time, store Store) (*Scheduler, *clock.Fake) {
	t.Helper()
	fake := clock.NewFake(start)
	sched, err := New(Config{Clock: fake, Store: store})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return sched, fake
}

func TestNewDefaults(t *testing.T) {
	t.Parallel()

	sched, err := New(Config{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if sched.initial != DefaultInitialInterval {
		t.Fatalf("initial interval = %s, want %s", sched.initial, DefaultInitialInterval)
	}
	if sched.max != DefaultMaxInterval {
		t.Fatalf("max interval = %s, want %s", sched.max, DefaultMaxInterval)
	}
	if sched.opts.Location != time.UTC {
		t.Fatalf("default location = %v, want UTC", sched.opts.Location)
	}
	if sched.Clock() == nil {
		t.Fatal("Clock() returned nil")
	}
	if sched.Now().Location() != time.UTC {
		t.Fatalf("Now() = %v, want UTC", sched.Now().Location())
	}
}

func TestNewRejectsInvertedIntervals(t *testing.T) {
	t.Parallel()

	_, err := New(Config{InitialInterval: 30 * time.Minute, MaxInterval: 5 * time.Minute})
	if err == nil {
		t.Fatal("a max interval shorter than the initial one must be rejected")
	}
}

func TestFromConfig(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.App.Timezone = "Europe/Berlin"
	cfg.Scheduler.UnknownResetInitialInterval = config.NewDuration(2 * time.Minute)
	cfg.Scheduler.UnknownResetMaxInterval = config.NewDuration(12 * time.Minute)

	sched, err := FromConfig(&cfg, clock.NewFake(time.Now()), NewMemoryStore())
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	if sched.opts.LocationName != "Europe/Berlin" {
		t.Fatalf("location name = %q", sched.opts.LocationName)
	}
	if sched.PollDelay(1) != 2*time.Minute {
		t.Fatalf("first poll delay = %s, want 2m", sched.PollDelay(1))
	}
	if sched.PollDelay(10) != 12*time.Minute {
		t.Fatalf("capped poll delay = %s, want 12m", sched.PollDelay(10))
	}

	// A bad zone is an error rather than a silent fallback to UTC: a wrong zone
	// would move every wake-up by hours.
	cfg.App.Timezone = "Mars/Olympus_Mons"
	if _, err := FromConfig(&cfg, clock.NewFake(time.Now()), nil); err == nil {
		t.Fatal("an unknown app.timezone must be rejected")
	}
	if _, err := FromConfig(nil, clock.NewFake(time.Now()), nil); err == nil {
		t.Fatal("FromConfig(nil) must be rejected")
	}
}

func TestFromConfigDefaultsToUTC(t *testing.T) {
	t.Parallel()

	cfg := config.Defaults()
	cfg.App.Timezone = ""

	sched, err := FromConfig(&cfg, clock.NewFake(time.Now()), nil)
	if err != nil {
		t.Fatalf("FromConfig: %v", err)
	}
	if sched.opts.Location != time.UTC {
		t.Fatalf("location = %v, want UTC", sched.opts.Location)
	}
}

func TestPlanKnownReset(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	sched, _ := newTestScheduler(t, start, nil)

	planned, res, err := sched.Plan("sess-1", 42, phrase("12pm", "Europe/Berlin"), start)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	want := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC)
	if !res.Known || !planned.ResetAt.Equal(want) {
		t.Fatalf("planned reset = %s, want %s", planned.ResetAt.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	if planned.Unknown {
		t.Fatal("a resolved phrase must not be marked unknown")
	}
	if planned.ResetTimezone != "Europe/Berlin" {
		t.Fatalf("timezone = %q", planned.ResetTimezone)
	}
	if planned.ResetRaw != "12pm (Europe/Berlin)" {
		t.Fatalf("raw phrase = %q", planned.ResetRaw)
	}
	if planned.UsageLimitID != 42 {
		t.Fatalf("usage limit id = %d", planned.UsageLimitID)
	}
	if planned.SessionID != "sess-1" {
		t.Fatalf("session id = %q", planned.SessionID)
	}
	if !planned.CreatedAt.Equal(start) || !planned.UpdatedAt.Equal(start) {
		t.Fatalf("timestamps = %s / %s", planned.CreatedAt, planned.UpdatedAt)
	}
}

// TestPlanUnknownResetFallsBackToPolling is the degraded path: a message with no
// usable reset time must poll, never give up.
func TestPlanUnknownResetFallsBackToPolling(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	sched, _ := newTestScheduler(t, start, nil)

	planned, res, err := sched.Plan("sess-1", 1, detector.ResetPhrase{}, start)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.Known {
		t.Fatal("the phrase should not have resolved")
	}
	if !planned.Unknown {
		t.Fatal("the schedule must be marked unknown")
	}
	if !planned.ResetAt.IsZero() {
		t.Fatalf("an unknown schedule must not carry a reset time: %s", planned.ResetAt)
	}
	if want := start.Add(DefaultInitialInterval); !planned.NextPollAt.Equal(want) {
		t.Fatalf("first poll = %s, want %s", planned.NextPollAt.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	if planned.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0", planned.Attempts)
	}
}

func TestPlanRejectsEmptySession(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	sched, _ := newTestScheduler(t, start, nil)

	if _, _, err := sched.Plan("", 0, phrase("12pm", ""), start); !errors.Is(err, ErrNoSession) {
		t.Fatalf("Plan with no session error = %v, want ErrNoSession", err)
	}
}

func TestPlanEvent(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	sched, _ := newTestScheduler(t, start, nil)

	event := detector.Analyze("You've hit your 5-hour limit \u00b7 resets 12pm (Europe/Berlin)")
	if !event.Detected {
		t.Fatalf("detector did not detect the canonical banner: %v", event.Notes)
	}

	planned, res, err := sched.PlanEvent("sess-1", event, start)
	if err != nil {
		t.Fatalf("PlanEvent: %v", err)
	}
	if !res.Known {
		t.Fatalf("reset did not resolve: %v", res.Notes)
	}
	if want := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC); !planned.ResetAt.Equal(want) {
		t.Fatalf("planned reset = %s, want %s", planned.ResetAt.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestSaveRestoreComplete(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	sched, _ := newTestScheduler(t, start, store)

	planned, _, err := sched.Plan("sess-1", 1, phrase("12pm", "Europe/Berlin"), start)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	saved, err := sched.Save(ctx, planned)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.SessionID != planned.SessionID {
		t.Fatalf("saved session = %q", saved.SessionID)
	}

	restored, err := sched.Restore(ctx)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(restored) != 1 || restored[0].SessionID != "sess-1" {
		t.Fatalf("Restore = %+v", restored)
	}
	if !restored[0].ResetAt.Equal(planned.ResetAt) {
		t.Fatalf("restored reset = %s, want %s", restored[0].ResetAt, planned.ResetAt)
	}

	if err := sched.Complete(ctx, "sess-1"); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	restored, err = sched.Restore(ctx)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(restored) != 0 {
		t.Fatalf("Complete left %d schedules", len(restored))
	}
}

// TestNilStoreIsInert documents that an unconfigured store degrades to
// in-memory-only behaviour instead of panicking.
func TestNilStoreIsInert(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	sched, _ := newTestScheduler(t, start, nil)

	if _, err := sched.Save(ctx, Schedule{SessionID: "a"}); err != nil {
		t.Fatalf("Save with no store: %v", err)
	}
	restored, err := sched.Restore(ctx)
	if err != nil {
		t.Fatalf("Restore with no store: %v", err)
	}
	if restored != nil {
		t.Fatalf("Restore with no store = %+v, want nil", restored)
	}
	if err := sched.Complete(ctx, "a"); err != nil {
		t.Fatalf("Complete with no store: %v", err)
	}
}

func TestNext(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	sched, _ := newTestScheduler(t, start, nil)

	schedules := []Schedule{
		{SessionID: "late", ResetAt: start.Add(3 * time.Hour)},
		{SessionID: "no-deadline"},
		{SessionID: "soon", ResetAt: start.Add(time.Hour)},
		{SessionID: "poll", Unknown: true, NextPollAt: start.Add(30 * time.Minute)},
	}

	next, ok := sched.Next(schedules, start)
	if !ok || next.SessionID != "poll" {
		t.Fatalf("Next = %q, %v, want poll", next.SessionID, ok)
	}

	// A schedule with no deadline is skipped rather than returned.
	if _, ok := sched.Next([]Schedule{{SessionID: "no-deadline"}}, start); ok {
		t.Fatal("Next must skip schedules with no deadline")
	}
	if _, ok := sched.Next(nil, start); ok {
		t.Fatal("Next on an empty list must report false")
	}

	// Equal deadlines are broken by session ID so the order is deterministic.
	tie, ok := sched.Next([]Schedule{
		{SessionID: "b", ResetAt: start.Add(time.Hour)},
		{SessionID: "a", ResetAt: start.Add(time.Hour)},
	}, start)
	if !ok || tie.SessionID != "a" {
		t.Fatalf("tie-break = %q, want a", tie.SessionID)
	}
}

func TestWaitBlocksUntilDue(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	sched, fake := newTestScheduler(t, start, nil)

	due := start.Add(time.Hour)
	done := make(chan time.Time, 1)
	errs := make(chan error, 1)
	go func() {
		at, err := sched.Wait(context.Background(), Schedule{SessionID: "s", ResetAt: due})
		errs <- err
		done <- at
	}()

	// Wait until the waiter has registered its timer, then step the fake clock.
	deadline := time.Now().Add(time.Second)
	for !fake.HasWaiters() && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	fake.Advance(59 * time.Minute)
	select {
	case <-done:
		t.Fatal("Wait returned before the reset time")
	case <-time.After(20 * time.Millisecond):
	}

	fake.Advance(2 * time.Minute)
	select {
	case err := <-errs:
		if err != nil {
			t.Fatalf("Wait: %v", err)
		}
		if at := <-done; at.Before(due) {
			t.Fatalf("Wait woke at %s, before %s", at.Format(time.RFC3339), due.Format(time.RFC3339))
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not wake after the reset time passed")
	}
}

func TestWaitAlreadyDue(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	sched, _ := newTestScheduler(t, start, nil)

	at, err := sched.Wait(context.Background(), Schedule{SessionID: "s", ResetAt: start.Add(-time.Minute)})
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !at.Equal(start) {
		t.Fatalf("Wait returned %s, want the current time %s", at.Format(time.RFC3339), start.Format(time.RFC3339))
	}
}

func TestWaitCancellation(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	sched, _ := newTestScheduler(t, start, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := sched.Wait(ctx, Schedule{SessionID: "s", ResetAt: start.Add(time.Hour)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait error = %v, want context.Canceled", err)
	}
}

func TestWaitRejectsBadInput(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	sched, _ := newTestScheduler(t, start, nil)

	if _, err := sched.Wait(nil, Schedule{SessionID: "s", ResetAt: start}); err == nil {
		t.Fatal("a nil context must be rejected")
	}
	if _, err := sched.Wait(context.Background(), Schedule{SessionID: "s"}); !errors.Is(err, ErrNoDeadline) {
		t.Fatalf("Wait with no deadline error = %v, want ErrNoDeadline", err)
	}
}

// TestAdvanceBacksOffAndPersists proves the poll counter is persisted, so a
// restart continues the backoff instead of starting over at five minutes.
func TestAdvanceBacksOffAndPersists(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	sched, _ := newTestScheduler(t, start, store)

	current, _, err := sched.Plan("sess-1", 1, detector.ResetPhrase{}, start)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, err := sched.Save(ctx, current); err != nil {
		t.Fatalf("Save: %v", err)
	}

	wantDelays := []time.Duration{10 * time.Minute, 20 * time.Minute, 30 * time.Minute, 30 * time.Minute}
	now := start
	for i, wantDelay := range wantDelays {
		now = now.Add(sched.IntervalFor(current))
		current, err = sched.Advance(ctx, current, now)
		if err != nil {
			t.Fatalf("Advance %d: %v", i, err)
		}
		if current.Attempts != i+1 {
			t.Fatalf("attempts = %d, want %d", current.Attempts, i+1)
		}
		if got := current.NextPollAt.Sub(now); got != wantDelay {
			t.Fatalf("poll %d delay = %s, want %s", i+1, got, wantDelay)
		}
	}

	// The persisted row carries the attempt count and poll time.
	restored, err := sched.Restore(ctx)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(restored) != 1 {
		t.Fatalf("restored %d schedules", len(restored))
	}
	if restored[0].Attempts != len(wantDelays) {
		t.Fatalf("persisted attempts = %d, want %d", restored[0].Attempts, len(wantDelays))
	}
	if !restored[0].NextPollAt.Equal(current.NextPollAt) {
		t.Fatalf("persisted next poll = %s, want %s", restored[0].NextPollAt, current.NextPollAt)
	}
}

func TestAdvanceIgnoresKnownReset(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	sched, _ := newTestScheduler(t, start, nil)

	known := Schedule{SessionID: "s", ResetAt: start.Add(time.Hour)}
	got, err := sched.Advance(context.Background(), known, start)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if got.Attempts != 0 || !got.NextPollAt.IsZero() {
		t.Fatalf("a known-reset schedule must not be polled: %+v", got)
	}
}

func TestPollDelay(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	sched, _ := newTestScheduler(t, start, nil)

	if got := sched.PollDelay(1); got != DefaultInitialInterval {
		t.Fatalf("PollDelay(1) = %s", got)
	}
	if got := sched.IntervalFor(Schedule{Attempts: 2}); got != 20*time.Minute {
		t.Fatalf("IntervalFor(attempts=2) = %s, want 20m", got)
	}
	if got := sched.IntervalFor(Schedule{}); got != DefaultInitialInterval {
		t.Fatalf("IntervalFor(fresh) = %s, want the initial interval", got)
	}
}

func TestOptionsAndResolveDelegation(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	berlin := mustLocation(t, "Europe/Berlin")
	fake := clock.NewFake(start)
	sched, err := New(Config{Clock: fake, Location: berlin, LocationName: "Europe/Berlin"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if sched.Options().LocationName != "Europe/Berlin" {
		t.Fatalf("Options().LocationName = %q", sched.Options().LocationName)
	}
	res := sched.Resolve(detector.ResetPhrase{Present: true, Expression: "12pm", Raw: "12pm", Timezone: "Europe/Berlin"}, start)
	if !res.Known {
		t.Fatalf("Resolve: %v", res.Notes)
	}
	// 12:00 CEST is 10:00Z.
	if want := time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC); !res.At.Equal(want) {
		t.Fatalf("resolved %s, want %s", res.At.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}
