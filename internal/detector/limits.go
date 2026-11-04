package detector

import (
	"fmt"
	"regexp"
	"strings"
)

// LimitKind classifies which usage limit a message describes.
//
// The values are identical to storage.LimitType so a detection can be persisted
// without a translation table. Everything that is not the five-hour or the
// weekly window is reported as LimitOther and keeps its original wording in
// [UsageLimitEvent.Qualifier], which is enough to tell "daily", "opus" or
// "extra usage" apart without inventing a kind for each one.
type LimitKind string

const (
	// LimitFiveHour is the rolling five-hour usage window.
	LimitFiveHour LimitKind = "five_hour"
	// LimitWeekly is the weekly usage window.
	LimitWeekly LimitKind = "weekly"
	// LimitOther is any other limit the CLI reports.
	LimitOther LimitKind = "other"
)

// Valid reports whether k is a recognised limit kind.
func (k LimitKind) Valid() bool {
	switch k {
	case LimitFiveHour, LimitWeekly, LimitOther:
		return true
	default:
		return false
	}
}

func (k LimitKind) String() string { return string(k) }

// Qualifiers recognised inside a message, ordered by specificity. The first one
// present decides both the qualifier and, for the first two, the kind.
var (
	qualifierExtraUsage = regexp.MustCompile(`\bextra usage\b`)
	qualifierWeekly     = regexp.MustCompile(`\b(?:weekly|7[- ]day|seven[- ]day)\b`)
	qualifierFiveHour   = regexp.MustCompile(`\b(?:5|five)[- ]hour\b`)
	qualifierDaily      = regexp.MustCompile(`\b(?:daily|24[- ]hour|per day)\b`)
	qualifierMonthly    = regexp.MustCompile(`\bmonthly\b`)
	qualifierOpus       = regexp.MustCompile(`\bopus\b`)
	qualifierSonnet     = regexp.MustCompile(`\bsonnet\b`)
	qualifierHaiku      = regexp.MustCompile(`\bhaiku\b`)
	qualifierMessage    = regexp.MustCompile(`\bmessage(?:s)? limit\b`)
)

// inferKind derives the limit kind and the descriptive qualifier from the
// normalised message.
func inferKind(folded string) (LimitKind, string) {
	switch {
	case qualifierExtraUsage.MatchString(folded):
		return LimitOther, "extra usage"
	case qualifierWeekly.MatchString(folded):
		return LimitWeekly, "weekly"
	case qualifierFiveHour.MatchString(folded):
		return LimitFiveHour, "5-hour"
	case qualifierDaily.MatchString(folded):
		return LimitOther, "daily"
	case qualifierMonthly.MatchString(folded):
		return LimitOther, "monthly"
	case qualifierOpus.MatchString(folded):
		return LimitOther, "opus"
	case qualifierSonnet.MatchString(folded):
		return LimitOther, "sonnet"
	case qualifierHaiku.MatchString(folded):
		return LimitOther, "haiku"
	case qualifierMessage.MatchString(folded):
		return LimitOther, "message"
	default:
		return LimitOther, ""
	}
}

// Pattern is one detection rule.
//
// Expression is a Go regular expression evaluated against the normalised text
// produced by [Normalize]; it is therefore lowercase and never anchored, so one
// rule finds its phrase anywhere in a wrapped message. Confidence is the score
// awarded when the rule matches and no guard applies; the guards in guard.go
// subtract from it.
type Pattern struct {
	// ID names the rule in events and logs. It must be unique and non-empty.
	ID string
	// Kind overrides the kind inferred from the text. Empty keeps inference.
	Kind LimitKind
	// Expression is the rule, a regular expression over normalised text.
	Expression string
	// Confidence is the score for a bare match, in (0, 1].
	Confidence float64
	// RequiresReset discards the match unless the message also carried reset
	// information. It is for phrasings that are only decisive together with a
	// reset time.
	RequiresReset bool
	// Description documents the phrasing the rule covers.
	Description string

	re *regexp.Regexp
}

// String renders the pattern id.
func (p Pattern) String() string { return p.ID }

// compile validates a pattern and prepares its regular expression.
func (p Pattern) compile() (Pattern, error) {
	if strings.TrimSpace(p.ID) == "" {
		return Pattern{}, fmt.Errorf("%w: pattern id must not be empty", ErrInvalidPattern)
	}
	if strings.TrimSpace(p.Expression) == "" {
		return Pattern{}, fmt.Errorf("%w: pattern %q has an empty expression", ErrInvalidPattern, p.ID)
	}
	if p.Confidence <= 0 || p.Confidence > 1 {
		return Pattern{}, fmt.Errorf("%w: pattern %q confidence %v must be in (0, 1]", ErrInvalidPattern, p.ID, p.Confidence)
	}
	if p.Kind != "" && !p.Kind.Valid() {
		return Pattern{}, fmt.Errorf("%w: pattern %q has unknown kind %q", ErrInvalidPattern, p.ID, p.Kind)
	}
	re, err := regexp.Compile(p.Expression)
	if err != nil {
		return Pattern{}, fmt.Errorf("%w: pattern %q: %v", ErrInvalidPattern, p.ID, err)
	}
	p.re = re
	return p, nil
}

// builtinPatterns returns the rules shipped with ccresu.
//
// The list is ordered most-specific first. Confidence resolves the overlap, and
// equal confidences keep the earlier rule, so the ordering is the tie-break.
func builtinPatterns() []Pattern {
	return []Pattern{
		{
			ID:          "canonical-hit-five-hour",
			Kind:        LimitFiveHour,
			Expression:  `\byou(?:'ve| have)? (?:just )?hit your (?:5|five)[- ]hour limit\b`,
			Confidence:  0.99,
			Description: "the canonical \"You've hit your 5-hour limit\" banner",
		},
		{
			ID:          "canonical-hit-weekly",
			Kind:        LimitWeekly,
			Expression:  `\byou(?:'ve| have)? (?:just )?hit your (?:weekly|7[- ]day|seven[- ]day) limit\b`,
			Confidence:  0.99,
			Description: "the canonical \"You've hit your weekly limit\" banner",
		},
		{
			ID:          "canonical-reached-five-hour",
			Kind:        LimitFiveHour,
			Expression:  `\byou(?:'ve| have)? (?:just )?reached your (?:5|five)[- ]hour limit\b`,
			Confidence:  0.99,
			Description: "\"You have reached your 5-hour limit\"",
		},
		{
			ID:          "canonical-reached-weekly",
			Kind:        LimitWeekly,
			Expression:  `\byou(?:'ve| have)? (?:just )?reached your (?:weekly|7[- ]day|seven[- ]day) limit\b`,
			Confidence:  0.99,
			Description: "\"You have reached your weekly limit\"",
		},
		{
			ID:          "canonical-hit-limit",
			Expression:  `\byou(?:'ve| have)? (?:just )?hit your (?:[\w\-]+ )?limit\b`,
			Confidence:  0.95,
			Description: "\"You've hit your <qualifier> limit\"",
		},
		{
			ID:          "canonical-reached-limit",
			Expression:  `\byou(?:'ve| have)? (?:just )?reached your (?:[\w\-]+ )?limit\b`,
			Confidence:  0.95,
			Description: "\"You have reached your <qualifier> limit\"",
		},
		{
			ID:          "canonical-exceeded-limit",
			Expression:  `\byou(?:'ve| have)? (?:just )?exceeded your (?:[\w\-]+ )?limit\b`,
			Confidence:  0.95,
			Description: "\"You've exceeded your <qualifier> limit\"",
		},
		{
			ID:          "five-hour-limit-hit",
			Kind:        LimitFiveHour,
			Expression:  `\b(?:5|five)[- ]hour limit (?:has been |was )?(?:reached|hit|exceeded)\b`,
			Confidence:  0.97,
			Description: "personless \"5-hour limit reached\"",
		},
		{
			ID:          "weekly-limit-hit",
			Kind:        LimitWeekly,
			Expression:  `\b(?:weekly|7[- ]day|seven[- ]day) limit (?:has been |was )?(?:reached|hit|exceeded)\b`,
			Confidence:  0.97,
			Description: "personless \"weekly limit reached\"",
		},
		{
			ID:          "epoch-usage-limit",
			Expression:  `\bclaude ai usage limit reached\|\d{9,19}\b`,
			Confidence:  0.97,
			Description: "the legacy \"Claude AI usage limit reached|<epoch>\" form",
		},
		{
			ID:          "paused-goal-usage-limit",
			Expression:  `\b(?:goal|session|task|run) paused\b[^.]{0,80}\busage limit\b`,
			Confidence:  0.95,
			Description: "the paused-goal banner that names the usage limit",
		},
		{
			ID:          "usage-limit-reached",
			Expression:  `\b(?:claude(?: code)?(?: ai)? )?usage limit (?:has been |was )?(?:reached|hit|exceeded)\b`,
			Confidence:  0.95,
			Description: "generic \"usage limit reached\"",
		},
		{
			ID:          "out-of-extra-usage",
			Kind:        LimitOther,
			Expression:  `\byou(?:'ve| have)? (?:run out of|used up|are out of|exhausted) (?:your )?extra usage\b`,
			Confidence:  0.93,
			Description: "\"You've run out of extra usage\"",
		},
		{
			ID:          "limit-will-reset",
			Expression:  `\b(?:[\w\-]+ )?limit will reset\b`,
			Confidence:  0.92,
			Description: "\"Your limit will reset at …\"",
		},
		{
			ID:            "limit-resets-in",
			Expression:    `\b(?:[\w\-]+ )?limit (?:will )?resets? in\b`,
			Confidence:    0.9,
			Description:   "\"limit resets in <duration>\"",
			RequiresReset: true,
		},
		{
			ID:          "plan-limit",
			Expression:  `\byou(?:'ve| have)? (?:reached|hit) (?:the )?(?:[\w\-]+ )?(?:maximum|limit) for (?:claude|your plan|this plan)\b`,
			Confidence:  0.9,
			Description: "\"You've reached the maximum for your plan\"",
		},
		{
			ID:          "generic-limit-reached",
			Expression:  `\b(?:[\w\-]+ )?limit (?:has been |was )?(?:reached|hit|exceeded)\b`,
			Confidence:  0.9,
			Description: "any \"<qualifier> limit reached\" without a subject",
		},
		{
			ID:          "quota-exhausted",
			Kind:        LimitOther,
			Expression:  `\b(?:usage |monthly )?quota (?:exceeded|exhausted|reached)\b`,
			Confidence:  0.8,
			Description: "\"quota exceeded\"",
		},
		{
			ID:            "limit-try-again",
			Expression:    `\b(?:usage|rate|message) limit\b[^.]{0,60}\b(?:try again|available again|resets?)\b`,
			Confidence:    0.88,
			Description:   "a limit notice that names when to come back",
			RequiresReset: false,
		},
	}
}
