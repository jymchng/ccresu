package detector

import "testing"

func TestPatternString(t *testing.T) {
	t.Parallel()

	p := Pattern{ID: "my-rule", Expression: "x", Confidence: 0.5}
	if p.String() != "my-rule" {
		t.Fatalf("String() = %q, want my-rule", p.String())
	}
}

func TestClampConfidence(t *testing.T) {
	t.Parallel()

	cases := []struct{ in, want float64 }{
		{-0.5, 0},
		{0, 0},
		{0.42, 0.42},
		{1, 1},
		{1.5, 1},
	}
	for _, tc := range cases {
		if got := clampConfidence(tc.in); got != tc.want {
			t.Errorf("clampConfidence(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestWindowAfterKeepsRunesIntact(t *testing.T) {
	t.Parallel()

	const multi = "aébc"
	got := windowAfter(multi, 1, 2)
	if got != "é" {
		t.Fatalf("windowAfter = %q, want é", got)
	}
}

func TestPauseClauseStartCutsAtASeparator(t *testing.T) {
	t.Parallel()

	const text = "it stalled. the session paused"
	pos := len("it stalled. ")
	if got := pauseClauseStart(text, pos); got != pos {
		t.Fatalf("pauseClauseStart = %d, want %d", got, pos)
	}
	if got := pauseClauseStart("goal paused", 0); got != 0 {
		t.Fatalf("pauseClauseStart at the start = %d, want 0", got)
	}
}

func TestExtractPausedGoalStartsAtTheClause(t *testing.T) {
	t.Parallel()

	paused, message := extractPausedGoal("it stalled. the session paused")
	if !paused {
		t.Fatal("expected a pause")
	}
	if message != "the session paused" {
		t.Fatalf("message = %q, want %q", message, "the session paused")
	}
}
