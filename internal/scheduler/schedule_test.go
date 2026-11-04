package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestScheduleDueAtAndDue(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		sched      Schedule
		wantOK     bool
		wantDue    bool
		wantString string
	}{
		{
			name:       "known reset in the future",
			sched:      Schedule{SessionID: "s", ResetAt: now.Add(time.Hour)},
			wantOK:     true,
			wantDue:    false,
			wantString: "reset at",
		},
		{
			name:       "known reset exactly now",
			sched:      Schedule{SessionID: "s", ResetAt: now},
			wantOK:     true,
			wantDue:    true,
			wantString: "reset at",
		},
		{
			name:       "known reset in the past",
			sched:      Schedule{SessionID: "s", ResetAt: now.Add(-time.Hour)},
			wantOK:     true,
			wantDue:    true,
			wantString: "reset at",
		},
		{
			name:       "unknown reset polls at next poll",
			sched:      Schedule{SessionID: "s", Unknown: true, NextPollAt: now.Add(time.Minute)},
			wantOK:     true,
			wantDue:    false,
			wantString: "unknown reset",
		},
		{
			name:       "unknown reset with no poll time is not schedulable",
			sched:      Schedule{SessionID: "s", Unknown: true},
			wantOK:     false,
			wantDue:    false,
			wantString: "unknown reset",
		},
		{
			name:       "known reset with no instant is not schedulable",
			sched:      Schedule{SessionID: "s"},
			wantOK:     false,
			wantDue:    false,
			wantString: "reset at",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			at, ok := tc.sched.DueAt()
			if ok != tc.wantOK {
				t.Fatalf("DueAt ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && at.Location() != time.UTC {
				t.Fatalf("DueAt location = %v, want UTC", at.Location())
			}
			if got := tc.sched.Due(now); got != tc.wantDue {
				t.Fatalf("Due = %v, want %v", got, tc.wantDue)
			}
			if !strings.Contains(tc.sched.String(), tc.wantString) {
				t.Fatalf("String() = %q, want it to mention %q", tc.sched.String(), tc.wantString)
			}
		})
	}
}

// TestScheduleDueAtConvertsToUTC proves a schedule stored in a local zone still
// compares correctly, because everything internal is absolute.
func TestScheduleDueAtConvertsToUTC(t *testing.T) {
	t.Parallel()

	berlin := mustLocation(t, "Europe/Berlin")
	local := time.Date(2026, 6, 15, 14, 0, 0, 0, berlin) // 12:00Z
	sched := Schedule{SessionID: "s", ResetAt: local}

	at, ok := sched.DueAt()
	if !ok {
		t.Fatal("DueAt reported no deadline")
	}
	if at.Location() != time.UTC {
		t.Fatalf("DueAt location = %v, want UTC", at.Location())
	}
	if want := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC); !at.Equal(want) {
		t.Fatalf("DueAt = %s, want %s", at.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestScheduleWait(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

	remaining, ok := Schedule{ResetAt: now.Add(90 * time.Minute)}.Wait(now)
	if !ok || remaining != 90*time.Minute {
		t.Fatalf("Wait = %s, %v", remaining, ok)
	}
	// A past deadline reports zero rather than a negative duration.
	remaining, ok = Schedule{ResetAt: now.Add(-time.Hour)}.Wait(now)
	if !ok || remaining != 0 {
		t.Fatalf("Wait for a past deadline = %s, %v", remaining, ok)
	}
	if _, ok := (Schedule{}).Wait(now); ok {
		t.Fatal("Wait on a schedule with no deadline must report false")
	}
}

func TestMemoryStoreRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewMemoryStore()

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	first := Schedule{SessionID: "a", ResetAt: now.Add(time.Hour), UpdatedAt: now}
	saved, err := store.SaveSchedule(ctx, first)
	if err != nil {
		t.Fatalf("SaveSchedule: %v", err)
	}
	if saved.CreatedAt.IsZero() {
		t.Fatal("CreatedAt was not filled in")
	}

	loaded, err := store.LoadSchedules(ctx)
	if err != nil {
		t.Fatalf("LoadSchedules: %v", err)
	}
	if len(loaded) != 1 || loaded[0].SessionID != "a" {
		t.Fatalf("LoadSchedules = %+v", loaded)
	}

	// Saving again replaces rather than duplicates, and keeps the original
	// creation time.
	second := first
	second.ResetAt = now.Add(2 * time.Hour)
	saved, err = store.SaveSchedule(ctx, second)
	if err != nil {
		t.Fatalf("SaveSchedule (replace): %v", err)
	}
	if !saved.CreatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("CreatedAt changed on replace: %s", saved.CreatedAt)
	}
	loaded, err = store.LoadSchedules(ctx)
	if err != nil {
		t.Fatalf("LoadSchedules: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("saving the same session twice produced %d rows", len(loaded))
	}

	if err := store.DeleteSchedule(ctx, "a"); err != nil {
		t.Fatalf("DeleteSchedule: %v", err)
	}
	loaded, err = store.LoadSchedules(ctx)
	if err != nil {
		t.Fatalf("LoadSchedules: %v", err)
	}
	if len(loaded) != 0 {
		t.Fatalf("DeleteSchedule left %d rows", len(loaded))
	}
	// Deleting an absent schedule is not an error: the wait is over either way.
	if err := store.DeleteSchedule(ctx, "absent"); err != nil {
		t.Fatalf("DeleteSchedule(absent): %v", err)
	}
}

func TestMemoryStoreOrdering(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

	for _, sched := range []Schedule{
		{SessionID: "late", ResetAt: now.Add(3 * time.Hour), UpdatedAt: now},
		{SessionID: "poll", Unknown: true, NextPollAt: now.Add(30 * time.Minute), UpdatedAt: now},
		{SessionID: "soon", ResetAt: now.Add(time.Hour), UpdatedAt: now},
	} {
		if _, err := store.SaveSchedule(ctx, sched); err != nil {
			t.Fatalf("SaveSchedule: %v", err)
		}
	}

	loaded, err := store.LoadSchedules(ctx)
	if err != nil {
		t.Fatalf("LoadSchedules: %v", err)
	}
	want := []string{"poll", "soon", "late"}
	if len(loaded) != len(want) {
		t.Fatalf("loaded %d schedules, want %d", len(loaded), len(want))
	}
	for i, id := range want {
		if loaded[i].SessionID != id {
			t.Fatalf("schedule %d = %q, want %q (earliest deadline first)", i, loaded[i].SessionID, id)
		}
	}
}

func TestMemoryStoreRejectsEmptySession(t *testing.T) {
	t.Parallel()

	if _, err := NewMemoryStore().SaveSchedule(context.Background(), Schedule{}); err == nil {
		t.Fatal("an empty session id must be rejected")
	}
}

func TestMemoryStoreFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewMemoryStore()
	boom := errors.New("boom")
	store.FailWith(boom)

	if _, err := store.SaveSchedule(ctx, Schedule{SessionID: "a"}); !errors.Is(err, boom) {
		t.Fatalf("SaveSchedule error = %v, want boom", err)
	}
	if _, err := store.LoadSchedules(ctx); !errors.Is(err, boom) {
		t.Fatalf("LoadSchedules error = %v, want boom", err)
	}
	if err := store.DeleteSchedule(ctx, "a"); !errors.Is(err, boom) {
		t.Fatalf("DeleteSchedule error = %v, want boom", err)
	}
}

func TestMemoryStoreConcurrentAccess(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewMemoryStore()
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			id := string(rune('a' + i))
			for j := 0; j < 20; j++ {
				_, _ = store.SaveSchedule(ctx, Schedule{SessionID: id, ResetAt: now.Add(time.Duration(j) * time.Minute), UpdatedAt: now})
				_, _ = store.LoadSchedules(ctx)
			}
			_ = store.DeleteSchedule(ctx, id)
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

func TestStorageStoreNilStore(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	adapter := NewStorageStore(nil)

	if _, err := adapter.SaveSchedule(ctx, Schedule{SessionID: "a"}); err == nil {
		t.Fatal("SaveSchedule with no storage must fail")
	}
	if _, err := adapter.LoadSchedules(ctx); err == nil {
		t.Fatal("LoadSchedules with no storage must fail")
	}
	if err := adapter.DeleteSchedule(ctx, "a"); err == nil {
		t.Fatal("DeleteSchedule with no storage must fail")
	}
}

// TestScheduleRoundTripThroughStorageTypes proves the conversion between the
// scheduler's view and the persistence representation is lossless.
func TestScheduleRoundTripThroughStorageTypes(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	original := Schedule{
		SessionID:     "sess",
		UsageLimitID:  7,
		ResetAt:       now.Add(time.Hour),
		ResetTimezone: "Europe/Berlin",
		ResetRaw:      "12pm (Europe/Berlin)",
		Unknown:       false,
		Attempts:      3,
		NextPollAt:    now.Add(10 * time.Minute),
		CreatedAt:     now,
		UpdatedAt:     now.Add(time.Minute),
	}

	got := fromStorageSchedule(toStorageSchedule(original))
	if got.SessionID != original.SessionID ||
		got.UsageLimitID != original.UsageLimitID ||
		!got.ResetAt.Equal(original.ResetAt) ||
		got.ResetTimezone != original.ResetTimezone ||
		got.ResetRaw != original.ResetRaw ||
		got.Unknown != original.Unknown ||
		got.Attempts != original.Attempts ||
		!got.NextPollAt.Equal(original.NextPollAt) ||
		!got.CreatedAt.Equal(original.CreatedAt) ||
		!got.UpdatedAt.Equal(original.UpdatedAt) {
		t.Fatalf("round trip lost data:\n got %+v\nwant %+v", got, original)
	}
}
