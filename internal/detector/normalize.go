package detector

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// ansiPattern matches the escape sequences a terminal-aware caller may have
// captured: CSI colour/cursor sequences and OSC titles.
var ansiPattern = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)")

// wrappedHyphenPattern rejoins a numeric qualifier the terminal wrapped after
// its hyphen, e.g. "5-\nhour" -> "5-hour". It is restricted to digits so that
// ordinary hyphenated prose is left alone.
var wrappedHyphenPattern = regexp.MustCompile(`(\d)- (\p{L})`)

// normalizedText holds the two canonical views of one raw message.
//
// Both views share the same punctuation and whitespace folding, so a reset
// phrase can be located with a case-insensitive pattern in spaced while the
// detection patterns run against folded. They are deliberately not index
// aligned: case folding may change a rune's encoded length, and no caller needs
// to translate an offset between them.
type normalizedText struct {
	// spaced preserves the author's capitalisation, which matters for IANA
	// timezone names such as "Europe/Berlin".
	spaced string
	// folded is spaced with case removed, the form every pattern is written
	// against.
	folded string
}

// Normalize folds raw session output into the canonical form the built-in
// patterns are written against.
//
// The result is lowercase, single-spaced, free of ANSI escapes and uses ASCII
// punctuation. It is stable and idempotent: Normalize(Normalize(s)) equals
// Normalize(s).
func Normalize(raw string) string { return normalize(raw).folded }

func normalize(raw string) normalizedText {
	if raw == "" {
		return normalizedText{}
	}
	if strings.ContainsRune(raw, 0x1b) {
		raw = ansiPattern.ReplaceAllString(raw, " ")
	}

	var b strings.Builder
	b.Grow(len(raw))
	for _, r := range raw {
		switch {
		case r == '\u00a0', r == '\u1680', r == '\u2000', r == '\u2001', r == '\u2002',
			r == '\u2003', r == '\u2004', r == '\u2005', r == '\u2006', r == '\u2007',
			r == '\u2008', r == '\u2009', r == '\u200a', r == '\u202f', r == '\u205f',
			r == '\u3000':
			b.WriteByte(' ')
		case r == '\u200b', r == '\u200c', r == '\u200d', r == '\u2060', r == '\ufeff',
			r == '\u00ad', r == 0x7f:
			// Zero-width joiners, soft hyphens and DEL carry no content.
		case r == '\u2018', r == '\u2019', r == '\u201a', r == '\u201b', r == '\u2032',
			r == '\u00b4', r == '\u02bc':
			b.WriteByte('\'')
		case r == '\u201c', r == '\u201d', r == '\u201e', r == '\u201f', r == '\u2033',
			r == '\u00ab', r == '\u00bb':
			b.WriteByte('"')
		case r == '\u2010', r == '\u2011', r == '\u2012', r == '\u2013', r == '\u2014',
			r == '\u2015', r == '\u2212', r == '\ufe63':
			b.WriteByte('-')
		case r == '\u00b7', r == '\u2022', r == '\u2023', r == '\u2027', r == '\u2219',
			r == '\u25cf', r == '\u25aa', r == '\u25e6':
			b.WriteByte(':')
		case r == '\u2026':
			b.WriteString("...")
		case r < 0x20:
			b.WriteByte(' ')
		case r >= 0xff01 && r <= 0xff5e:
			// Fullwidth ASCII variants, common in CJK terminals.
			b.WriteRune(r - 0xfee0)
		case r >= 0x2500 && r <= 0x257f:
			// Box drawing from a captured TUI frame.
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}

	spaced := strings.Join(strings.Fields(b.String()), " ")
	spaced = wrappedHyphenPattern.ReplaceAllString(spaced, "$1-$2")
	return normalizedText{spaced: spaced, folded: strings.ToLower(spaced)}
}

// windowBefore returns at most n bytes of s immediately before end, widened to a
// rune boundary.
func windowBefore(s string, end, n int) string {
	if end > len(s) {
		end = len(s)
	}
	start := end - n
	if start < 0 {
		start = 0
	}
	for start < end && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:end]
}

// windowAfter returns at most n bytes of s starting at start, widened to a rune
// boundary.
func windowAfter(s string, start, n int) string {
	if start > len(s) {
		return ""
	}
	end := start + n
	if end > len(s) {
		end = len(s)
	}
	for end < len(s) && !utf8.RuneStart(s[end]) {
		end++
	}
	return s[start:end]
}

// runeBounded truncates s to at most n runes without splitting one.
func runeBounded(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
