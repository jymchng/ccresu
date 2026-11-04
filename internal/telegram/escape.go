package telegram

import "strings"

// ParseModeHTML is the only parse mode ccresu sends.
//
// Every interpolated value is escaped, so a session id, project path or CLI
// error containing markup cannot break the message or inject formatting.
const ParseModeHTML = "HTML"

// htmlEscaper replaces the characters Telegram's HTML parser treats as markup.
//
// A single pass is essential: escaping "&" first and then the other characters
// in separate passes would double-escape the entities this pass produces.
var htmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
)

// Escape renders s safely for parse_mode=HTML.
//
// Telegram accepts only a small tag vocabulary and rejects the whole message
// when a stray "<" appears, so every dynamic value in a template goes through
// here.
func Escape(s string) string {
	if s == "" {
		return ""
	}
	return htmlEscaper.Replace(s)
}

// Code renders s as an escaped <code> span, for identifiers.
func Code(s string) string {
	return "<code>" + Escape(s) + "</code>"
}

// Bold renders s as an escaped <b> span.
func Bold(s string) string {
	return "<b>" + Escape(s) + "</b>"
}
