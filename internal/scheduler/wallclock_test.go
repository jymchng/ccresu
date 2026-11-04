package scheduler

import (
	"testing"
	"time"
)

// mustLocation loads a location or fails the test.
func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

func TestResolveWallOrdinaryTimes(t *testing.T) {
	t.Parallel()

	berlin := mustLocation(t, "Europe/Berlin")
	tests := []struct {
		name    string
		when    wall
		wantUTC time.Time
	}{
		{
			name:    "winter is cet (+1)",
			when:    wall{year: 2026, month: 1, day: 15, hour: 12, minute: 0},
			wantUTC: time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC),
		},
		{
			name:    "summer is cest (+2)",
			when:    wall{year: 2026, month: 7, day: 15, hour: 12, minute: 0},
			wantUTC: time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			at, dstCase := resolveWall(tc.when, berlin, false)
			if dstCase != DSTNone {
				t.Fatalf("DST case = %q, want none", dstCase)
			}
			if !at.Equal(tc.wantUTC) {
				t.Fatalf("resolved %s, want %s", at.Format(time.RFC3339), tc.wantUTC.Format(time.RFC3339))
			}
			if at.Location() != time.UTC {
				t.Fatalf("resolved location = %v, want UTC", at.Location())
			}
		})
	}
}

// TestResolveWallSpringForwardGap covers 2026-03-29 in Berlin, when 02:00-03:00
// does not exist. Waking after the gap is safe; waking before it is not.
func TestResolveWallSpringForwardGap(t *testing.T) {
	t.Parallel()

	berlin := mustLocation(t, "Europe/Berlin")
	at, dstCase := resolveWall(wall{year: 2026, month: 3, day: 29, hour: 2, minute: 30}, berlin, false)
	if dstCase != DSTGap {
		t.Fatalf("DST case = %q, want gap", dstCase)
	}
	// 03:30 CEST is the instant immediately after the missing hour.
	want := time.Date(2026, 3, 29, 1, 30, 0, 0, time.UTC)
	if !at.Equal(want) {
		t.Fatalf("resolved %s, want %s", at.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	if got := wallOf(at, berlin); got.hour != 3 || got.minute != 30 {
		t.Fatalf("local wall clock = %s, want 03:30", got.String())
	}
}

// TestResolveWallFallBackAmbiguous covers 2026-10-25 in Berlin, when 02:30
// occurs twice. The later instant is chosen by default so a recovery cannot
// start before the limit really reset.
func TestResolveWallFallBackAmbiguous(t *testing.T) {
	t.Parallel()

	berlin := mustLocation(t, "Europe/Berlin")
	when := wall{year: 2026, month: 10, day: 25, hour: 2, minute: 30}

	late, dstCase := resolveWall(when, berlin, false)
	if dstCase != DSTAmbiguous {
		t.Fatalf("DST case = %q, want ambiguous", dstCase)
	}
	early, dstCase := resolveWall(when, berlin, true)
	if dstCase != DSTAmbiguous {
		t.Fatalf("DST case = %q, want ambiguous", dstCase)
	}

	wantEarly := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC) // 02:30 CEST
	wantLate := time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC)  // 02:30 CET
	if !early.Equal(wantEarly) {
		t.Fatalf("earlier resolution = %s, want %s", early.Format(time.RFC3339), wantEarly.Format(time.RFC3339))
	}
	if !late.Equal(wantLate) {
		t.Fatalf("later resolution = %s, want %s", late.Format(time.RFC3339), wantLate.Format(time.RFC3339))
	}
	if !late.After(early) {
		t.Fatal("the default resolution must be the later of the two instants")
	}
	// Both must still show the wall clock the message wrote.
	for _, at := range []time.Time{early, late} {
		if got := wallOf(at, berlin); got.hour != 2 || got.minute != 30 {
			t.Fatalf("local wall clock = %s, want 02:30", got.String())
		}
	}
}

func TestWallArithmetic(t *testing.T) {
	t.Parallel()

	base := wall{year: 2026, month: 3, day: 28, hour: 9, minute: 15}
	if got := base.addDays(1); got.day != 29 || got.hour != 9 || got.minute != 15 {
		t.Fatalf("addDays(1) = %s", got.String())
	}
	// Across a month boundary.
	if got := (wall{year: 2026, month: 1, day: 31}).addDays(1); got.month != time.February || got.day != 1 {
		t.Fatalf("addDays across month = %s", got.String())
	}
	// 29 February normalises forward to 1 March in a non-leap year.
	if got := (wall{year: 2024, month: 2, day: 29}).addYears(1); got.month != time.March || got.day != 1 {
		t.Fatalf("addYears from 29 Feb = %s", got.String())
	}
	// 29 February survives a leap year.
	if got := (wall{year: 2024, month: 2, day: 29}).addYears(4); got.month != time.February || got.day != 29 {
		t.Fatalf("addYears across a leap year = %s", got.String())
	}
	if got := (wall{year: 2026, month: 12, day: 31}).addDays(1); got.year != 2027 || got.month != time.January || got.day != 1 {
		t.Fatalf("addDays across year = %s", got.String())
	}
}

func TestWallWeekday(t *testing.T) {
	t.Parallel()

	// 2026-06-15 is a Monday.
	if got := (wall{year: 2026, month: 6, day: 15}).weekday(); got != time.Monday {
		t.Fatalf("weekday = %v, want Monday", got)
	}
	if got := (wall{year: 2026, month: 6, day: 21}).weekday(); got != time.Sunday {
		t.Fatalf("weekday = %v, want Sunday", got)
	}
}

func TestWallString(t *testing.T) {
	t.Parallel()

	got := (wall{year: 2026, month: 6, day: 5, hour: 9, minute: 7}).String()
	if got != "2026-06-05 09:07" {
		t.Fatalf("String() = %q", got)
	}
}

func TestSameAndBeforeDate(t *testing.T) {
	t.Parallel()

	today := wall{year: 2026, month: 6, day: 15, hour: 9}
	tests := []struct {
		name       string
		other      wall
		wantSame   bool
		wantBefore bool
	}{
		{"same day different time", wall{year: 2026, month: 6, day: 15, hour: 23}, true, false},
		{"earlier day", wall{year: 2026, month: 6, day: 14}, false, true},
		{"later day", wall{year: 2026, month: 6, day: 16}, false, false},
		{"earlier month", wall{year: 2026, month: 5, day: 31}, false, true},
		{"later month", wall{year: 2026, month: 7, day: 1}, false, false},
		{"earlier year", wall{year: 2025, month: 12, day: 31}, false, true},
		{"later year", wall{year: 2027, month: 1, day: 1}, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := sameDate(tc.other, today); got != tc.wantSame {
				t.Fatalf("sameDate = %v, want %v", got, tc.wantSame)
			}
			if got := beforeDate(tc.other, today); got != tc.wantBefore {
				t.Fatalf("beforeDate = %v, want %v", got, tc.wantBefore)
			}
		})
	}
}

func TestJoinNotes(t *testing.T) {
	t.Parallel()

	if got := joinNotes("", "second"); got != "second" {
		t.Fatalf("joinNotes with empty existing = %q", got)
	}
	if got := joinNotes("first", "second"); got != "first; second" {
		t.Fatalf("joinNotes = %q", got)
	}
}
