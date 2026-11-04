package detector

import "testing"

func TestExtractReset(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		in       string
		want     bool
		expr     string
		timezone string
		relative bool
		raw      string
		epoch    int64
	}{
		{
			name:     "canonical am/pm with zone",
			in:       "You've hit your 5-hour limit · resets 12pm (Europe/Berlin)",
			want:     true,
			expr:     "12pm",
			timezone: "Europe/Berlin",
			raw:      "12pm (Europe/Berlin)",
		},
		{
			name:     "weekday with zone",
			in:       "You've hit your weekly limit · resets Monday 9am (UTC)",
			want:     true,
			expr:     "Monday 9am",
			timezone: "UTC",
			raw:      "Monday 9am (UTC)",
		},
		{
			name:     "relative duration",
			in:       "You've run out of extra usage · resets in 4 hours",
			want:     true,
			expr:     "4 hours",
			relative: true,
			raw:      "4 hours",
		},
		{
			name:     "24 hour clock with bare zone",
			in:       "The limit will reset at 15:30 UTC",
			want:     true,
			expr:     "15:30",
			timezone: "UTC",
			raw:      "15:30 (UTC)",
		},
		{
			name:     "12 hour clock with minutes",
			in:       "The limit will reset at 3:00 PM UTC",
			want:     true,
			expr:     "3:00 PM",
			timezone: "UTC",
			raw:      "3:00 PM (UTC)",
		},
		{
			name:     "tomorrow with time and zone",
			in:       "resets tomorrow at 9am (America/New_York)",
			want:     true,
			expr:     "tomorrow at 9am",
			timezone: "America/New_York",
			raw:      "tomorrow at 9am (America/New_York)",
		},
		{
			name:     "noon",
			in:       "resets noon (Europe/Berlin)",
			want:     true,
			expr:     "noon",
			timezone: "Europe/Berlin",
			raw:      "noon (Europe/Berlin)",
		},
		{
			name: "midnight",
			in:   "resets at midnight",
			want: true,
			expr: "midnight",
			raw:  "midnight",
		},
		{
			name: "explicit date",
			in:   "5-hour limit reached. Resets at 2026-06-15 09:00.",
			want: true,
			expr: "2026-06-15 09:00",
			raw:  "2026-06-15 09:00",
		},
		{
			name: "month and day",
			in:   "resets Jan 5 at 9am",
			want: true,
			expr: "Jan 5 at 9am",
			raw:  "Jan 5 at 9am",
		},
		{
			name: "try again in minutes",
			in:   "Usage limit reached, try again in 20 minutes.",
			want: true,
			expr: "20 minutes",
			raw:  "20 minutes",
			// "try again in" leaves the preposition outside the expression
			relative: true,
		},
		{
			name:  "legacy epoch",
			in:    "Claude AI usage limit reached|1730000000",
			want:  true,
			expr:  "1730000000",
			raw:   "1730000000",
			epoch: 1730000000,
		},
		{
			name:  "legacy epoch in milliseconds",
			in:    "Claude AI usage limit reached|1730000000000",
			want:  true,
			expr:  "1730000000000",
			raw:   "1730000000000",
			epoch: 1730000000,
		},
		{
			name:  "legacy epoch in microseconds",
			in:    "Claude AI usage limit reached|1730000000000000",
			want:  true,
			expr:  "1730000000000000",
			raw:   "1730000000000000",
			epoch: 1730000000,
		},
		{
			name: "reset without a time",
			in:   "Goal paused · usage limit reached · send a message after it resets",
			want: false,
		},
		{
			name: "keyword with nothing after it",
			in:   "it resets",
			want: false,
		},
		{
			name: "no reset information",
			in:   "You've hit your limit",
			want: false,
		},
		{
			name: "empty",
			in:   "",
			want: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractReset(tc.in)
			if got.Present != tc.want {
				t.Fatalf("Present = %v, want %v (phrase %+v)", got.Present, tc.want, got)
			}
			if !tc.want {
				if got != (ResetPhrase{}) {
					t.Fatalf("expected a zero phrase, got %+v", got)
				}
				return
			}
			if got.Expression != tc.expr {
				t.Errorf("Expression = %q, want %q", got.Expression, tc.expr)
			}
			if got.Timezone != tc.timezone {
				t.Errorf("Timezone = %q, want %q", got.Timezone, tc.timezone)
			}
			if got.Relative != tc.relative {
				t.Errorf("Relative = %v, want %v", got.Relative, tc.relative)
			}
			if got.EpochUnix != tc.epoch {
				t.Errorf("EpochUnix = %d, want %d", got.EpochUnix, tc.epoch)
			}
			if tc.raw != "" && got.Raw != tc.raw {
				t.Errorf("Raw = %q, want %q", got.Raw, tc.raw)
			}
			if tc.timezone != "" && !got.HasTimezone() {
				t.Errorf("HasTimezone() = false, want true")
			}
		})
	}
}

func TestExtractResetIgnoresAmbiguousWords(t *testing.T) {
	t.Parallel()

	// "tomorrow" must not be mistaken for a timezone.
	got := extractReset("resets 12pm tomorrow")
	if !got.Present {
		t.Fatalf("expected a phrase, got %+v", got)
	}
	if got.Timezone != "" {
		t.Fatalf("Timezone = %q, want empty", got.Timezone)
	}
	if got.Expression != "12pm tomorrow" {
		t.Fatalf("Expression = %q, want %q", got.Expression, "12pm tomorrow")
	}

	// A parenthesised aside is not a timezone.
	aside := extractReset("resets 12pm (approximately)")
	if aside.Timezone != "" {
		t.Fatalf("Timezone = %q, want empty for a non-timezone aside", aside.Timezone)
	}
}

func TestResetPhraseHasTimezone(t *testing.T) {
	t.Parallel()

	if (ResetPhrase{}).HasTimezone() {
		t.Fatal("zero phrase must not report a timezone")
	}
	if !(ResetPhrase{Timezone: "UTC"}).HasTimezone() {
		t.Fatal("phrase with a timezone must report one")
	}
}
