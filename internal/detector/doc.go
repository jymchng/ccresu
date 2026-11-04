// Package detector recognises Claude Code usage-limit interruptions in session
// output.
//
// Detection is deliberately conservative: a message is reported as a usage
// limit only when a known phrasing matches with enough confidence, and a set of
// guards demotes text that merely *discusses* limits — documentation, quotes,
// questions, hypotheticals or examples.
//
// The package also separates a usage limit from the other ways a session can
// stop. Authentication errors, network errors, invalid API keys, user
// cancellation and normal completion are classified explicitly instead of being
// mistaken for a limit, so the recovery engine can treat each case differently.
//
// # Example
//
//	event := detector.Analyze("You've hit your 5-hour limit · resets 12pm (Europe/Berlin)")
//	if event.Detected {
//		fmt.Println(event.Limit, event.Reset.Expression, event.Reset.Timezone)
//		// five_hour 12pm Europe/Berlin
//	}
//
// # Normalisation
//
// Real output arrives wrapped, coloured and with typographic punctuation.
// [Normalize] strips ANSI escapes, folds every Unicode space onto a single
// space, maps curly quotes, en/em dashes, bullets and fullwidth characters onto
// their ASCII equivalents, rejoins words the terminal wrapped after a hyphen,
// and lowercases the result. Patterns are written against that canonical form,
// so a single rule covers "You've hit your 5-hour limit", "you’ve hit your
// 5–hour\nlimit" and a fullwidth "Ｙｏｕ'ｖｅ" equally.
//
// # Extensibility
//
// [Options.Patterns] appends rules to the built-in set. A rule is a regular
// expression over the normalised text plus a confidence, which lets a new
// Claude Code phrasing be taught to ccresu without a code change.
package detector
