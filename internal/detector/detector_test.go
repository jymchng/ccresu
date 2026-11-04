package detector

import (
	"sync"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/clock"
)

// analysisCase is one end-to-end expectation for Analyze.
type analysisCase struct {
	name string
	in   string

	detected   bool
	class      Classification
	kind       LimitKind
	qualifier  string
	resetExpr  string
	timezone   string
	relative   bool
	pausedGoal bool
	epoch      int64
	patternID  string
	minScore   float64
	maxScore   float64
}

func TestAnalyze(t *testing.T) {
	t.Parallel()

	cases := []analysisCase{
		{
			name: "canonical five hour banner", in: "You've hit your 5-hour limit · resets 12pm (Europe/Berlin)",
			detected: true, class: ClassificationUsageLimit, kind: LimitFiveHour, qualifier: "5-hour",
			resetExpr: "12pm", timezone: "Europe/Berlin", patternID: "canonical-hit-five-hour",
			minScore: 0.99,
		},
		{
			name: "typographic punctuation and terminal wrapping", in: "You\u2019ve hit your 5\u2013hour\nlimit \u00b7 resets 12pm (Europe/Berlin)",
			detected: true, class: ClassificationUsageLimit, kind: LimitFiveHour,
			resetExpr: "12pm", timezone: "Europe/Berlin",
		},
		{
			name: "fullwidth characters", in: "\uff39\uff4f\uff55'\uff56\uff45 \uff48\uff49\uff54 \uff59\uff4f\uff55\uff52 \uff15-\uff48\uff4f\uff55\uff52 \uff4c\uff49\uff4d\uff49\uff54 \u00b7 resets 12pm (Europe/Berlin)",
			detected: true, class: ClassificationUsageLimit, kind: LimitFiveHour,
			resetExpr: "12pm", timezone: "Europe/Berlin",
		},
		{
			name: "ansi colour codes", in: "\x1b[31mYou've hit your 5-hour limit\x1b[0m · resets 12pm (Europe/Berlin)",
			detected: true, class: ClassificationUsageLimit, kind: LimitFiveHour,
			resetExpr: "12pm", timezone: "Europe/Berlin",
		},
		{
			name: "weekly banner", in: "You've hit your weekly limit · resets Monday 9am (UTC)",
			detected: true, class: ClassificationUsageLimit, kind: LimitWeekly, qualifier: "weekly",
			resetExpr: "Monday 9am", timezone: "UTC",
		},
		{
			name: "reached without a reset time", in: "You have reached your 5-hour limit",
			detected: true, class: ClassificationUsageLimit, kind: LimitFiveHour, qualifier: "5-hour",
		},
		{
			name: "daily limit is an other limit", in: "You've exceeded your daily limit",
			detected: true, class: ClassificationUsageLimit, kind: LimitOther, qualifier: "daily",
		},
		{
			name: "personless five hour", in: "5-hour limit reached",
			detected: true, class: ClassificationUsageLimit, kind: LimitFiveHour, qualifier: "5-hour",
		},
		{
			name: "personless weekly", in: "Weekly limit exceeded",
			detected: true, class: ClassificationUsageLimit, kind: LimitWeekly, qualifier: "weekly",
		},
		{
			name: "claude usage limit with reset", in: "Claude usage limit reached. Your limit will reset at 5pm.",
			detected: true, class: ClassificationUsageLimit, kind: LimitOther,
			resetExpr: "5pm",
		},
		{
			name: "paused goal without a reset time", in: "Goal paused · usage limit reached · send a message after it resets",
			detected: true, class: ClassificationUsageLimit, kind: LimitOther,
			pausedGoal: true, patternID: "paused-goal-usage-limit",
		},
		{
			name: "extra usage with a relative reset", in: "You've run out of extra usage · resets in 4 hours",
			detected: true, class: ClassificationUsageLimit, kind: LimitOther, qualifier: "extra usage",
			resetExpr: "4 hours", relative: true,
		},
		{
			name: "legacy epoch form", in: "Claude AI usage limit reached|1730000000",
			detected: true, class: ClassificationUsageLimit, kind: LimitOther,
			resetExpr: "1730000000", epoch: 1730000000, patternID: "epoch-usage-limit",
		},
		{
			name: "opus limit", in: "You've hit your Opus limit",
			detected: true, class: ClassificationUsageLimit, kind: LimitOther, qualifier: "opus",
		},
		{
			name: "message limit with zone", in: "You've hit your message limit · resets 2am (Asia/Singapore)",
			detected: true, class: ClassificationUsageLimit, kind: LimitOther, qualifier: "message",
			resetExpr: "2am", timezone: "Asia/Singapore",
		},
		{
			name: "limit will reset with a bare zone", in: "The limit will reset at 3:00 PM UTC",
			detected: true, class: ClassificationUsageLimit, kind: LimitOther,
			resetExpr: "3:00 PM", timezone: "UTC",
		},
		{
			name: "resets in a duration", in: "Your usage limit resets in 4 hours",
			detected: true, class: ClassificationUsageLimit, kind: LimitOther,
			resetExpr: "4 hours", relative: true,
		},
		{
			name: "explicit date", in: "5-hour limit reached. Resets at 2026-06-15 09:00.",
			detected: true, class: ClassificationUsageLimit, kind: LimitFiveHour,
			resetExpr: "2026-06-15 09:00",
		},
		{
			name: "tomorrow with a time and zone", in: "You've hit your 5-hour limit · resets tomorrow at 9am (America/New_York)",
			detected: true, class: ClassificationUsageLimit, kind: LimitFiveHour,
			resetExpr: "tomorrow at 9am", timezone: "America/New_York",
		},
		{
			name: "noon", in: "You've hit your limit · resets noon (Europe/Berlin)",
			detected: true, class: ClassificationUsageLimit, kind: LimitOther,
			resetExpr: "noon", timezone: "Europe/Berlin",
		},
		{
			name: "try again in minutes", in: "Usage limit reached, try again in 20 minutes.",
			detected: true, class: ClassificationUsageLimit, kind: LimitOther,
			resetExpr: "20 minutes", relative: true,
		},
		{
			name: "quota exhausted", in: "Your quota exceeded for this month",
			detected: true, class: ClassificationUsageLimit, kind: LimitOther,
			minScore: 0.8, maxScore: 0.8, patternID: "quota-exhausted",
		},
		{
			name: "plan maximum", in: "You've reached the maximum for your plan",
			detected: true, class: ClassificationUsageLimit, kind: LimitOther, patternID: "plan-limit",
		},
		{
			name: "banner inside a transcript blob", in: "some earlier output\nYou've hit your 5-hour limit · resets 12pm (Europe/Berlin)\nand more",
			detected: true, class: ClassificationUsageLimit, kind: LimitFiveHour,
			resetExpr: "12pm", timezone: "Europe/Berlin",
		},
		{
			name: "a previous sentence does not demote a real banner", in: "it works well. You've hit your 5-hour limit · resets 12pm (Europe/Berlin)",
			detected: true, class: ClassificationUsageLimit, kind: LimitFiveHour,
			resetExpr: "12pm", timezone: "Europe/Berlin",
		},
		{
			name: "a quoted example earlier does not mask the real banner", in: "```\nYou've hit your 5-hour limit\n```\nYou've hit your 5-hour limit · resets 12pm (Europe/Berlin)",
			detected: true, class: ClassificationUsageLimit, kind: LimitFiveHour,
			resetExpr: "12pm", timezone: "Europe/Berlin", patternID: "canonical-hit-five-hour",
		},
		{
			name: "a described example earlier does not mask the real banner", in: "ccresu detects the phrase \"You've hit your 5-hour limit\". Later: You've hit your 5-hour limit · resets 12pm (Europe/Berlin)",
			detected: true, class: ClassificationUsageLimit, kind: LimitFiveHour,
			resetExpr: "12pm", timezone: "Europe/Berlin",
		},

		// Negative classifications: recognised, but explicitly not a limit.
		{
			name: "invalid api key", in: "Invalid API key · Please run /login",
			class: ClassificationInvalidKey,
		},
		{
			name: "401 with a bad key", in: `API Error: 401 {"error":{"type":"authentication_error","message":"invalid x-api-key"}}`,
			class: ClassificationInvalidKey,
		},
		{
			name: "key described as invalid", in: "Your API key is invalid.",
			class: ClassificationInvalidKey,
		},
		{
			name: "authentication failure", in: "Authentication failed. Please log in.",
			class: ClassificationAuthError,
		},
		{
			name: "connection refused", in: "CONNECTION REFUSED",
			class: ClassificationNetworkError,
		},
		{
			name: "dns failure", in: "fetch failed: ENOTFOUND",
			class: ClassificationNetworkError,
		},
		{
			name: "user cancellation", in: "Operation cancelled by user",
			class: ClassificationUserCancelled,
		},
		{
			name: "interrupted by user", in: "Interrupted by user",
			class: ClassificationUserCancelled,
		},
		{
			name: "goal completed", in: "Goal completed successfully",
			class: ClassificationCompleted,
		},
		{
			name: "all tasks finished", in: "All tasks finished",
			class: ClassificationCompleted,
		},
		{
			name: "session completed", in: "Session completed.",
			class: ClassificationCompleted,
		},
		{
			name: "ordinary output", in: "I've updated the file and the tests pass.",
			class: ClassificationNone,
		},
		{
			name: "empty input", in: "",
			class: ClassificationNone,
		},
		{
			name: "whitespace only", in: "   \n\t  ",
			class: ClassificationNone,
		},

		// False-positive guards: prose that discusses limits.
		{
			name: "conditional prose", in: "If you hit your usage limit, ccresu waits until it resets.",
			class: ClassificationNone,
		},
		{
			name: "temporal prose", in: "When you've hit your 5-hour limit, ccresu resumes the session.",
			class: ClassificationNone,
		},
		{
			name: "described message", in: "ccresu detects the message You've hit your 5-hour limit · resets 12pm (Europe/Berlin).",
			class: ClassificationNone,
		},
		{
			name: "double quoted example", in: `"You've hit your 5-hour limit"`,
			class: ClassificationNone,
		},
		{
			name: "backticked example", in: "`You've hit your 5-hour limit`",
			class: ClassificationNone,
		},
		{
			name: "fenced example", in: "```\nYou've hit your 5-hour limit\n```",
			class: ClassificationNone,
		},
		{
			name: "question", in: "Did you hit your usage limit?",
			class: ClassificationNone,
		},
		{
			name: "markdown quote", in: "> You've hit your 5-hour limit",
			class: ClassificationNone,
		},
		{
			name: "transient rate limit retry", in: "Rate limit reached, retrying in 5 seconds",
			class: ClassificationNone,
		},
		{
			name: "possibility prose", in: "Note: you can hit your 5-hour limit if you work long enough.",
			class: ClassificationNone,
		},
		{
			name: "phrase discussed in quotes", in: "The phrase 'You've hit your 5-hour limit' tells ccresu to wait.",
			class: ClassificationNone,
		},
		{
			name: "negated", in: "You have not hit your limit",
			class: ClassificationNone,
		},
		{
			name: "third person", in: "users who hit their limit see a banner",
			class: ClassificationNone,
		},
	}

	d := defaultDetector
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			event := d.Analyze(tc.in)
			if event.Detected != tc.detected {
				t.Fatalf("Detected = %v, want %v (class %q, confidence %.2f, notes %v)",
					event.Detected, tc.detected, event.Classification, event.Confidence, event.Notes)
			}
			if event.Classification != tc.class {
				t.Fatalf("Classification = %q, want %q (notes %v)", event.Classification, tc.class, event.Notes)
			}
			if event.Message != tc.in {
				t.Fatalf("Message = %q, want the original input", event.Message)
			}
			if event.DetectedAt.IsZero() {
				t.Fatal("DetectedAt must be set")
			}
			if !tc.detected {
				if event.Confidence != 0 && event.Confidence > DefaultThreshold {
					t.Fatalf("confidence %.2f must not reach the threshold for a negative case", event.Confidence)
				}
				return
			}

			if event.Limit != tc.kind {
				t.Errorf("Limit = %q, want %q", event.Limit, tc.kind)
			}
			if tc.qualifier != "" && event.Qualifier != tc.qualifier {
				t.Errorf("Qualifier = %q, want %q", event.Qualifier, tc.qualifier)
			}
			if tc.patternID != "" && event.PatternID != tc.patternID {
				t.Errorf("PatternID = %q, want %q", event.PatternID, tc.patternID)
			}
			if event.PausedGoal != tc.pausedGoal {
				t.Errorf("PausedGoal = %v, want %v", event.PausedGoal, tc.pausedGoal)
			}
			if tc.minScore > 0 && event.Confidence < tc.minScore {
				t.Errorf("Confidence = %.2f, want at least %.2f", event.Confidence, tc.minScore)
			}
			if tc.maxScore > 0 && event.Confidence > tc.maxScore {
				t.Errorf("Confidence = %.2f, want at most %.2f", event.Confidence, tc.maxScore)
			}
			if tc.resetExpr != "" {
				if !event.Reset.Present {
					t.Fatalf("Reset.Present = false, want true")
				}
				if event.Reset.Expression != tc.resetExpr {
					t.Errorf("Reset.Expression = %q, want %q", event.Reset.Expression, tc.resetExpr)
				}
				if event.Reset.Timezone != tc.timezone {
					t.Errorf("Reset.Timezone = %q, want %q", event.Reset.Timezone, tc.timezone)
				}
				if event.Reset.Relative != tc.relative {
					t.Errorf("Reset.Relative = %v, want %v", event.Reset.Relative, tc.relative)
				}
				if event.Reset.EpochUnix != tc.epoch {
					t.Errorf("Reset.EpochUnix = %d, want %d", event.Reset.EpochUnix, tc.epoch)
				}
			}
		})
	}
}

func TestAnalyzeEmptyInputNote(t *testing.T) {
	t.Parallel()

	event := Analyze("")
	if len(event.Notes) != 1 || event.Notes[0] != "empty input" {
		t.Fatalf("Notes = %v, want [empty input]", event.Notes)
	}
	if event.Normalized != "" {
		t.Fatalf("Normalized = %q, want empty", event.Normalized)
	}
}

func TestAnalyzeExplainsItsDecision(t *testing.T) {
	t.Parallel()

	event := Analyze("If you hit your usage limit, ccresu waits.")
	if event.PatternID == "" {
		t.Fatal("expected the rule that matched to be reported")
	}
	if !containsNote(event.Notes, "conditional") {
		t.Fatalf("Notes = %v, want the conditional guard", event.Notes)
	}
	if !containsNote(event.Notes, "below confidence threshold") {
		t.Fatalf("Notes = %v, want the threshold note", event.Notes)
	}
}

func TestAnalyzePausedGoalWithoutLimit(t *testing.T) {
	t.Parallel()

	// A pause with no limit phrase is reported as a pause, not a limit.
	event := Analyze("Goal paused for lunch")
	if event.Detected {
		t.Fatal("must not be detected as a usage limit")
	}
	if !event.PausedGoal {
		t.Fatal("PausedGoal must be true")
	}
	if !containsNote(event.Notes, "paused goal without a usage-limit phrase") {
		t.Fatalf("Notes = %v", event.Notes)
	}
}

func TestEventHelpers(t *testing.T) {
	t.Parallel()

	detected := Analyze("You've hit your 5-hour limit · resets 12pm (Europe/Berlin)")
	if !detected.IsUsageLimit() {
		t.Fatal("IsUsageLimit must agree with Detected")
	}
	if detected.Reason() == "" {
		t.Fatal("Reason must not be empty")
	}

	plain := Analyze("hello")
	if plain.IsUsageLimit() {
		t.Fatal("ordinary text is not a limit")
	}
	if plain.Reason() != "none: no rule matched" {
		t.Fatalf("Reason = %q", plain.Reason())
	}

	empty := UsageLimitEvent{Classification: ClassificationCompleted}
	if empty.Reason() != "completed" {
		t.Fatalf("Reason = %q, want completed", empty.Reason())
	}
}

func TestAnalyzeIsConcurrencySafe(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"You've hit your 5-hour limit · resets 12pm (Europe/Berlin)",
		"Goal paused · usage limit reached · send a message after it resets",
		"If you hit your usage limit, ccresu waits.",
		"Invalid API key",
		"ordinary output",
	}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for _, in := range inputs {
				_ = defaultDetector.Analyze(in)
			}
		}(i)
	}
	wg.Wait()
}

func TestNewWithCustomPattern(t *testing.T) {
	t.Parallel()

	d, err := New(Options{
		DisableBuiltinPatterns: true,
		Patterns: []Pattern{{
			ID:          "custom-parking",
			Expression:  `\bparked\b`,
			Confidence:  0.99,
			Description: "an invented phrasing",
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if d.Analyze("You've hit your 5-hour limit").Detected {
		t.Fatal("the built-in rules must be disabled")
	}
	event := d.Analyze("the session is parked, resets 5pm")
	if !event.Detected {
		t.Fatalf("custom pattern did not fire: %+v", event)
	}
	if event.PatternID != "custom-parking" {
		t.Fatalf("PatternID = %q, want custom-parking", event.PatternID)
	}
	if ids := d.PatternIDs(); len(ids) != 1 || ids[0] != "custom-parking" {
		t.Fatalf("PatternIDs = %v", ids)
	}
}

func TestPatternRequiresReset(t *testing.T) {
	t.Parallel()

	d, err := New(Options{
		DisableBuiltinPatterns: true,
		Patterns: []Pattern{{
			ID:            "needs-reset",
			Expression:    `\bparked\b`,
			Confidence:    0.99,
			RequiresReset: true,
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if d.Analyze("the session is parked").Detected {
		t.Fatal("a rule that requires a reset must not fire without one")
	}
	if !d.Analyze("the session is parked, resets 5pm").Detected {
		t.Fatal("the rule must fire when a reset phrase is present")
	}
}

func TestThresholdIsHonoured(t *testing.T) {
	t.Parallel()

	d, err := New(Options{Threshold: 0.999})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if d.Threshold() != 0.999 {
		t.Fatalf("Threshold() = %v", d.Threshold())
	}
	// The canonical banner scores 0.99, which is below this gate.
	if d.Analyze("You've hit your 5-hour limit").Detected {
		t.Fatal("a score below the gate must not detect")
	}
}

func TestClockInjection(t *testing.T) {
	t.Parallel()

	want := time.Date(2026, time.June, 15, 9, 0, 0, 0, time.UTC)
	d, err := New(Options{Clock: clock.NewFake(want)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	event := d.Analyze("You've hit your 5-hour limit")
	if !event.DetectedAt.Equal(want) {
		t.Fatalf("DetectedAt = %v, want %v", event.DetectedAt, want)
	}
}

func TestPackageLevelHelpers(t *testing.T) {
	t.Parallel()

	if !IsUsageLimit("You've hit your 5-hour limit") {
		t.Fatal("IsUsageLimit must report the canonical banner")
	}
	if IsUsageLimit("hello") {
		t.Fatal("IsUsageLimit must not report ordinary text")
	}
	if got := Analyze("hello"); got.Classification != ClassificationNone {
		t.Fatalf("Analyze classification = %q", got.Classification)
	}
	if DefaultThreshold <= 0 || DefaultThreshold >= 1 {
		t.Fatalf("DefaultThreshold = %v", DefaultThreshold)
	}
}

func TestNoRuleMatchedNote(t *testing.T) {
	t.Parallel()

	event := Analyze("the build finished with exit code 0")
	if len(event.Notes) != 1 || event.Notes[0] != "no rule matched" {
		t.Fatalf("Notes = %v, want [no rule matched]", event.Notes)
	}
}

func TestNegativeRuleNote(t *testing.T) {
	t.Parallel()

	event := Analyze("Invalid API key")
	if event.Detected {
		t.Fatal("must not be detected")
	}
	found := false
	for _, note := range event.Notes {
		if len(note) > len("matched negative rule: ") && note[:len("matched negative rule: ")] == "matched negative rule: " {
			found = true
		}
	}
	if !found {
		t.Fatalf("Notes = %v, want a negative-rule note", event.Notes)
	}
}
