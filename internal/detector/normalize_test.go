package detector

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain", "Hello World", "hello world"},
		{"csi colour", "\x1b[31mRed\x1b[0m text", "red text"},
		{"osc title", "\x1b]0;window title\x07Body", "body"},
		{"no-break space", "You\u00a0hit\u00a0your limit", "you hit your limit"},
		{"narrow no-break space", "your\u202flimit", "your limit"},
		{"ideographic space", "your\u3000limit", "your limit"},
		{"figure space", "your\u2007limit", "your limit"},
		{"en space", "your\u2002limit", "your limit"},
		{"curly apostrophe", "You\u2019ve hit your limit", "you've hit your limit"},
		{"curly double quotes", "\u201cquoted\u201d", "\"quoted\""},
		{"prime", "5\u2032", "5'"},
		{"en dash", "5\u2013hour", "5-hour"},
		{"em dash", "a\u2014b", "a-b"},
		{"minus sign", "5\u2212hour", "5-hour"},
		{"small hyphen", "5\ufe63hour", "5-hour"},
		{"bullet separator", "limit \u00b7 resets", "limit : resets"},
		{"black circle", "limit \u25cf resets", "limit : resets"},
		{"ellipsis", "wait\u2026", "wait..."},
		{"zero width space", "li\u200bmit", "limit"},
		{"zero width joiner", "li\u200dmit", "limit"},
		{"byte order mark", "\ufeffYou've hit", "you've hit"},
		{"soft hyphen", "li\u00admit", "limit"},
		{"delete", "a\x7fb", "ab"},
		{"fullwidth", "\uff39\uff4f\uff55'\uff56\uff45 \uff48\uff49\uff54", "you've hit"},
		{"box drawing", "\u2502 You've hit \u2502", "you've hit"},
		{"control characters", "a\x00b\x1fc", "a b c"},
		{"tab and newline", "a\tb\nc", "a b c"},
		{"carriage return", "a\r\nb", "a b"},
		{"wrapped hyphen", "5-\nhour limit", "5-hour limit"},
		{"trailing spaces", "  hi  ", "hi"},
		{"already canonical", "you've hit your limit", "you've hit your limit"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Normalize(tc.in); got != tc.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeIsIdempotent(t *testing.T) {
	t.Parallel()

	inputs := []string{
		"You've hit your 5-hour limit · resets 12pm (Europe/Berlin)",
		"\x1b[1mＹｏｕ'ｖｅ\x1b[0m hit your 5–hour\nlimit",
		"   ",
		"",
		"Claude AI usage limit reached|1730000000",
	}
	for _, in := range inputs {
		once := Normalize(in)
		twice := Normalize(once)
		if once != twice {
			t.Fatalf("Normalize is not idempotent for %q: %q then %q", in, once, twice)
		}
	}
}

func TestNormalizePreservesTimezoneCase(t *testing.T) {
	t.Parallel()

	got := normalize("resets 12pm (Europe/Berlin)")
	if got.spaced != "resets 12pm (Europe/Berlin)" {
		t.Fatalf("spaced = %q, want the original capitalisation preserved", got.spaced)
	}
	if got.folded != "resets 12pm (europe/berlin)" {
		t.Fatalf("folded = %q, want a lowercase copy", got.folded)
	}
}

func TestWindowHelpers(t *testing.T) {
	t.Parallel()

	const text = "abcdefghij"
	if got := windowBefore(text, 5, 3); got != "cde" {
		t.Fatalf("windowBefore = %q, want cde", got)
	}
	if got := windowBefore(text, 2, 10); got != "ab" {
		t.Fatalf("windowBefore clamped = %q, want ab", got)
	}
	if got := windowBefore(text, len(text)+5, 3); got != "hij" {
		t.Fatalf("windowBefore past the end = %q, want hij", got)
	}
	if got := windowAfter(text, 5, 3); got != "fgh" {
		t.Fatalf("windowAfter = %q, want fgh", got)
	}
	if got := windowAfter(text, 8, 10); got != "ij" {
		t.Fatalf("windowAfter clamped = %q, want ij", got)
	}
	if got := windowAfter(text, len(text)+1, 3); got != "" {
		t.Fatalf("windowAfter past the end = %q, want empty", got)
	}

	// A window that starts mid-rune must widen to the rune boundary.
	const multi = "aébc"
	if got := windowBefore(multi, 4, 2); !strings.HasPrefix(got, "b") {
		t.Fatalf("windowBefore split a rune: %q", got)
	}
}

func TestRuneBounded(t *testing.T) {
	t.Parallel()

	if got := runeBounded("abcdef", 3); got != "abc" {
		t.Fatalf("runeBounded = %q, want abc", got)
	}
	if got := runeBounded("abc", 10); got != "abc" {
		t.Fatalf("runeBounded over the length = %q, want abc", got)
	}
	if got := runeBounded("abc", 0); got != "" {
		t.Fatalf("runeBounded zero = %q, want empty", got)
	}
	if got := runeBounded("abc", -1); got != "" {
		t.Fatalf("runeBounded negative = %q, want empty", got)
	}
	// Multi-byte runes must not be split.
	if got := runeBounded("héllo", 2); got != "hé" {
		t.Fatalf("runeBounded multi-byte = %q, want hé", got)
	}
	if got := runeBounded("héllo", 5); got != "héllo" {
		t.Fatalf("runeBounded exact = %q, want héllo", got)
	}
}
