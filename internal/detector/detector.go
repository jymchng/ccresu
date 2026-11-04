package detector

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jymchng/ccresu/internal/clock"
)

// ErrInvalidPattern reports a detection rule that cannot be compiled.
var ErrInvalidPattern = errors.New("detector: invalid pattern")

// DefaultThreshold is the minimum confidence a detection needs to count.
//
// It sits at 0.5 so that the canonical banners (0.95–0.99) always detect, while
// a generic match (0.9) collapses below the line as soon as one strong guard —
// a conditional, a quotation, a code fence or a question — applies.
const DefaultThreshold = 0.5

// UsageLimitEvent is the structured result of inspecting one message.
//
// It is a single value for every outcome, not just limits: Classification says
// what the message was, and Detected is true only for ClassificationUsageLimit.
// That keeps the negative cases first-class — an auth failure or a completed
// session is reported, with its confidence and matched rule, instead of being
// indistinguishable from silence.
type UsageLimitEvent struct {
	// Detected reports whether a usage limit was confirmed.
	Detected bool
	// Classification is the verdict, including the explicit non-limit cases.
	Classification Classification
	// Limit is the kind of limit, valid when Detected is true.
	Limit LimitKind
	// Qualifier preserves the wording of a limit that is not the five-hour or
	// weekly one, e.g. "daily", "opus" or "extra usage".
	Qualifier string
	// Confidence is the final score in [0,1] after the guards were applied.
	Confidence float64
	// Reset carries the reset time the message mentioned, if any.
	Reset ResetPhrase
	// PausedGoal reports whether the message also said the goal is paused.
	PausedGoal bool
	// PausedMessage is the paused-goal wording as written, when present.
	PausedMessage string
	// Message is the original, unmodified input.
	Message string
	// Normalized is the canonical lowercase form the patterns matched.
	Normalized string
	// Matched is the normalised substring that triggered the rule.
	Matched string
	// PatternID names the rule that matched, empty when nothing did.
	PatternID string
	// Notes explains the decision: which guards fired, or why nothing matched.
	Notes []string
	// DetectedAt is when the inspection ran.
	DetectedAt time.Time
}

// IsUsageLimit reports whether the event is a confirmed usage limit.
func (e UsageLimitEvent) IsUsageLimit() bool { return e.Detected }

// Reason renders a short human-readable explanation of the classification.
func (e UsageLimitEvent) Reason() string {
	if e.Detected {
		return fmt.Sprintf("usage limit %s detected with confidence %.2f", e.Limit, e.Confidence)
	}
	if len(e.Notes) == 0 {
		return string(e.Classification)
	}
	return string(e.Classification) + ": " + strings.Join(e.Notes, ", ")
}

// Options configures a [Detector].
type Options struct {
	// Threshold is the minimum confidence for a detection. Zero means
	// DefaultThreshold; it must stay in (0, 1].
	Threshold float64
	// Patterns are extra rules appended to the built-in ones.
	Patterns []Pattern
	// DisableBuiltinPatterns keeps only Patterns. It exists for tests and for a
	// deployment that wants a completely custom vocabulary.
	DisableBuiltinPatterns bool
	// Clock supplies DetectedAt. Zero means the system clock.
	Clock clock.Clock
}

// Detector recognises usage-limit interruptions.
//
// A Detector is immutable after construction and therefore safe for concurrent
// use; Analyse never writes to shared state.
type Detector struct {
	patterns  []Pattern
	threshold float64
	clock     clock.Clock
}

// New builds a Detector from opts.
func New(opts Options) (*Detector, error) {
	threshold := opts.Threshold
	if threshold == 0 {
		threshold = DefaultThreshold
	}
	if threshold < 0 || threshold > 1 {
		return nil, fmt.Errorf("%w: threshold %v must be in (0, 1]", ErrInvalidPattern, opts.Threshold)
	}

	defs := make([]Pattern, 0, len(opts.Patterns)+16)
	if !opts.DisableBuiltinPatterns {
		defs = append(defs, builtinPatterns()...)
	}
	defs = append(defs, opts.Patterns...)

	compiled := make([]Pattern, 0, len(defs))
	seen := make(map[string]struct{}, len(defs))
	for _, def := range defs {
		p, err := def.compile()
		if err != nil {
			return nil, err
		}
		if _, dup := seen[p.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate pattern id %q", ErrInvalidPattern, p.ID)
		}
		seen[p.ID] = struct{}{}
		compiled = append(compiled, p)
	}

	clk := opts.Clock
	if clk == nil {
		clk = clock.System()
	}
	return &Detector{patterns: compiled, threshold: threshold, clock: clk}, nil
}

// mustDetector builds the package default and panics if the shipped rules are
// broken, which can only be a programming error.
func mustDetector() *Detector {
	d, err := New(Options{})
	if err != nil {
		panic("detector: built-in patterns are invalid: " + err.Error())
	}
	return d
}

var defaultDetector = mustDetector()

// Analyze inspects a message with the built-in rules.
func Analyze(raw string) UsageLimitEvent { return defaultDetector.Analyze(raw) }

// IsUsageLimit reports whether a message is a usage-limit interruption.
func IsUsageLimit(raw string) bool { return defaultDetector.Analyze(raw).Detected }

// Threshold returns the confidence a detection needs.
func (d *Detector) Threshold() float64 { return d.threshold }

// PatternIDs lists the rule identifiers in evaluation order.
func (d *Detector) PatternIDs() []string {
	ids := make([]string, 0, len(d.patterns))
	for _, p := range d.patterns {
		ids = append(ids, p.ID)
	}
	return ids
}

// limitMatch is the best rule hit for one message.
type limitMatch struct {
	pattern  Pattern
	start    int
	end      int
	matched  string
	score    float64
	penalty  float64
	guardLog []string
}

// Analyze inspects one message and returns the structured event.
func (d *Detector) Analyze(raw string) UsageLimitEvent {
	text := normalize(raw)
	event := UsageLimitEvent{
		Classification: ClassificationNone,
		Message:        raw,
		Normalized:     text.folded,
		DetectedAt:     d.clock.Now(),
	}

	if strings.TrimSpace(raw) == "" {
		event.Notes = []string{"empty input"}
		return event
	}

	// The reset phrase is needed before matching because a rule may depend on
	// it, and the paused-goal banner is independent of the limit rules.
	event.Reset = extractReset(text.spaced)
	event.PausedGoal, event.PausedMessage = extractPausedGoal(text.spaced)

	if best := d.bestMatch(text.folded, event.Reset.Present); best != nil {
		event.Matched = best.matched
		event.PatternID = best.pattern.ID
		event.Confidence = best.score
		event.Notes = best.guardLog
		event.Limit, event.Qualifier = inferKind(text.folded)
		if best.pattern.Kind != "" {
			event.Limit = best.pattern.Kind
		}
		if best.score >= d.threshold {
			event.Detected = true
			event.Classification = ClassificationUsageLimit
			return event
		}
		event.Notes = append(event.Notes, "below confidence threshold")
	}

	if rule := bestNegative(text.folded); rule.re != nil {
		event.Classification = rule.Classification
		event.Notes = append(event.Notes, "matched negative rule: "+rule.Description)
		return event
	}

	if event.PausedGoal {
		event.Notes = append(event.Notes, "paused goal without a usage-limit phrase")
		return event
	}

	if event.Notes == nil {
		event.Notes = []string{"no rule matched"}
	}
	return event
}

// bestMatch evaluates every rule and returns the highest scoring hit.
//
// Every occurrence of a rule is scored, not just the leftmost one: a transcript
// blob can quote the banner in an example and then contain the real thing a few
// lines later, and stopping at the quotation would lose the event. The number of
// occurrences examined per rule is capped so a pathological input cannot turn
// detection into a quadratic scan.
func (d *Detector) bestMatch(folded string, hasReset bool) *limitMatch {
	const maxOccurrences = 32

	var best *limitMatch
	for _, p := range d.patterns {
		if p.RequiresReset && !hasReset {
			continue
		}
		for _, loc := range p.re.FindAllStringIndex(folded, maxOccurrences) {
			penalty, notes := applyGuards(folded, loc[0], loc[1])
			score := clampConfidence(p.Confidence - penalty)
			if best != nil && score <= best.score {
				continue
			}
			best = &limitMatch{
				pattern:  p,
				start:    loc[0],
				end:      loc[1],
				matched:  folded[loc[0]:loc[1]],
				score:    score,
				penalty:  penalty,
				guardLog: notes,
			}
		}
	}
	return best
}

// clampConfidence keeps a score inside [0,1].
func clampConfidence(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}
