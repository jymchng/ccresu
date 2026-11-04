// Package detector_test verifies the detector from a caller's point of view.
//
// It deliberately uses only the exported API, so it cannot lean on the internal
// helpers the unit tests use. Each test maps to one clause of the acceptance
// criteria.
package detector_test

import (
	"strings"
	"testing"

	"github.com/jymchng/ccresu/internal/config"
	"github.com/jymchng/ccresu/internal/detector"
	"github.com/jymchng/ccresu/internal/storage"
)

// TestAcceptanceCanonicalBanner is the exact message from the README and the
// end-to-end scenario. It must be detected with a five-hour kind and a reset
// phrase a scheduler can resolve.
func TestAcceptanceCanonicalBanner(t *testing.T) {
	const message = "You've hit your 5-hour limit · resets 12pm (Europe/Berlin)"

	event := detector.Analyze(message)
	if !event.Detected {
		t.Fatalf("not detected: classification=%q notes=%v", event.Classification, event.Notes)
	}
	if event.Classification != detector.ClassificationUsageLimit {
		t.Fatalf("classification = %q", event.Classification)
	}
	if event.Limit != detector.LimitFiveHour {
		t.Fatalf("limit = %q, want five_hour", event.Limit)
	}
	if !event.Reset.Present {
		t.Fatal("reset phrase missing")
	}
	if event.Reset.Expression != "12pm" {
		t.Fatalf("expression = %q, want 12pm", event.Reset.Expression)
	}
	if event.Reset.Timezone != "Europe/Berlin" {
		t.Fatalf("timezone = %q, want Europe/Berlin", event.Reset.Timezone)
	}
	if event.Confidence < 0.9 || event.Confidence > 1 {
		t.Fatalf("confidence = %v, want a high score within [0,1]", event.Confidence)
	}
	if event.Message != message {
		t.Fatal("the original message must be preserved verbatim")
	}
}

// TestAcceptancePausedGoalBanner is the second message from the README. It
// carries no reset time, which must be reported honestly rather than invented.
func TestAcceptancePausedGoalBanner(t *testing.T) {
	event := detector.Analyze("Goal paused · usage limit reached · send a message after it resets")

	if !event.Detected {
		t.Fatalf("not detected: %v", event.Notes)
	}
	if !event.PausedGoal {
		t.Fatal("the paused goal was not reported")
	}
	if event.PausedMessage == "" {
		t.Fatal("the paused wording must be available for notifications")
	}
	if event.Reset.Present {
		t.Fatalf("a reset time was invented: %+v", event.Reset)
	}
}

// TestAcceptanceResetBearingAndResetless covers both variants of every limit
// family the product must recognise.
func TestAcceptanceResetBearingAndResetless(t *testing.T) {
	cases := []struct {
		message  string
		kind     detector.LimitKind
		hasReset bool
	}{
		{"You've hit your 5-hour limit · resets 12pm (Europe/Berlin)", detector.LimitFiveHour, true},
		{"You've hit your 5-hour limit", detector.LimitFiveHour, false},
		{"You've hit your weekly limit · resets Monday 9am (UTC)", detector.LimitWeekly, true},
		{"You've hit your weekly limit", detector.LimitWeekly, false},
		{"You've hit your daily limit · resets at 3am", detector.LimitOther, true},
		{"You've hit your daily limit", detector.LimitOther, false},
		{"You've run out of extra usage · resets in 4 hours", detector.LimitOther, true},
		{"You've run out of extra usage", detector.LimitOther, false},
	}

	for _, tc := range cases {
		event := detector.Analyze(tc.message)
		if !event.Detected {
			t.Errorf("%q not detected (notes %v)", tc.message, event.Notes)
			continue
		}
		if event.Limit != tc.kind {
			t.Errorf("%q limit = %q, want %q", tc.message, event.Limit, tc.kind)
		}
		if event.Reset.Present != tc.hasReset {
			t.Errorf("%q reset.Present = %v, want %v", tc.message, event.Reset.Present, tc.hasReset)
		}
	}
}

// TestAcceptanceNonClassification proves each non-limit outcome is named, not
// silently confused with a limit.
func TestAcceptanceNonClassification(t *testing.T) {
	cases := []struct {
		message string
		want    detector.Classification
	}{
		{"Invalid API key · Please run /login", detector.ClassificationInvalidKey},
		{"missing credentials for this request", detector.ClassificationInvalidKey},
		{"Authentication failed. Please log in.", detector.ClassificationAuthError},
		{"Connection refused", detector.ClassificationNetworkError},
		{"fetch failed: ENOTFOUND", detector.ClassificationNetworkError},
		{"Operation cancelled by user", detector.ClassificationUserCancelled},
		{"Interrupted by user", detector.ClassificationUserCancelled},
		{"Goal completed successfully", detector.ClassificationCompleted},
		{"All tasks finished", detector.ClassificationCompleted},
	}

	for _, tc := range cases {
		event := detector.Analyze(tc.message)
		if event.Detected {
			t.Errorf("%q was wrongly detected as a usage limit", tc.message)
			continue
		}
		if event.Classification != tc.want {
			t.Errorf("%q classification = %q, want %q", tc.message, event.Classification, tc.want)
		}
	}
}

// TestAcceptanceGuardsAgainstProse is the false-positive suite: text that talks
// about limits must not be mistaken for an interruption.
func TestAcceptanceGuardsAgainstProse(t *testing.T) {
	messages := []string{
		"If you hit your usage limit, ccresu waits until it resets.",
		"When you've hit your 5-hour limit, ccresu resumes the session.",
		"ccresu detects the message You've hit your 5-hour limit · resets 12pm (Europe/Berlin).",
		`The status shows "You've hit your 5-hour limit" when the window is exhausted.`,
		"```\nYou've hit your 5-hour limit\n```",
		"> You've hit your 5-hour limit",
		"Did you hit your usage limit?",
		"Rate limit reached, retrying in 5 seconds",
		"You have not hit your limit",
		"users who hit their limit see a banner",
		"I'll explain what happens when you've hit your 5-hour limit.",
		"The phrase 'You've hit your 5-hour limit' tells ccresu to wait.",
	}
	for _, message := range messages {
		if event := detector.Analyze(message); event.Detected {
			t.Errorf("prose was detected as a limit: %q (confidence %.2f, notes %v)",
				message, event.Confidence, event.Notes)
		}
	}
}

// TestAcceptanceRealBannerAfterProse is the other half of the guard contract: a
// real banner must survive nearby prose.
func TestAcceptanceRealBannerAfterProse(t *testing.T) {
	messages := []string{
		"You can hit the 5-hour limit.\nYou've hit your 5-hour limit · resets 12pm (Europe/Berlin)",
		"Error: You've hit your 5-hour limit · resets 12pm (Europe/Berlin)",
		"it works well. You've hit your 5-hour limit · resets 12pm (Europe/Berlin)",
		"You've hit your 5-hour limit · resets 12pm (Europe/Berlin)\nWould you like to continue?",
	}
	for _, message := range messages {
		event := detector.Analyze(message)
		if !event.Detected {
			t.Errorf("real banner missed in %q (notes %v)", message, event.Notes)
			continue
		}
		if event.Reset.Expression != "12pm" || event.Reset.Timezone != "Europe/Berlin" {
			t.Errorf("%q reset = %+v", message, event.Reset)
		}
	}
}

// TestAcceptanceQuotedExampleDoesNotMaskTheRealBanner is a regression guard: a
// transcript can quote the banner and then contain the real thing, and the
// quotation must not win just because it comes first.
func TestAcceptanceQuotedExampleDoesNotMaskTheRealBanner(t *testing.T) {
	messages := []string{
		"```\nYou've hit your 5-hour limit\n```\nYou've hit your 5-hour limit · resets 12pm (Europe/Berlin)",
		`The docs say "You've hit your 5-hour limit". Then it happened: You've hit your 5-hour limit · resets 12pm (Europe/Berlin)`,
		`ccresu detects the phrase "You've hit your 5-hour limit". Later: You've hit your 5-hour limit · resets 12pm (Europe/Berlin)`,
	}
	for _, message := range messages {
		event := detector.Analyze(message)
		if !event.Detected {
			t.Errorf("real banner masked by the quoted example in %q (notes %v)", message, event.Notes)
			continue
		}
		if event.Confidence < 0.95 {
			t.Errorf("%q confidence = %.2f, want the clean occurrence to win", message, event.Confidence)
		}
	}
}

// TestAcceptanceNormalisation drives the same banner through hostile
// renderings: case, wrapping, unicode punctuation, fullwidth glyphs and ANSI.
func TestAcceptanceNormalisation(t *testing.T) {
	renderings := []string{
		"YOU'VE HIT YOUR 5-HOUR LIMIT · RESETS 12PM (EUROPE/BERLIN)",
		"you've hit your 5-hour limit",
		"You\u2019ve hit your 5\u2013hour\nlimit \u00b7 resets 12pm (Europe/Berlin)",
		"\x1b[31mYou've hit your 5-hour limit\x1b[0m · resets 12pm (Europe/Berlin)",
		"You\u00a0have\u00a0hit\u00a0your\u00a05-hour\u00a0limit",
		"Ｙｏｕ'ｖｅ ｈｉｔ ｙｏｕｒ ５-ｈｏｕｒ ｌｉｍｉｔ",
		"  you've   hit\tyour\n5-hour limit  ",
	}
	for _, rendering := range renderings {
		if event := detector.Analyze(rendering); !event.Detected {
			t.Errorf("normalisation failed for %q (normalized %q, notes %v)",
				rendering, event.Normalized, event.Notes)
		}
		if event := detector.Analyze(rendering); event.Limit != detector.LimitFiveHour {
			t.Errorf("%q limit = %q, want five_hour", rendering, event.Limit)
		}
	}
}

// TestAcceptanceNormalizeContract checks the exported normaliser directly.
func TestAcceptanceNormalizeContract(t *testing.T) {
	once := detector.Normalize("You\u2019ve hit your 5\u2013hour\nlimit \u00b7 resets 12pm (Europe/Berlin)")
	if once != "you've hit your 5-hour limit : resets 12pm (europe/berlin)" {
		t.Fatalf("Normalize = %q", once)
	}
	if twice := detector.Normalize(once); twice != once {
		t.Fatalf("Normalize is not idempotent: %q vs %q", once, twice)
	}
	if got := detector.Normalize(""); got != "" {
		t.Fatalf("Normalize(\"\") = %q", got)
	}
}

// TestAcceptanceConfidenceBounds checks the confidence contract on every
// classification, including hostile input.
func TestAcceptanceConfidenceBounds(t *testing.T) {
	inputs := []string{
		"", " ", "\x00\x01\x02", strings.Repeat("limit ", 2000),
		"\"'`[[((limit", "You've hit your 5-hour limit",
		"Invalid API key", "Goal completed",
	}
	for _, in := range inputs {
		event := detector.Analyze(in)
		if event.Confidence < 0 || event.Confidence > 1 {
			t.Fatalf("confidence %v out of range for %q", event.Confidence, in)
		}
		if event.Classification == "" {
			t.Fatalf("classification must never be empty (input %q)", in)
		}
		if !event.Classification.Valid() {
			t.Fatalf("classification %q is not valid", event.Classification)
		}
	}
}

// TestAcceptanceCustomPatterns proves the configurable-pattern contract, both
// in code and through the configuration schema the app will wire together.
func TestAcceptanceCustomPatterns(t *testing.T) {
	d, err := detector.New(detector.Options{
		DisableBuiltinPatterns: true,
		Patterns: []detector.Pattern{{
			ID:            "proxy-quota",
			Kind:          detector.LimitFiveHour,
			Expression:    `proxy quota exhausted`,
			Confidence:    0.95,
			RequiresReset: true,
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if d.Analyze("proxy quota exhausted").Detected {
		t.Fatal("RequiresReset must gate the rule")
	}
	event := d.Analyze("proxy quota exhausted, resets 6pm (UTC)")
	if !event.Detected {
		t.Fatalf("custom rule did not fire: %v", event.Notes)
	}
	if event.PatternID != "proxy-quota" {
		t.Fatalf("pattern = %q", event.PatternID)
	}
	if event.Limit != detector.LimitFiveHour {
		t.Fatalf("kind override ignored: %q", event.Limit)
	}
}

// TestAcceptanceConfigSchemaMatchesDetector is the cross-package contract: a
// detector.custom_patterns entry from the config file must be expressible as a
// detector.Pattern without translation, and the kinds must line up with the
// storage vocabulary.
func TestAcceptanceConfigSchemaMatchesDetector(t *testing.T) {
	configured := config.DetectorPattern{
		ID:            "proxy-quota",
		Kind:          "weekly",
		Pattern:       `proxy quota exhausted`,
		Confidence:    0.95,
		RequiresReset: false,
		Description:   "a corporate proxy notice",
	}
	// This is exactly the mapping the app layer will perform.
	pattern := detector.Pattern{
		ID:            configured.ID,
		Kind:          detector.LimitKind(configured.Kind),
		Expression:    configured.Pattern,
		Confidence:    configured.Confidence,
		RequiresReset: configured.RequiresReset,
		Description:   configured.Description,
	}
	d, err := detector.New(detector.Options{Patterns: []detector.Pattern{pattern}})
	if err != nil {
		t.Fatalf("a config-defined pattern must be accepted as-is: %v", err)
	}
	event := d.Analyze("proxy quota exhausted")
	if !event.Detected || event.Limit != detector.LimitWeekly {
		t.Fatalf("config pattern did not work: detected=%v kind=%q notes=%v",
			event.Detected, event.Limit, event.Notes)
	}

	// The detector's kinds must be the storage vocabulary verbatim.
	pairs := []struct {
		detector detector.LimitKind
		stored   storage.LimitType
	}{
		{detector.LimitFiveHour, storage.LimitTypeFiveHour},
		{detector.LimitWeekly, storage.LimitTypeWeekly},
		{detector.LimitOther, storage.LimitTypeOther},
	}
	for _, pair := range pairs {
		if string(pair.detector) != string(pair.stored) {
			t.Errorf("detector kind %q != storage type %q", pair.detector, pair.stored)
		}
	}
}

// TestAcceptanceThresholdIsConfigurable checks the confidence gate a user can
// tune from the config file.
func TestAcceptanceThresholdIsConfigurable(t *testing.T) {
	strict, err := detector.New(detector.Options{Threshold: 0.995})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if strict.Analyze("You've hit your 5-hour limit").Detected {
		t.Fatal("a score below the configured gate must not detect")
	}

	lax, err := detector.New(detector.Options{Threshold: 0.3})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// "Quota exceeded" alone scores 0.8; it must pass a lax gate.
	if !lax.Analyze("Your quota exceeded for this month").Detected {
		t.Fatal("a lax gate must accept a weaker signal")
	}
}

// TestAcceptanceEventShape pins the structured event a persistence layer and a
// notification layer both consume.
func TestAcceptanceEventShape(t *testing.T) {
	event := detector.Analyze("You've hit your 5-hour limit · resets 12pm (Europe/Berlin)")

	if event.DetectedAt.IsZero() {
		t.Fatal("DetectedAt must be populated")
	}
	if event.Normalized == "" || event.Matched == "" || event.PatternID == "" {
		t.Fatalf("event is not self-describing: %+v", event)
	}
	if !event.IsUsageLimit() {
		t.Fatal("IsUsageLimit must agree with Detected")
	}
	if event.Reason() == "" {
		t.Fatal("Reason must explain the verdict")
	}
	if !strings.Contains(event.Normalized, "5-hour limit") {
		t.Fatalf("normalized text = %q", event.Normalized)
	}

	// A negative event is equally structured.
	negative := detector.Analyze("Invalid API key")
	if negative.Detected || negative.Classification != detector.ClassificationInvalidKey {
		t.Fatalf("negative event = %+v", negative)
	}
	if negative.IsUsageLimit() {
		t.Fatal("IsUsageLimit must be false for an invalid key")
	}
}

// TestAcceptanceNoPanicOnHostileInput is a small robustness sweep: session
// output is attacker-adjacent input from another process.
func TestAcceptanceNoPanicOnHostileInput(t *testing.T) {
	inputs := []string{
		string([]byte{0xff, 0xfe, 0x00, 0x41}),
		strings.Repeat("\x1b[", 1000),
		strings.Repeat("```", 500) + "You've hit your 5-hour limit",
		strings.Repeat("((", 500) + "limit",
		"\u0301\u0301\u0301limit",
		strings.Repeat("é", 10000),
	}
	for _, in := range inputs {
		_ = detector.Analyze(in)   // must not panic
		_ = detector.Normalize(in) // must not panic
	}
}

// TestAcceptanceLabelledMentionIsNotAnEvent covers the mirror image of the
// quoted-example case: prose that labels the banner ("a banner message
// reading …") must not be mistaken for the banner itself, while a real banner
// that merely follows a label ending in a colon must still be detected.
func TestAcceptanceLabelledMentionIsNotAnEvent(t *testing.T) {
	if ev := detector.Analyze("a banner message reading you've hit your limit"); ev.Detected {
		t.Fatalf("a labelled mention was reported as an event: kind=%q confidence=%v notes=%v",
			ev.Limit, ev.Confidence, ev.Notes)
	}

	for _, in := range []string{
		"error message: you've hit your 5-hour limit \u00b7 resets 12pm (Europe/Berlin)",
		"the log said: you've hit your 5-hour limit \u00b7 resets 12pm (Europe/Berlin)",
	} {
		ev := detector.Analyze(in)
		if !ev.Detected || ev.Limit != detector.LimitFiveHour {
			t.Fatalf("a real banner after a label was lost: %q -> detected=%v kind=%q notes=%v",
				in, ev.Detected, ev.Limit, ev.Notes)
		}
	}
}
