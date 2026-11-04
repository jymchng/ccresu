package telegram

import (
	"strings"
	"testing"
)

func TestEscape(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"plain", "plain"},
		{"<b>bold</b>", "&lt;b&gt;bold&lt;/b&gt;"},
		{"a & b", "a &amp; b"},
		{`say "hi"`, "say &quot;hi&quot;"},
		{"5 > 3 < 7", "5 &gt; 3 &lt; 7"},
		// A single pass is required: & must not be escaped twice.
		{"&amp;", "&amp;amp;"},
		{"You've hit your 5-hour limit · resets 12pm", "You've hit your 5-hour limit · resets 12pm"},
	}
	for _, tc := range cases {
		if got := Escape(tc.in); got != tc.want {
			t.Errorf("Escape(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCodeAndBoldEscapeTheirContent(t *testing.T) {
	if got, want := Code("<x>"), "<code>&lt;x&gt;</code>"; got != want {
		t.Errorf("Code = %q, want %q", got, want)
	}
	if got, want := Bold("<x>"), "<b>&lt;x&gt;</b>"; got != want {
		t.Errorf("Bold = %q, want %q", got, want)
	}
	if got := Code("plain"); !strings.HasPrefix(got, "<code>") || !strings.HasSuffix(got, "</code>") {
		t.Errorf("Code = %q, want a code span", got)
	}
}
