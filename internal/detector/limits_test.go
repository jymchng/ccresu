package detector

import (
	"errors"
	"testing"
)

func TestLimitKindValid(t *testing.T) {
	t.Parallel()

	for _, kind := range []LimitKind{LimitFiveHour, LimitWeekly, LimitOther} {
		if !kind.Valid() {
			t.Errorf("%q must be valid", kind)
		}
		if kind.String() != string(kind) {
			t.Errorf("String() = %q, want %q", kind.String(), string(kind))
		}
	}
	for _, kind := range []LimitKind{"", "hourly", "FIVE_HOUR"} {
		if kind.Valid() {
			t.Errorf("%q must not be valid", kind)
		}
	}
}

func TestInferKind(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in       string
		wantKind LimitKind
		wantQual string
	}{
		{"you've hit your 5-hour limit", LimitFiveHour, "5-hour"},
		{"you've hit your five hour limit", LimitFiveHour, "5-hour"},
		{"the 5 hour limit was reached", LimitFiveHour, "5-hour"},
		{"weekly limit reached", LimitWeekly, "weekly"},
		{"7-day limit reached", LimitWeekly, "weekly"},
		{"seven-day limit reached", LimitWeekly, "weekly"},
		{"daily limit reached", LimitOther, "daily"},
		{"24-hour limit reached", LimitOther, "daily"},
		{"per day limit", LimitOther, "daily"},
		{"monthly limit reached", LimitOther, "monthly"},
		{"your opus limit", LimitOther, "opus"},
		{"your sonnet limit", LimitOther, "sonnet"},
		{"your haiku limit", LimitOther, "haiku"},
		{"you ran out of extra usage", LimitOther, "extra usage"},
		{"message limit reached", LimitOther, "message"},
		{"usage limit reached", LimitOther, ""},
		{"", LimitOther, ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			kind, qual := inferKind(tc.in)
			if kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", kind, tc.wantKind)
			}
			if qual != tc.wantQual {
				t.Errorf("qualifier = %q, want %q", qual, tc.wantQual)
			}
		})
	}
}

func TestInferKindPrecedence(t *testing.T) {
	t.Parallel()

	// The weekly window is mentioned alongside the five-hour one: the more
	// specific weekly qualifier wins because a weekly limit implies the
	// five-hour window is also exhausted.
	kind, qual := inferKind("you've hit your weekly limit, the 5-hour limit resets later")
	if kind != LimitWeekly || qual != "weekly" {
		t.Fatalf("kind=%q qualifier=%q, want weekly/weekly", kind, qual)
	}
}

func TestPatternCompileErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		pattern Pattern
	}{
		{"empty id", Pattern{Expression: "x", Confidence: 0.5}},
		{"blank id", Pattern{ID: "   ", Expression: "x", Confidence: 0.5}},
		{"empty expression", Pattern{ID: "x", Confidence: 0.5}},
		{"zero confidence", Pattern{ID: "x", Expression: "x", Confidence: 0}},
		{"negative confidence", Pattern{ID: "x", Expression: "x", Confidence: -0.5}},
		{"confidence above one", Pattern{ID: "x", Expression: "x", Confidence: 1.5}},
		{"unknown kind", Pattern{ID: "x", Expression: "x", Confidence: 0.5, Kind: "bogus"}},
		{"invalid regexp", Pattern{ID: "x", Expression: "[unclosed", Confidence: 0.5}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(Options{Patterns: []Pattern{tc.pattern}}); err == nil {
				t.Fatal("expected an error")
			} else if !errors.Is(err, ErrInvalidPattern) {
				t.Fatalf("error = %v, want it to wrap ErrInvalidPattern", err)
			}
		})
	}
}

func TestNewRejectsDuplicatePatternIDs(t *testing.T) {
	t.Parallel()

	dup := Pattern{ID: "canonical-hit-five-hour", Expression: "x", Confidence: 0.5}
	_, err := New(Options{Patterns: []Pattern{dup}})
	if err == nil {
		t.Fatal("expected a duplicate-id error")
	}
	if !errors.Is(err, ErrInvalidPattern) {
		t.Fatalf("error = %v, want ErrInvalidPattern", err)
	}
}

func TestNewRejectsBadThreshold(t *testing.T) {
	t.Parallel()

	for _, threshold := range []float64{-0.1, 1.1, 2} {
		if _, err := New(Options{Threshold: threshold}); err == nil {
			t.Fatalf("threshold %v must be rejected", threshold)
		}
	}
	// Zero means "use the default" and one is a legal, if unreachable, gate.
	if _, err := New(Options{Threshold: 1}); err != nil {
		t.Fatalf("threshold 1 must be accepted: %v", err)
	}
}
