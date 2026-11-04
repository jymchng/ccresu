package scheduler

import (
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/detector"
)

// phrase builds a reset phrase the way the detector would.
func phrase(expr, zone string) detector.ResetPhrase {
	p := detector.ResetPhrase{Present: true, Expression: expr, Timezone: zone}
	p.Raw = expr
	if zone != "" {
		p.Raw = expr + " (" + zone + ")"
	}
	return p
}

func TestResolveTimeOfDay(t *testing.T) {
	t.Parallel()

	// A Monday, 09:00 UTC.
	now := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		expr     string
		want     time.Time
		wantKind Kind
	}{
		{"12pm", "12pm", time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC), KindTimeOfDay},
		{"noon", "noon", time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC), KindTimeOfDay},
		{"midday", "midday", time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC), KindTimeOfDay},
		{"quarter to four", "3:45pm", time.Date(2026, 6, 15, 15, 45, 0, 0, time.UTC), KindTimeOfDay},
		{"upper case", "3:45 PM", time.Date(2026, 6, 15, 15, 45, 0, 0, time.UTC), KindTimeOfDay},
		{"dotted", "3:45 p.m.", time.Date(2026, 6, 15, 15, 45, 0, 0, time.UTC), KindTimeOfDay},
		{"24 hour", "14:00", time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC), KindTimeOfDay},
		{"already passed rolls a day", "8am", time.Date(2026, 6, 16, 8, 0, 0, 0, time.UTC), KindTimeOfDay},
		{"midnight rolls a day", "midnight", time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC), KindTimeOfDay},
		{"12am is midnight", "12am", time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC), KindTimeOfDay},
		{"now exactly rolls a day", "9am", time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC), KindTimeOfDay},
		{"tomorrow", "tomorrow", time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC), KindTimeOfDay},
		{"tomorrow at a time", "tomorrow at 9am", time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC), KindTimeOfDay},
		{"tomorrow with comma", "tomorrow, 9am", time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC), KindTimeOfDay},
		{"time then day word", "3:45pm tomorrow", time.Date(2026, 6, 16, 15, 45, 0, 0, time.UTC), KindTimeOfDay},
		{"tonight defaults late", "tonight", time.Date(2026, 6, 15, 23, 59, 0, 0, time.UTC), KindTimeOfDay},
		{"today rolls when passed", "today", time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC), KindTimeOfDay},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := Resolve(phrase(tc.expr, ""), now, DefaultOptions())
			if !res.Known {
				t.Fatalf("%q did not resolve: %v", tc.expr, res.Notes)
			}
			if res.Kind != tc.wantKind {
				t.Fatalf("%q kind = %q, want %q", tc.expr, res.Kind, tc.wantKind)
			}
			if !res.At.Equal(tc.want) {
				t.Fatalf("%q resolved to %s, want %s", tc.expr, res.At.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
			if res.At.Location() != time.UTC {
				t.Fatalf("%q resolved outside UTC: %v", tc.expr, res.At.Location())
			}
		})
	}
}

func TestResolveWeekday(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC) // Monday
	tests := []struct {
		expr string
		want time.Time
	}{
		{"Monday 9am", time.Date(2026, 6, 22, 9, 0, 0, 0, time.UTC)},  // today's time has passed
		{"tuesday 9am", time.Date(2026, 6, 16, 9, 0, 0, 0, time.UTC)}, //
		{"Wed", time.Date(2026, 6, 17, 0, 0, 0, 0, time.UTC)},         // midnight assumed
		{"thursday at 3pm", time.Date(2026, 6, 18, 15, 0, 0, 0, time.UTC)},
		{"friday", time.Date(2026, 6, 19, 0, 0, 0, 0, time.UTC)},
		{"saturday 10:30am", time.Date(2026, 6, 20, 10, 30, 0, 0, time.UTC)},
		{"sunday", time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC)},
	}

	for _, tc := range tests {
		t.Run(tc.expr, func(t *testing.T) {
			t.Parallel()
			res := Resolve(phrase(tc.expr, ""), now, DefaultOptions())
			if !res.Known {
				t.Fatalf("%q did not resolve: %v", tc.expr, res.Notes)
			}
			if res.Kind != KindWeekday {
				t.Fatalf("%q kind = %q, want weekday", tc.expr, res.Kind)
			}
			if !res.At.Equal(tc.want) {
				t.Fatalf("%q resolved to %s, want %s", tc.expr, res.At.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}

func TestResolveDates(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		expr string
		want time.Time
	}{
		{"month day", "June 16", time.Date(2026, 6, 16, 0, 0, 0, 0, time.UTC)},
		{"abbreviated month", "Jul 1", time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)},
		{"with ordinal", "August 3rd", time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)},
		{"same day is due now", "June 15", time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)},
		{"passed date rolls a year", "Jan 5", time.Date(2027, 1, 5, 0, 0, 0, 0, time.UTC)},
		{"explicit year", "March 5, 2027", time.Date(2027, 3, 5, 0, 0, 0, 0, time.UTC)},
		{"explicit year and time", "March 5, 2027 at 9am", time.Date(2027, 3, 5, 9, 0, 0, 0, time.UTC)},
		{"past explicit date stays", "2026-01-02", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		{"iso date", "2026-06-20", time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)},
		{"iso with time", "2026-06-20 09:30", time.Date(2026, 6, 20, 9, 30, 0, 0, time.UTC)},
		{"iso with seconds", "2026-06-20T09:30:15", time.Date(2026, 6, 20, 9, 30, 0, 0, time.UTC)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := Resolve(phrase(tc.expr, ""), now, DefaultOptions())
			if !res.Known {
				t.Fatalf("%q did not resolve: %v", tc.expr, res.Notes)
			}
			if res.Kind != KindDate {
				t.Fatalf("%q kind = %q, want date", tc.expr, res.Kind)
			}
			if !res.At.Equal(tc.want) {
				t.Fatalf("%q resolved to %s, want %s", tc.expr, res.At.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}

func TestResolveRelative(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		expr string
		want time.Duration
	}{
		{"4 hours", 4 * time.Hour},
		{"in 4 hours", 4 * time.Hour},
		{"3h 20m", 3*time.Hour + 20*time.Minute},
		{"90 minutes", 90 * time.Minute},
		{"45m", 45 * time.Minute},
		{"30 seconds", 30 * time.Second},
		{"2 hours and 13 minutes", 2*time.Hour + 13*time.Minute},
		{"1 hour, 5 minutes", time.Hour + 5*time.Minute},
	}

	for _, tc := range tests {
		t.Run(tc.expr, func(t *testing.T) {
			t.Parallel()
			res := Resolve(phrase(tc.expr, ""), now, DefaultOptions())
			if !res.Known {
				t.Fatalf("%q did not resolve: %v", tc.expr, res.Notes)
			}
			if res.Kind != KindRelative {
				t.Fatalf("%q kind = %q, want relative", tc.expr, res.Kind)
			}
			if want := now.Add(tc.want); !res.At.Equal(want) {
				t.Fatalf("%q resolved to %s, want %s", tc.expr, res.At.Format(time.RFC3339), want.Format(time.RFC3339))
			}
			// A duration is absolute, so it names no location.
			if res.LocationName != "" {
				t.Fatalf("%q reported location %q, want none", tc.expr, res.LocationName)
			}
		})
	}
}

func TestResolveEpoch(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	at := time.Date(2026, 6, 15, 18, 30, 0, 0, time.UTC)
	res := Resolve(detector.ResetPhrase{Present: true, EpochUnix: at.Unix(), Raw: "1781"}, now, DefaultOptions())
	if !res.Known || res.Kind != KindEpoch {
		t.Fatalf("epoch did not resolve: %+v", res)
	}
	if !res.At.Equal(at) {
		t.Fatalf("epoch resolved to %s, want %s", res.At.Format(time.RFC3339), at.Format(time.RFC3339))
	}
	if res.LocationName != "UTC" {
		t.Fatalf("epoch location = %q", res.LocationName)
	}
}

func TestResolveTimezones(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		zone    string
		want    time.Time
		wantLoc string
	}{
		{
			name:    "iana zone puts the wall clock in that zone",
			zone:    "Europe/Berlin", // UTC+2 in June
			want:    time.Date(2026, 6, 15, 10, 0, 0, 0, time.UTC),
			wantLoc: "Europe/Berlin",
		},
		{
			name:    "abbreviation is a fixed offset",
			zone:    "PST",
			want:    time.Date(2026, 6, 15, 20, 0, 0, 0, time.UTC),
			wantLoc: "PST",
		},
		{
			name:    "india half hour offset",
			zone:    "Asia/Kolkata", // 09:00Z is already 14:30 there, so noon has passed
			want:    time.Date(2026, 6, 16, 6, 30, 0, 0, time.UTC),
			wantLoc: "Asia/Kolkata",
		},
		{
			name:    "tokyo",
			zone:    "Asia/Tokyo", // 09:00Z is already 18:00 there, so noon has passed
			want:    time.Date(2026, 6, 16, 3, 0, 0, 0, time.UTC),
			wantLoc: "Asia/Tokyo",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := Resolve(phrase("12pm", tc.zone), now, DefaultOptions())
			if !res.Known {
				t.Fatalf("did not resolve: %v", res.Notes)
			}
			if !res.At.Equal(tc.want) {
				t.Fatalf("resolved to %s, want %s", res.At.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
			if res.LocationName != tc.wantLoc {
				t.Fatalf("location = %q, want %q", res.LocationName, tc.wantLoc)
			}
			if !strings.Contains(res.Local, "12:00") {
				t.Fatalf("local wall clock = %q, want 12:00 in the original zone", res.Local)
			}
		})
	}
}

func TestResolveUnknownAndInvalid(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		input detector.ResetPhrase
	}{
		{"not present", detector.ResetPhrase{}},
		{"empty expression", detector.ResetPhrase{Present: true, Expression: "   "}},
		{"gibberish", phrase("soonish", "")},
		{"unknown zone", phrase("12pm", "Mars/Olympus_Mons")},
		{"duration with trailing words", phrase("4 hours ago", "")},
		{"impossible hour", phrase("99pm", "")},
		{"impossible minute", phrase("9:99", "")},
		{"unknown day word tail", phrase("tomorrow whenever", "")},
		{"weekday with bad time", phrase("Monday 99pm", "")},
		{"month with bad day", phrase("June 45", "")},
		{"month with bad time", phrase("June 5 at 99pm", "")},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res := Resolve(tc.input, now, DefaultOptions())
			if res.Known {
				t.Fatalf("expected no resolution, got %s", res.At.Format(time.RFC3339))
			}
			if res.Kind != KindUnknown {
				t.Fatalf("kind = %q, want unknown", res.Kind)
			}
			if len(res.Notes) == 0 {
				t.Fatal("an unresolved phrase must explain itself in Notes")
			}
		})
	}
}

func TestResolvePreservesPhrase(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	p := phrase("12pm", "Europe/Berlin")
	res := Resolve(p, now, DefaultOptions())
	if res.Phrase != p.Raw {
		t.Fatalf("Phrase = %q, want %q", res.Phrase, p.Raw)
	}
}

func TestOptionsDefaults(t *testing.T) {
	t.Parallel()

	opts := Options{}.withDefaults()
	if opts.Location != time.UTC {
		t.Fatalf("default location = %v, want UTC", opts.Location)
	}
	if opts.LocationName != "UTC" {
		t.Fatalf("default location name = %q, want UTC", opts.LocationName)
	}
	if DefaultOptions().PreferEarlierOnAmbiguous {
		t.Fatal("the default must prefer the later instant, not the earlier one")
	}

	berlin := mustLocation(t, "Europe/Berlin")
	named := Options{Location: berlin}.withDefaults()
	if named.LocationName != "Europe/Berlin" {
		t.Fatalf("derived location name = %q", named.LocationName)
	}
}

func TestPlanCivilDirect(t *testing.T) {
	t.Parallel()

	today := wall{year: 2026, month: 6, day: 15}

	if plan, ok := planCivil("noon", today); !ok || plan.wall.hour != 12 || !plan.rollable {
		t.Fatalf("noon plan = %+v, ok=%v", plan, ok)
	}
	if plan, ok := planCivil("midnight", today); !ok || plan.wall.hour != 0 {
		t.Fatalf("midnight plan = %+v, ok=%v", plan, ok)
	}
	if _, ok := planCivil("whenever", today); ok {
		t.Fatal("gibberish must not plan")
	}
}

func TestParseTimeOfDay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in     string
		hour   int
		minute int
		wantOK bool
	}{
		{"12pm", 12, 0, true},
		{"12am", 0, 0, true},
		{"1pm", 13, 0, true},
		{"11:59pm", 23, 59, true},
		{"00:00", 0, 0, true},
		{"23:59", 23, 59, true},
		{"9", 9, 0, true},
		{"13pm", 0, 0, false},
		{"0am", 0, 0, false},
		{"24:00", 0, 0, false},
		{"9:60", 0, 0, false},
		{"noon", 0, 0, false},
		{"", 0, 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			hour, minute, ok := parseTimeOfDay(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("parseTimeOfDay(%q) ok = %v, want %v", tc.in, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if hour != tc.hour || minute != tc.minute {
				t.Fatalf("parseTimeOfDay(%q) = %02d:%02d, want %02d:%02d", tc.in, hour, minute, tc.hour, tc.minute)
			}
		})
	}
}

func TestParseDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in     string
		want   time.Duration
		wantOK bool
	}{
		{"4 hours", 4 * time.Hour, true},
		{"1 hour", time.Hour, true},
		{"2 hrs", 2 * time.Hour, true},
		{"90 minutes", 90 * time.Minute, true},
		{"3 mins", 3 * time.Minute, true},
		{"30 seconds", 30 * time.Second, true},
		{"10 secs", 10 * time.Second, true},
		{"3h 20m", 3*time.Hour + 20*time.Minute, true},
		{"3h20m", 3*time.Hour + 20*time.Minute, true},
		{"1 hour 30 minutes 15 seconds", time.Hour + 30*time.Minute + 15*time.Second, true},
		{"", 0, false},
		{"4 hours ago", 0, false},
		{"soon", 0, false},
		{"0 hours", 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got, ok := parseDuration(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("parseDuration(%q) ok = %v, want %v", tc.in, ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Fatalf("parseDuration(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestWeekdayAndMonthIndex(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]time.Weekday{
		"sun": time.Sunday, "sunday": time.Sunday,
		"mon": time.Monday, "monday": time.Monday,
		"tue": time.Tuesday, "tues": time.Tuesday, "tuesday": time.Tuesday,
		"wed": time.Wednesday, "wednesday": time.Wednesday,
		"thu": time.Thursday, "thur": time.Thursday, "thurs": time.Thursday, "thursday": time.Thursday,
		"fri": time.Friday, "friday": time.Friday,
		"sat": time.Saturday, "saturday": time.Saturday,
	} {
		got, ok := weekdayIndex(name)
		if !ok || got != want {
			t.Fatalf("weekdayIndex(%q) = %v, %v", name, got, ok)
		}
	}
	if _, ok := weekdayIndex("funday"); ok {
		t.Fatal("weekdayIndex(funday) should not resolve")
	}

	for name, want := range map[string]time.Month{
		"jan": time.January, "january": time.January,
		"feb": time.February, "sep": time.September, "sept": time.September,
		"dec": time.December, "december": time.December,
	} {
		got, ok := monthIndex(name)
		if !ok || got != want {
			t.Fatalf("monthIndex(%q) = %v, %v", name, got, ok)
		}
	}
	if _, ok := monthIndex("ja"); ok {
		t.Fatal("a two-letter month must not resolve")
	}
	if _, ok := monthIndex("xyz"); ok {
		t.Fatal("monthIndex(xyz) should not resolve")
	}
}

func TestNormalizeExpression(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"  12PM  ":         "12pm",
		"Tomorrow  At 9am": "tomorrow at 9am",
		"3:45 p.m.":        "3:45 p.m",
	}
	for in, want := range tests {
		if got := normalizeExpression(in); got != want {
			t.Fatalf("normalizeExpression(%q) = %q, want %q", in, got, want)
		}
	}
}
