package app

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jymchng/ccresu/internal/storage"
)

// reportedStates is the state list status reports in, in lifecycle order. It is
// spelled out here rather than derived from the database so that a state with no
// rows still appears as 0 instead of vanishing from the report.
var reportedStates = []storage.State{
	storage.StateDiscovered,
	storage.StateRunning,
	storage.StateUsageLimitDetected,
	storage.StateWaitingForReset,
	storage.StateResetTimeReached,
	storage.StateCheckingSession,
	storage.StateCheckingLimit,
	storage.StateResuming,
	storage.StateResumed,
	storage.StateRetryWait,
	storage.StatePermanentlyFailed,
	storage.StateCompleted,
	storage.StateUserTerminated,
	storage.StateDisabled,
	storage.StateUnrecoverable,
}

// table writes aligned columns. Every command uses it so the output is greppable
// and stable.
func (a *App) table(w io.Writer, headers []string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, row := range rows {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	_ = tw.Flush()
}

// writeJSON emits an indented JSON document, which is the machine-readable form
// of a command's report.
func (a *App) writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode JSON report: %w", err)
	}
	return nil
}

// field prints one "name:      value" line.
func (a *App) field(w io.Writer, name string, value any) {
	fmt.Fprintf(w, "%-14s %v\n", name+":", value)
}

// formatTime renders t in the configured zone, or "-" for a zero time.
func formatTime(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "-"
	}
	if loc == nil {
		loc = time.UTC
	}
	return t.In(loc).Format("2006-01-02 15:04:05 MST")
}

// formatDuration renders a duration compactly, or "-" when it is zero.
func formatDuration(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	return d.Round(time.Second).String()
}

// shortID trims a session id to its first eight characters, which is unambiguous
// in practice and keeps a table readable. The full id is always available from
// `ccresu inspect`.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// truncateText bounds a free-text column.
func truncateText(s string, limit int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= limit {
		return s
	}
	if limit <= 3 {
		return s[:limit]
	}
	return s[:limit-3] + "..."
}

// yesNo renders a boolean the way an operator reads it.
func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
