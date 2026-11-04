package detector

import (
	"regexp"
	"strings"
)

// pausedGoalPattern matches the paused-goal banner. The CLI announces a paused
// goal separately from the limit notice, and the two can arrive in either
// order, so they are extracted independently and combined afterwards.
var pausedGoalPattern = regexp.MustCompile(`(?i)\b(?:goal|session|task|work|run)\s+(?:is\s+|has been\s+|was\s+)?paused\b`)

// pausedUntilPattern matches "paused until the limit resets", which names no
// goal but still reports a pause.
var pausedUntilPattern = regexp.MustCompile(`(?i)\bpaused\s+(?:until|by|due to|because of)\b`)

// extractPausedGoal reports whether the message says the active goal is paused
// and returns the wording.
func extractPausedGoal(spaced string) (bool, string) {
	loc := pausedGoalPattern.FindStringIndex(spaced)
	if loc == nil {
		loc = pausedUntilPattern.FindStringIndex(spaced)
	}
	if loc == nil {
		return false, ""
	}
	// Start at the beginning of the clause so "the session has been paused"
	// is reported whole rather than from "session".
	start := pauseClauseStart(spaced, loc[0])
	message := strings.TrimSpace(runeBounded(spaced[start:], 240))
	return true, message
}

// pauseClauseStart walks back to the start of the clause that contains pos,
// stopping at a sentence or separator boundary.
func pauseClauseStart(text string, pos int) int {
	window := windowBefore(text, pos, 60)
	cut := 0
	for _, sep := range []string{". ", "! ", "? ", ": ", "; ", " - "} {
		if i := strings.LastIndex(window, sep); i >= 0 && i+len(sep) > cut {
			cut = i + len(sep)
		}
	}
	return pos - len(window) + cut
}
