package detector

import (
	"strings"
	"testing"
)

func TestApplyGuardsRejectsInvalidRanges(t *testing.T) {
	t.Parallel()

	text := "you've hit your limit"
	cases := []struct{ start, end int }{
		{-1, 5},
		{0, len(text) + 1},
		{5, 5},
		{9, 4},
	}
	for _, tc := range cases {
		penalty, notes := applyGuards(text, tc.start, tc.end)
		if penalty != 0 || notes != nil {
			t.Fatalf("applyGuards(%d,%d) = %v,%v, want 0,nil", tc.start, tc.end, penalty, notes)
		}
	}
}

func TestGuardFires(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		text   string
		needle string
		want   string
	}{
		{"conditional", "if you've hit your limit", "you've hit your limit", "conditional"},
		{"modal", "you might hit your limit", "hit your limit", "modal"},
		{"negation", "there is no usage limit", "usage limit", "negation"},
		{"described by a phrase", "the phrase says you've hit your limit", "you've hit your limit", "described"},
		{"described by a verb", "ccresu detects you've hit your limit", "you've hit your limit", "described"},
		{"named as a message", "a banner message reading you've hit your limit", "you've hit your limit", "named"},
		{"third person", "users who hit their limit", "hit their limit", "third-person"},
		{"example", "for example you've hit your limit", "you've hit your limit", "example"},
		{"transient retry", "limit reached, retrying in 5 seconds", "limit reached", "transient-retry"},
		{"quoted", `"you've hit your limit"`, "you've hit your limit", "quoted"},
		{"code fence", "```\nyou've hit your limit\n```", "you've hit your limit", "code-fence"},
		{"question", "did you hit your limit?", "hit your limit", "question"},
		{"markdown quote", "> you've hit your limit", "you've hit your limit", "markdown-quote"},
		{"explained after", "you've hit your limit, which means ccresu waits", "you've hit your limit", "explained"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			start := strings.Index(tc.text, tc.needle)
			if start < 0 {
				t.Fatalf("needle %q not in %q", tc.needle, tc.text)
			}
			penalty, notes := applyGuards(tc.text, start, start+len(tc.needle))
			if penalty <= 0 {
				t.Fatalf("penalty = %v, want a positive value (notes %v)", penalty, notes)
			}
			if !containsNote(notes, tc.want) {
				t.Fatalf("notes = %v, want %q", notes, tc.want)
			}
		})
	}
}

func TestGuardDoesNotFireOnCleanBanner(t *testing.T) {
	t.Parallel()

	text := "you've hit your 5-hour limit : resets 12pm (europe/berlin)"
	start := strings.Index(text, "you've hit your 5-hour limit")
	penalty, notes := applyGuards(text, start, start+len("you've hit your 5-hour limit"))
	if penalty != 0 {
		t.Fatalf("penalty = %v, want 0 (notes %v)", penalty, notes)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %v, want none", notes)
	}
}

func TestApplyGuardsCapsThePenalty(t *testing.T) {
	t.Parallel()

	// Every guard at once must not push the penalty above one.
	text := "```\n> if users see \"you've hit your limit?\" the phrase means nothing, retrying\n```"
	start := strings.Index(text, "you've hit your limit")
	penalty, notes := applyGuards(text, start, start+len("you've hit your limit"))
	if penalty != 1 {
		t.Fatalf("penalty = %v, want it capped at 1 (notes %v)", penalty, notes)
	}
}

func TestIsQuoted(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		text   string
		needle string
		want   bool
	}{
		{"double quotes", `"you've hit your limit"`, "you've hit your limit", true},
		{"single quotes", `'you've hit your limit'`, "you've hit your limit", true},
		{"backticks", "`you've hit your limit`", "you've hit your limit", true},
		{"brackets", "[you've hit your limit]", "you've hit your limit", true},
		{"apostrophe inside the span", "you've hit your limit", "you've hit your limit", false},
		{"mismatched quotes", `"you've hit your limit'`, "you've hit your limit", false},
		{"open at the start of the text", "you've hit your limit'", "you've hit your limit", false},
		{"nothing after", `"you've hit your limit"`, "you've hit your limit", true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			start := strings.Index(tc.text, tc.needle)
			if got := isQuoted(tc.text, start, start+len(tc.needle)); got != tc.want {
				t.Fatalf("isQuoted(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

func TestInsideCodeFence(t *testing.T) {
	t.Parallel()

	text := "```\nlimit reached\n```"
	inside := strings.Index(text, "limit")
	outside := strings.Index(text, "```") // the fence itself is not inside
	if !insideCodeFence(text, inside) {
		t.Fatal("expected the inner text to be inside a fence")
	}
	if insideCodeFence(text, outside) {
		t.Fatal("the opening fence must not count as inside")
	}
	if insideCodeFence("no fence here", 3) {
		t.Fatal("plain text has no fence")
	}
}

func TestIsQuestion(t *testing.T) {
	t.Parallel()

	cases := []struct {
		text string
		end  int
		want bool
	}{
		{"did you hit your limit?", len("did you hit your limit"), true},
		{"you've hit your limit?", len("you've hit your limit"), true},
		{"you've hit your limit. continue?", len("you've hit your limit"), false},
		{"you've hit your limit", len("you've hit your limit"), false},
		{"you've hit your limit far away ?", len("you've hit your limit"), true},
	}
	for _, tc := range cases {
		if got := isQuestion(tc.text, tc.end); got != tc.want {
			t.Errorf("isQuestion(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestClauseBefore(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		text  string
		start int
		want  string
	}{
		{"single sentence", "if you've", 3, "if "},
		{"keeps only the current sentence", "it works well. you've", 15, ""},
		{"keeps only the current clause", "note: you've", 6, ""},
		{"keeps an abbreviation", "e.g. you've", 5, "e.g. "},
		{"keeps i.e.", "i.e. you've", 5, "i.e. "},
		{"no separator", "you've", 0, ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := clauseBefore(tc.text, tc.start, 80); got != tc.want {
				t.Fatalf("clauseBefore(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestIsAbbreviation(t *testing.T) {
	t.Parallel()

	for _, in := range []string{"e.g", "i.e", "etc", "vs", "approx", "E.G"} {
		if !isAbbreviation(in) {
			t.Errorf("%q must be recognised as an abbreviation", in)
		}
	}
	for _, in := range []string{"the end", "works", ""} {
		if isAbbreviation(in) {
			t.Errorf("%q must not be an abbreviation", in)
		}
	}
}

func TestContainsNote(t *testing.T) {
	t.Parallel()

	notes := []string{"conditional", "quoted"}
	if !containsNote(notes, "quoted") {
		t.Fatal("expected to find quoted")
	}
	if containsNote(notes, "negation") {
		t.Fatal("did not expect negation")
	}
	if containsNote(nil, "anything") {
		t.Fatal("nil notes contain nothing")
	}
}
