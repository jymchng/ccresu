package detector

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Guard patterns. Each one lowers the confidence of a match that looks like
// prose about a limit rather than an actual interruption.
//
// The penalties are chosen so that the canonical banner (0.99) survives almost
// everything, while a generic match (0.9) drops below the default 0.5
// threshold once a single strong marker applies.
var (
	// negationBefore: "not", "never", "no longer", "haven't"…
	negationBefore = regexp.MustCompile(`\b(?:not|never|no|none|zero|without|n't|haven't|hasn't|hadn't|didn't|doesn't|don't|won't|wouldn't|isn't|aren't|wasn't|weren't|cannot|can't|yet to)\b\s*(?:\w+\s+){0,2}$`)

	// conditionalBefore: a hypothetical or a dependency.
	conditionalBefore = regexp.MustCompile(`\b(?:if|when|whenever|once|unless|whether|before|after|anytime|any time|in case|in the event|suppose|assuming|should)\b.*$`)

	// modalBefore: "you can hit", "you might reach".
	modalBefore = regexp.MustCompile(`\b(?:can|could|may|might|would|should|will|'ll)\s+(?:just\s+|also\s+|easily\s+)?(?:hit|reach|exceed|run out of|be)\b.*$`)

	// knowledgeBefore: the message is being named, quoted or described.
	strongKnowledgeBefore = regexp.MustCompile(`\b(?:phrase|regexp?|regex|pattern|literal|do(?:c|cs)|documentation)\b.*$`)
	weakKnowledgeBefore   = regexp.MustCompile(`\b(?:message|string|text|caption|banner|wording)\b.*$`)

	// detectVerbBefore: "ccresu detects You've hit…".
	detectVerbBefore = regexp.MustCompile(`\b(?:detects?|detected|recogni[sz]es?|recogni[sz]ed|matches?|matched|parses?|parsed|prints?|printed)\b.*$`)

	// thirdPersonBefore: documentation talking about users in general.
	thirdPersonBefore = regexp.MustCompile(`\b(?:users?|someone|somebody|people|developers?|anyone|whoever)\b.*$`)

	// exampleBefore: an illustration rather than a live message.
	exampleBefore = regexp.MustCompile(`\b(?:for example|for instance|such as|e\.g\.|i\.e\.|example)\b.*$`)

	// transientBefore: a rate limiter that retries by itself.
	transientBefore = regexp.MustCompile(`\b(?:retrying|retry(?:ing)? (?:after|in)|backing off|temporarily unavailable|please retry|will retry)\b`)

	// discussingAfter: the sentence explains what the phrase means.
	discussingAfter = regexp.MustCompile(`\b(?:means|indicates|refers to|looks like|happens when|the error message|this error|this message|that message|is shown|is displayed|appears when)\b`)
)

// applyGuards scores how much a match looks like prose rather than an event.
//
// It returns the total penalty to subtract from the pattern confidence and one
// note per guard that fired, so a surprising decision can be explained in logs.
// The penalty is capped at 1 so confidence never goes negative.
func applyGuards(text string, start, end int) (float64, []string) {
	if start < 0 || end > len(text) || start >= end {
		return 0, nil
	}
	before := windowBefore(text, start, 80)
	clause := clauseBefore(text, start, 80)
	after := windowAfter(text, end, 80)

	var penalty float64
	var notes []string
	fire := func(name string, p float64) {
		penalty += p
		notes = append(notes, name)
	}

	if conditionalBefore.MatchString(clause) {
		fire("conditional", 0.55)
	}
	// The modal verb sits just before the matched text, so the look-behind has
	// to include the match itself to see "might hit".
	if modalBefore.MatchString(clause + text[start:end]) {
		if !containsNote(notes, "conditional") {
			fire("modal", 0.5)
		}
	}
	if negationBefore.MatchString(clause) {
		fire("negation", 1)
	}
	if strongKnowledgeBefore.MatchString(before) || detectVerbBefore.MatchString(before) {
		fire("described", 0.6)
	}
	if weakKnowledgeBefore.MatchString(before) {
		fire("named", 0.25)
	}
	if thirdPersonBefore.MatchString(clause) {
		fire("third-person", 0.4)
	}
	if exampleBefore.MatchString(before) {
		fire("example", 0.5)
	}
	if transientBefore.MatchString(text) {
		fire("transient-retry", 0.5)
	}
	if isQuoted(text, start, end) {
		fire("quoted", 0.5)
	}
	if insideCodeFence(text, start) {
		fire("code-fence", 0.8)
	}
	if isQuestion(text, end) {
		fire("question", 0.55)
	}
	if strings.HasPrefix(strings.TrimSpace(text), ">") {
		fire("markdown-quote", 0.5)
	}
	if discussingAfter.MatchString(after) {
		fire("explained", 0.3)
	}

	if penalty > 1 {
		penalty = 1
	}
	return penalty, notes
}

// clauseBefore narrows a look-behind window to the clause that actually
// contains the match.
//
// Without this, "It works well. You've hit your 5-hour limit …" could be demoted
// by an unrelated word in the previous sentence, or a documentation paragraph
// could suppress a real banner that follows it. Abbreviations such as "e.g."
// are not treated as sentence ends.
func clauseBefore(text string, start, n int) string {
	w := windowBefore(text, start, n)
	cut := 0
	for _, sep := range []string{". ", "! ", "? ", ": ", "; "} {
		idx := strings.LastIndex(w, sep)
		if idx < 0 {
			continue
		}
		if isAbbreviation(w[:idx]) {
			continue
		}
		if idx+len(sep) > cut {
			cut = idx + len(sep)
		}
	}
	return w[cut:]
}

// isAbbreviation reports whether prefix ends in a common abbreviation whose
// full stop is not a sentence boundary.
func isAbbreviation(prefix string) bool {
	lower := strings.ToLower(prefix)
	for _, abbr := range []string{"e.g", "i.e", "etc", "vs", "approx"} {
		if strings.HasSuffix(lower, abbr) {
			return true
		}
	}
	return false
}

// isQuoted reports whether the matched span is wrapped in matching quotes or
// backticks. An apostrophe inside a word is not a delimiter, so "you've" never
// counts as an opening quote.
func isQuoted(text string, start, end int) bool {
	before := strings.TrimRight(text[:start], " ")
	after := strings.TrimLeft(text[end:], " ")
	if before == "" || after == "" {
		return false
	}
	open, _ := utf8.DecodeLastRuneInString(before)
	close, _ := utf8.DecodeRuneInString(after)
	switch open {
	case '"':
		return close == '"'
	case '\'':
		return close == '\''
	case '`':
		return close == '`'
	case '[':
		return close == ']'
	default:
		return false
	}
}

// insideCodeFence reports whether start falls inside a fenced code block.
func insideCodeFence(text string, start int) bool {
	return strings.Count(text[:start], "```")%2 == 1
}

// isQuestion reports whether the sentence that contains the match asks a
// question. Only a question mark before the next full stop counts, so a trailing
// question about an unrelated matter does not demote a real banner.
func isQuestion(text string, end int) bool {
	tail := windowAfter(text, end, 40)
	if i := strings.IndexByte(tail, '?'); i >= 0 {
		return !strings.Contains(tail[:i], ".")
	}
	return false
}

func containsNote(notes []string, want string) bool {
	for _, note := range notes {
		if note == want {
			return true
		}
	}
	return false
}
