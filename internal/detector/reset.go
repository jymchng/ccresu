package detector

import (
	"regexp"
	"strconv"
	"strings"
)

// ResetPhrase is the reset information carried by a usage-limit message.
//
// The detector only splits the phrase into its parts. Resolving "12pm" against
// a calendar, a timezone and daylight-saving rules is the scheduler's job;
// keeping the parts separate means the scheduler can resolve the same phrase
// again later without re-reading the message.
type ResetPhrase struct {
	// Present reports whether the message carried reset information at all.
	Present bool
	// Raw is the phrase as written, e.g. "12pm (Europe/Berlin)".
	Raw string
	// Expression is the time expression alone, e.g. "12pm", "Monday 9am",
	// "4 hours", "tomorrow" or "2026-06-15 09:00".
	Expression string
	// Timezone is the IANA zone written next to the expression, e.g.
	// "Europe/Berlin". Empty when the message relied on the ambient zone.
	Timezone string
	// Relative reports that Expression is a duration from now, e.g.
	// "4 hours" in "resets in 4 hours".
	Relative bool
	// EpochUnix is the reset instant when the message carried a Unix timestamp
	// instead of a phrase, in seconds. Zero otherwise.
	EpochUnix int64
}

// HasTimezone reports whether the phrase named its own timezone.
func (r ResetPhrase) HasTimezone() bool { return r.Timezone != "" }

// timeOfDay matches a wall-clock time: "12pm", "12:30 PM", "14:00".
const timeOfDay = `(?:\d{1,2}:\d{2}(?:\s*(?:am|pm|a\.m\.|p\.m\.))?|\d{1,2}\s*(?:am|pm|a\.m\.|p\.m\.))`

// durationUnit matches a single duration component.
const durationUnit = `(?:hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s)`

// duration matches "4 hours", "3h 20m" and "90 minutes".
const durationExpr = `\d+\s*` + durationUnit + `\b(?:\s*(?:and\s*)?\d+\s*(?:minutes?|mins?|m|seconds?|secs?|s)\b)?`

// weekday matches "Monday", "Mon", "tues".
const weekday = `(?:mon|tue|tues|wed|wednes|thu|thur|thurs|fri|sat|satur|sun)(?:day)?`

// month matches "January" and "Jan".
const month = `(?:jan|feb|mar|apr|may|jun|jul|aug|sep|sept|oct|nov|dec)[a-z]*`

// zone is the shape of a timezone written next to a reset time.
//
// A generic word is deliberately rejected: without the slash requirement
// "resets 12pm tomorrow" would read "tomorrow" as the timezone. Only a
// region/city name or a well-known abbreviation counts.
const zone = `[A-Za-z][A-Za-z0-9_+\-]*\/[A-Za-z0-9_+\-]+|(?i:UTC|GMT|UT|Z|EST|EDT|CST|CDT|MST|MDT|PST|PDT|CET|CEST|BST|IST|JST|SGT|KST|AEST|AEDT|NZST|NZDT)`

// resetExpression is the complete grammar a reset phrase may use. The order of
// the alternatives matters: a date must be tried before a bare clock time, and a
// duration before both, so "4 hours" is not read as "4".
const resetExpression = `(?:` + durationExpr +
	`|(?:today|tomorrow|tonight)(?:\s*,?\s*(?:at\s+)?` + timeOfDay + `)?` +
	`|midnight|noon` +
	`|` + weekday + `(?:\s*,?\s*(?:at\s+)?` + timeOfDay + `)?` +
	`|` + month + `\s+\d{1,2}(?:,?\s*\d{4})?(?:\s+(?:at\s+)?` + timeOfDay + `)?` +
	`|\d{4}-\d{2}-\d{2}(?:[ t]\d{2}:\d{2}(?::\d{2})?)?` +
	`|` + timeOfDay + `(?:\s+(?:today|tomorrow|tonight))?)`

var (
	// resetKeywordPattern finds the words that introduce a reset phrase.
	resetKeywordPattern = regexp.MustCompile(`(?i)\b(?:resets?|will reset|try again|available again|back at)\b`)

	// resetExprPattern captures the phrase that follows such a word.
	resetExprPattern = regexp.MustCompile(`(?i)^[\s:,.!\-]*(?:(?P<prep>in|at|on|by|after|around|until)\s+)?(?P<expr>` +
		resetExpression + `)(?:\s*\((?P<zone>` + zone + `)\))?(?:\s+(?P<zone2>(?i:UTC|GMT|Z)|` + zone + `))?`)

	// durationOnlyPattern reports whether an expression is a relative duration.
	durationOnlyPattern = regexp.MustCompile(`(?i)^` + durationExpr + `$`)

	// epochResetPattern finds the legacy "usage limit reached|<epoch>" form.
	epochResetPattern = regexp.MustCompile(`(?i)usage limit reached\|(\d{9,19})`)
)

// extractReset finds and decomposes the reset phrase in a case-preserving
// message.
func extractReset(spaced string) ResetPhrase {
	if strings.TrimSpace(spaced) == "" {
		return ResetPhrase{}
	}
	if m := epochResetPattern.FindStringSubmatch(spaced); m != nil {
		if value, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			// Normalise to seconds: 19 digits are nanoseconds, 16 are
			// microseconds and 13 are milliseconds.
			switch {
			case value > 1e17:
				value /= 1e9
			case value > 1e15:
				value /= 1e6
			case value > 1e12:
				value /= 1e3
			}
			return ResetPhrase{Present: true, Raw: m[1], Expression: m[1], EpochUnix: value}
		}
	}

	for _, loc := range resetKeywordPattern.FindAllStringIndex(spaced, -1) {
		rest := windowAfter(spaced, loc[1], 160)
		m := resetExprPattern.FindStringSubmatch(rest)
		if m == nil {
			continue
		}
		expression := strings.TrimSpace(m[2])
		if expression == "" {
			continue
		}
		timezone := strings.TrimSpace(m[3])
		if timezone == "" {
			timezone = strings.TrimSpace(m[4])
		}
		raw := expression
		if timezone != "" {
			raw = expression + " (" + timezone + ")"
		}
		prep := strings.ToLower(strings.TrimSpace(m[1]))
		return ResetPhrase{
			Present:    true,
			Raw:        raw,
			Expression: expression,
			Timezone:   timezone,
			Relative:   prep == "in" || durationOnlyPattern.MatchString(expression),
		}
	}
	return ResetPhrase{}
}
