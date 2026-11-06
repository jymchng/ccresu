package app

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/jymchng/ccresu/internal/detector"
	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/storage"
)

// cmdVersion reports the build metadata plus the runtime it is executing on.
func (a *App) cmdVersion(_ context.Context, args []string) error {
	fs := a.flags("version", "version")
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return usagef("version takes no arguments, got %q", rest[0])
	}
	if a.json {
		return a.writeJSON(a.stdout, map[string]string{
			"version":   a.build.Version,
			"commit":    a.build.Commit,
			"buildDate": a.build.BuildDate,
			"go":        runtime.Version(),
			"platform":  runtime.GOOS + "/" + runtime.GOARCH,
		})
	}
	fmt.Fprintf(a.stdout, "ccresu %s\n", a.build.Version)
	a.field(a.stdout, "commit", a.build.Commit)
	a.field(a.stdout, "built", a.build.BuildDate)
	a.field(a.stdout, "go", runtime.Version())
	a.field(a.stdout, "platform", runtime.GOOS+"/"+runtime.GOARCH)
	return nil
}

// statusReport is the machine-readable form of `ccresu status`.
type statusReport struct {
	ConfigPath      string `json:"config_path"`
	DatabasePath    string `json:"database_path"`
	Sessions        int    `json:"sessions"`
	EnabledSessions int    `json:"enabled_sessions"`
	// SessionsListedByDefault is what a bare `ccresu sessions` prints: the
	// enabled rows only. It exists so a script can reconcile this report with
	// `ccresu sessions --json` without having to know the default filter, which
	// is the mismatch that made "sessions: 27 (1 enabled)" and a one-row
	// listing look like two different answers.
	SessionsListedByDefault int            `json:"sessions_listed_by_default"`
	States                  map[string]int `json:"states"`
	PendingRecoveries       int            `json:"pending_recoveries"`
	Schedules               int            `json:"schedules"`
	NextScheduleAt          string         `json:"next_schedule_at,omitempty"`
	PendingNotifications    int            `json:"pending_notifications"`
	TelegramEnabled         bool           `json:"telegram_enabled"`
	RecoveryEnabled         bool           `json:"recovery_enabled"`
	MaxAttempts             int            `json:"max_attempts"`
	Monitoring              bool           `json:"monitoring"`
	Now                     time.Time      `json:"now"`
	SessionIDs              []string       `json:"pending_session_ids,omitempty"`
}

// cmdStatus summarises the persisted state: how many sessions ccresu knows about,
// what they are doing, and what is queued.
func (a *App) cmdStatus(ctx context.Context, args []string) error {
	fs := a.flags("status", "status")
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return usagef("status takes no arguments, got %q", rest[0])
	}
	rt, release, err := a.openRuntime(ctx)
	if err != nil {
		return err
	}
	defer release()

	now := rt.Clock.Now().UTC()
	report := statusReport{
		ConfigPath:      rt.ConfigPath,
		DatabasePath:    rt.Store.Path(),
		States:          make(map[string]int, len(reportedStates)),
		TelegramEnabled: rt.Notifier != nil,
		RecoveryEnabled: rt.Recovery != nil && rt.Recovery.Enabled(),
		Monitoring:      rt.Config != nil && rt.Config.Monitoring.Enabled,
		Now:             now,
	}
	if rt.Recovery != nil {
		report.MaxAttempts = rt.Recovery.MaxAttempts()
	}

	total, err := rt.Store.CountSessions(ctx, storage.SessionFilter{})
	if err != nil {
		return fmt.Errorf("count sessions: %w", err)
	}
	report.Sessions = total

	enabled := true
	enabledCount, err := rt.Store.CountSessions(ctx, storage.SessionFilter{Enabled: &enabled})
	if err != nil {
		return fmt.Errorf("count enabled sessions: %w", err)
	}
	report.EnabledSessions = enabledCount
	report.SessionsListedByDefault = enabledCount

	for _, state := range reportedStates {
		count, err := rt.Store.CountSessions(ctx, storage.SessionFilter{States: []storage.State{state}})
		if err != nil {
			return fmt.Errorf("count %s sessions: %w", state, err)
		}
		if count > 0 {
			report.States[string(state)] = count
		}
	}

	if rt.Recovery != nil {
		pending, err := rt.Recovery.Pending(ctx)
		if err != nil {
			return fmt.Errorf("list pending recoveries: %w", err)
		}
		report.PendingRecoveries = len(pending)
		for _, session := range pending {
			report.SessionIDs = append(report.SessionIDs, session.ID)
		}
	}

	if rt.Scheduler != nil {
		schedules, err := rt.Scheduler.Restore(ctx)
		if err != nil {
			return fmt.Errorf("load schedules: %w", err)
		}
		report.Schedules = len(schedules)
		if next, ok := rt.Scheduler.Next(schedules, now); ok {
			if due, has := next.DueAt(); has {
				report.NextScheduleAt = due.UTC().Format(time.RFC3339)
			}
		}
	}

	pendingNotifications, err := rt.Store.ListPendingNotifications(ctx, 0)
	if err != nil {
		return fmt.Errorf("list pending notifications: %w", err)
	}
	report.PendingNotifications = len(pendingNotifications)

	if a.json {
		return a.writeJSON(a.stdout, report)
	}

	loc := a.displayLocation(rt)
	a.field(a.stdout, "config", report.ConfigPath)
	a.field(a.stdout, "database", report.DatabasePath)
	a.field(a.stdout, "sessions", fmt.Sprintf("%d total, %d enabled", report.Sessions, report.EnabledSessions))
	// Name the filter out loud. "sessions: 27 (1 enabled)" next to a one-row
	// `ccresu sessions` reads like a bug unless the listing rule is stated, and
	// the rule is that a bare listing is enabled-only.
	a.field(a.stdout, "listing", fmt.Sprintf("ccresu sessions shows the %d enabled; ccresu sessions --all shows all %d",
		report.SessionsListedByDefault, report.Sessions))
	a.field(a.stdout, "pending", report.PendingRecoveries)
	a.field(a.stdout, "schedules", report.Schedules)
	if report.NextScheduleAt != "" {
		a.field(a.stdout, "next wake", report.NextScheduleAt)
	}
	a.field(a.stdout, "telegram", yesNo(report.TelegramEnabled))
	a.field(a.stdout, "recovery", fmt.Sprintf("%s (max attempts %d)", yesNo(report.RecoveryEnabled), report.MaxAttempts))
	a.field(a.stdout, "now", formatTime(now, loc))
	if len(report.States) > 0 {
		fmt.Fprintln(a.stdout, "")
		a.table(a.stdout, []string{"STATE", "SESSIONS"}, stateRows(report.States))
	}
	return nil
}

// stateRows turns the state histogram into table rows in lifecycle order.
func stateRows(states map[string]int) [][]string {
	rows := make([][]string, 0, len(states))
	for _, state := range reportedStates {
		if count, ok := states[string(state)]; ok {
			rows = append(rows, []string{string(state), fmt.Sprint(count)})
		}
	}
	return rows
}

// displayLocation resolves app.timezone, falling back to UTC.
//
// Configuration validation already guarantees the zone loads; the fallback exists
// so a hand-edited runtime config cannot panic a read-only command.
func (a *App) displayLocation(rt *Runtime) *time.Location {
	if rt != nil && rt.Config != nil {
		if loc, err := rt.Config.DisplayLocation(); err == nil {
			return loc
		}
	}
	return time.UTC
}

// cmdSessions lists discovered sessions.
func (a *App) cmdSessions(ctx context.Context, args []string) error {
	// The subcommands are dispatched before the listing because `sessions` is
	// also the listing: `ccresu sessions status <id>` must not be read as a
	// filter on a session named "status".
	if name, rest, ok := sessionsSubcommand(args); ok {
		switch name {
		case subcommandStatus:
			return a.inspectSession(ctx, "sessions status", rest)
		case subcommandDelete:
			return a.cmdSessionsDelete(ctx, rest)
		}
	}

	fs := a.flags("sessions", sessionsUsage)
	all := fs.Bool("all", false, "include disabled and settled sessions")
	limit := fs.Int("limit", 0, "maximum sessions to list (0 means all)")
	var states stringList
	fs.Var(&states, "state", "only show this state (repeatable)")
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return usagef("sessions takes no arguments, got %q", rest[0])
	}
	if *limit < 0 {
		return usagef("--limit must not be negative")
	}

	rt, release, err := a.openRuntime(ctx)
	if err != nil {
		return err
	}
	defer release()

	filter := storage.SessionFilter{Limit: *limit, NewestFirst: true}
	if !*all {
		enabled := true
		filter.Enabled = &enabled
	}
	for _, raw := range states {
		state := storage.State(strings.ToUpper(strings.TrimSpace(raw)))
		if !state.Valid() {
			return usagef("unknown state %q", raw)
		}
		filter.States = append(filter.States, state)
	}

	sessions, err := rt.Store.ListSessions(ctx, filter)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}
	// How many rows match the same state filter if the enabled filter is dropped.
	// The footer uses it to say what the default listing is hiding, so "one row"
	// is never presented as "one session exists".
	matching, err := rt.Store.CountSessions(ctx, storage.SessionFilter{States: filter.States})
	if err != nil {
		return fmt.Errorf("count sessions: %w", err)
	}

	schedules, err := rt.Store.LoadSchedules(ctx)
	if err != nil {
		return fmt.Errorf("load schedules: %w", err)
	}
	scheduleBySession := make(map[string]storage.Schedule, len(schedules))
	for _, schedule := range schedules {
		scheduleBySession[schedule.SessionID] = schedule
	}

	now := rt.Clock.Now()
	if a.json {
		return a.writeJSON(a.stdout, sessionReports(sessions, scheduleBySession, now))
	}
	if len(sessions) == 0 {
		if !*all && matching > 0 {
			// The exact case that looked like a bug: rows exist, none are enabled.
			fmt.Fprintf(a.stdout, "no enabled sessions match; %d match with --all\n", matching)
			return nil
		}
		fmt.Fprintln(a.stdout, "no sessions match")
		return nil
	}
	loc := a.displayLocation(rt)
	// NAME and CONTINUATION are shown only when some visible row has one, so a
	// listing of unnamed, never-resumed sessions keeps the columns it has always
	// had.
	showName, showContinuation := false, false
	for _, session := range sessions {
		if strings.TrimSpace(session.Name) != "" {
			showName = true
		}
		if session.ContinuationSessionID != "" {
			showContinuation = true
		}
	}
	headers := []string{"SESSION"}
	if showName {
		headers = append(headers, "NAME")
	}
	headers = append(headers, "STATE", "NEXT ATTEMPT")
	if showContinuation {
		headers = append(headers, "CONTINUATION")
	}
	headers = append(headers, "ENABLED", "RECOVERABLE", "PROJECT", "LAST SEEN", "NOTE")
	rows := make([][]string, 0, len(sessions))
	for _, session := range sessions {
		note := session.Reason
		if note == "" {
			note = session.LastError
		}
		next := nextAttemptCell(recovery.NextAttemptFor(session, schedulePtr(scheduleBySession, session.ID), now), now)
		row := []string{shortID(session.ID)}
		if showName {
			row = append(row, emptyDash(truncateText(session.Name, 32)))
		}
		row = append(row,
			string(session.State),
			next,
		)
		if showContinuation {
			row = append(row, emptyDash(shortID(session.ContinuationSessionID)))
		}
		row = append(row,
			yesNo(session.Enabled),
			yesNo(session.Recoverable),
			truncateText(session.ProjectPath, 40),
			formatTime(session.LastSeenAt, loc),
			truncateText(note, 40),
		)
		rows = append(rows, row)
	}
	a.table(a.stdout, headers, rows)
	fmt.Fprintln(a.stdout)
	fmt.Fprint(a.stdout, sessionsFootnote(len(sessions), matching, *all, *limit))
	return nil
}

// sessionsFootnote states what the table is showing and what the default filter
// leaves out.
//
// `status` counts every row it knows about while a bare `sessions` lists only the
// enabled ones; without this line the two outputs read as contradictory. It also
// names a truncating --limit, because a full page looks the same as a short one.
func sessionsFootnote(shown, matching int, all bool, limit int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d session(s) shown", shown)
	if all {
		b.WriteString(" (--all: disabled and settled sessions included)\n")
	} else {
		b.WriteString(": enabled only")
		if matching != shown {
			fmt.Fprintf(&b, " (of %d matching; --all includes disabled and settled)\n", matching)
		} else {
			b.WriteString(" (--all includes disabled and settled)\n")
		}
	}
	if limit > 0 && shown == limit {
		fmt.Fprintf(&b, "limit %d reached; use --limit 0 to list every match\n", limit)
	}
	return b.String()
}

// schedulePtr returns the stored schedule for a session, or nil when it has none.
// A nil schedule and a zero Schedule mean different things to NextAttemptFor.
func schedulePtr(bySession map[string]storage.Schedule, sessionID string) *storage.Schedule {
	schedule, ok := bySession[sessionID]
	if !ok {
		return nil
	}
	return &schedule
}

// nextAttemptCell renders the next attempt for a table column: the humanised
// countdown when something is scheduled, and "none (<reason>)" when nothing is,
// so a reader never has to open `inspect` to find out why a row is idle.
func nextAttemptCell(next recovery.NextAttempt, now time.Time) string {
	if next.Scheduled {
		return recovery.HumanizeUntil(next.At, now)
	}
	return truncateText("none ("+next.Reason+")", 30)
}

// sessionReport is the JSON shape of one listed session: every stored column plus
// the derived next attempt, so a script gets the machine-readable instant next to
// the row it describes.
type sessionReport struct {
	storage.Session
	// NextAttemptAt is the instant ccresu will next act, or null when nothing is
	// scheduled. The field is a pointer so "not scheduled" and "scheduled at the
	// zero time" cannot be confused.
	NextAttemptAt *time.Time `json:"next_attempt_at"`
	// NextAttemptReason explains the instant, or why there is none.
	NextAttemptReason string `json:"next_attempt_reason"`
}

// sessionReports joins sessions with their schedules into the JSON report.
func sessionReports(sessions []storage.Session, bySession map[string]storage.Schedule, now time.Time) []sessionReport {
	reports := make([]sessionReport, 0, len(sessions))
	for _, session := range sessions {
		next := recovery.NextAttemptFor(session, schedulePtr(bySession, session.ID), now)
		report := sessionReport{Session: session, NextAttemptReason: next.Reason}
		if next.Scheduled {
			at := next.At
			report.NextAttemptAt = &at
		}
		reports = append(reports, report)
	}
	return reports
}

// cmdInspect prints everything known about one session.
//
// It is the historical spelling of `ccresu sessions status <id>`; both call
// inspectSession, so the two can never drift into showing different fields.
func (a *App) cmdInspect(ctx context.Context, args []string) error {
	return a.inspectSession(ctx, "inspect", args)
}

// inspectSession prints everything ccresu knows about one session.
//
// command names the invocation in usage errors and refusals, so a typo in
// `sessions status` is reported as a `sessions status` problem rather than as an
// `inspect` one.
func (a *App) inspectSession(ctx context.Context, command string, args []string) error {
	fs := a.flags(command, command+" <session-id>")
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	id, err := a.sessionArg(fs, command)
	if err != nil {
		return err
	}

	rt, release, err := a.openRuntime(ctx)
	if err != nil {
		return err
	}
	defer release()

	session, err := rt.Store.GetSession(ctx, id)
	if err != nil {
		return fmt.Errorf("session %s: %w", id, err)
	}
	loc := a.displayLocation(rt)

	limit, limitErr := rt.Store.LatestUsageLimit(ctx, id)
	if limitErr != nil && !errors.Is(limitErr, storage.ErrNotFound) {
		return fmt.Errorf("latest usage limit for %s: %w", id, limitErr)
	}
	attempt, attemptErr := rt.Store.LatestAttempt(ctx, id)
	if attemptErr != nil && !errors.Is(attemptErr, storage.ErrNotFound) {
		return fmt.Errorf("latest attempt for %s: %w", id, attemptErr)
	}
	schedule, scheduleErr := rt.Store.GetSchedule(ctx, id)
	if scheduleErr != nil && !errors.Is(scheduleErr, storage.ErrNotFound) {
		return fmt.Errorf("schedule for %s: %w", id, scheduleErr)
	}
	transitions, err := rt.Store.ListTransitions(ctx, id, 5)
	if err != nil {
		return fmt.Errorf("transitions for %s: %w", id, err)
	}

	now := rt.Clock.Now()
	var scheduleArg *storage.Schedule
	if scheduleErr == nil {
		scheduleArg = &schedule
	}
	next := recovery.NextAttemptFor(session, scheduleArg, now)

	if a.json {
		return a.writeJSON(a.stdout, map[string]any{
			"session":      session,
			"usage_limit":  optional(limitErr, limit),
			"attempt":      optional(attemptErr, attempt),
			"schedule":     optional(scheduleErr, schedule),
			"transitions":  transitions,
			"next_attempt": nextAttemptReport(next, now),
		})
	}

	a.field(a.stdout, "session", session.ID)
	a.field(a.stdout, "state", session.State)
	a.field(a.stdout, "enabled", yesNo(session.Enabled))
	a.field(a.stdout, "recoverable", yesNo(session.Recoverable))
	if session.Reason != "" {
		a.field(a.stdout, "reason", session.Reason)
	}
	if session.LastError != "" {
		a.field(a.stdout, "last error", session.LastError)
	}
	a.field(a.stdout, "project", emptyDash(session.ProjectPath))
	a.field(a.stdout, "name", emptyDash(session.Name))
	a.field(a.stdout, "transcript", emptyDash(session.TranscriptPath))
	if session.JobID != "" {
		a.field(a.stdout, "job", session.JobID)
	}
	if session.ContinuationSessionID != "" {
		// The conversation moved: the id ccresu tracks is the one that was
		// resumed, and this is the session that is actually answering now.
		a.field(a.stdout, "continuation", session.ContinuationSessionID)
	}
	if session.ContinuationTranscriptPath != "" {
		// Detection reads this file for the next reset time, so an operator
		// looking at "why has this session not moved" needs to see it.
		a.field(a.stdout, "continuation transcript", session.ContinuationTranscriptPath)
	}
	a.field(a.stdout, "type", session.Type)
	a.field(a.stdout, "last seen", formatTime(session.LastSeenAt, loc))

	// The next attempt is its own block, above the raw records, because it is the
	// question an operator actually opens `inspect` to answer.
	fmt.Fprintln(a.stdout, "")
	fmt.Fprintln(a.stdout, "Next attempt")
	if next.Scheduled {
		a.field(a.stdout, "at", formatTime(next.At, time.UTC)+" ("+recovery.HumanizeUntil(next.At, now)+")")
	} else {
		a.field(a.stdout, "at", "none")
	}
	a.field(a.stdout, "why", next.Reason)

	if limitErr == nil {
		fmt.Fprintln(a.stdout, "")
		fmt.Fprintln(a.stdout, "Latest usage limit")
		a.field(a.stdout, "type", limit.LimitType)
		a.field(a.stdout, "detected", formatTime(limit.DetectedAt, loc))
		reset := "-"
		if !limit.ResetAt.IsZero() {
			reset = formatTime(limit.ResetAt, loc)
		}
		a.field(a.stdout, "resets", reset)
		if limit.ResetRaw != "" {
			a.field(a.stdout, "raw", limit.ResetRaw)
		}
		a.field(a.stdout, "confidence", fmt.Sprintf("%.2f", limit.Confidence))
	}

	if attemptErr == nil {
		fmt.Fprintln(a.stdout, "")
		fmt.Fprintln(a.stdout, "Latest attempt")
		a.field(a.stdout, "number", attempt.AttemptNumber)
		a.field(a.stdout, "state", attempt.State)
		a.field(a.stdout, "started", formatTime(attempt.StartedAt, loc))
		a.field(a.stdout, "finished", formatTime(attempt.FinishedAt, loc))
		if attempt.Command != "" {
			a.field(a.stdout, "command", attempt.Command)
		}
		if attempt.Error != "" {
			a.field(a.stdout, "error", attempt.Error)
		}
	}

	if scheduleErr == nil {
		fmt.Fprintln(a.stdout, "")
		fmt.Fprintln(a.stdout, "Schedule")
		a.field(a.stdout, "due", formatTime(schedule.DueAt(), loc))
		a.field(a.stdout, "unknown reset", yesNo(schedule.Unknown))
		a.field(a.stdout, "polls", schedule.Attempts)
		if schedule.ResetRaw != "" {
			a.field(a.stdout, "phrase", schedule.ResetRaw)
		}
	}

	if len(transitions) > 0 {
		fmt.Fprintln(a.stdout, "")
		fmt.Fprintln(a.stdout, "Recent transitions")
		rows := make([][]string, 0, len(transitions))
		for _, transition := range transitions {
			rows = append(rows, []string{
				formatTime(transition.OccurredAt, loc),
				string(transition.FromState) + " -> " + string(transition.ToState),
				truncateText(transition.Reason, 48),
			})
		}
		a.table(a.stdout, []string{"WHEN", "TRANSITION", "REASON"}, rows)
	}
	return nil
}

// The subcommands of `ccresu sessions`.
const (
	subcommandStatus = "status"
	subcommandDelete = "delete"
)

// sessionsUsage is the documented form of the sessions command.
const sessionsUsage = "sessions [--all] [--state STATE]... [--limit N] | sessions status <session-id> | sessions delete <session-id>"

// sessionsSubcommand finds the subcommand in a `ccresu sessions` argument list.
//
// The scan skips flags, and the values of the flags that take one, so all of
//
//	ccresu sessions delete <id>
//	ccresu sessions --json delete <id>
//	ccresu sessions --limit 5 status <id>
//	ccresu --json sessions delete <id>
//
// reach the same handler. The first positional word is the only candidate: a
// session id that happens to be spelled "status" is still accepted by
// `sessions delete status`, because by then the subcommand has been found.
func sessionsSubcommand(args []string) (string, []string, bool) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			// Everything after -- is positional, and the first of those is the
			// only thing that can be a subcommand.
			if i+1 < len(args) && isSessionsSubcommand(args[i+1]) {
				return args[i+1], withoutWord(args, i+1), true
			}
			return "", nil, false
		case strings.HasPrefix(arg, "-"):
			if sessionsFlagTakesValue(arg) {
				i++
			}
			continue
		case isSessionsSubcommand(arg):
			return arg, withoutWord(args, i), true
		default:
			return "", nil, false
		}
	}
	return "", nil, false
}

// withoutWord returns args with the element at index removed.
//
// The flags written before the subcommand are kept, because they are still the
// caller's instructions: `ccresu sessions --json status <id>` must produce JSON,
// and dropping everything before the word is how that silently stops working.
func withoutWord(args []string, index int) []string {
	rest := make([]string, 0, len(args)-1)
	rest = append(rest, args[:index]...)
	return append(rest, args[index+1:]...)
}

// isSessionsSubcommand reports whether word names a `ccresu sessions` subcommand.
func isSessionsSubcommand(word string) bool {
	return word == subcommandStatus || word == subcommandDelete
}

// sessionsFlagTakesValue reports whether a flag argument consumes the next
// argument. Flags written as --name=value carry their own value and must not be
// counted here.
func sessionsFlagTakesValue(arg string) bool {
	if strings.Contains(arg, "=") {
		return false
	}
	switch strings.TrimLeft(arg, "-") {
	case "limit", "state", "config", "c":
		return true
	default:
		return false
	}
}

// cmdSessionsDelete removes one session from ccresu.
//
// It deletes ccresu's records, never the conversation: nothing under ~/.claude is
// read, written or removed. A tombstone keeps the session gone when discovery
// would otherwise upsert it straight back, and `ccresu enable <id>` removes the
// tombstone when the operator changes their mind.
func (a *App) cmdSessionsDelete(ctx context.Context, args []string) error {
	const command = "sessions delete"
	fs := a.flags(command, command+" <session-id>")
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	id, err := a.sessionArg(fs, command)
	if err != nil {
		return err
	}

	rt, release, err := a.openRuntime(ctx)
	if err != nil {
		return err
	}
	defer release()

	deleted, counts, err := rt.Store.ForgetSession(ctx, id)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			// Already forgotten is a success: the operator asked for the session
			// to be gone, and it is. A missing session that was never deleted is
			// still a not-found, so a typo cannot look like a clean delete.
			if tombstone, tombErr := rt.Store.GetDeletedSession(ctx, id); tombErr == nil {
				return a.reportAlreadyDeleted(id, tombstone)
			}
		}
		return fmt.Errorf("%s %s: %w", command, id, err)
	}

	if a.json {
		return a.writeJSON(a.stdout, map[string]any{
			"session_id":      id,
			"deleted":         true,
			"state":           string(deleted.State),
			"project":         deleted.ProjectPath,
			"transcript":      deleted.TranscriptPath,
			"deleted_at":      deleted.DeletedAt.UTC().Format(time.RFC3339Nano),
			"removed":         counts,
			"restore":         "ccresu enable " + id,
			"transcript_kept": true,
		})
	}

	a.field(a.stdout, "session", id)
	a.field(a.stdout, "state at deletion", deleted.State)
	a.field(a.stdout, "project", emptyDash(deleted.ProjectPath))
	a.field(a.stdout, "transcript", emptyDash(deleted.TranscriptPath))
	a.field(a.stdout, "attempts removed", counts.Attempts)
	a.field(a.stdout, "transitions removed", counts.Transitions)
	a.field(a.stdout, "usage limits removed", counts.UsageLimits)
	a.field(a.stdout, "notifications removed", counts.Notifications)
	a.field(a.stdout, "pending wait", yesNo(counts.Scheduled))
	a.field(a.stdout, "conversation", "kept: nothing under ~/.claude is touched")
	fmt.Fprintf(a.stdout, "session %s: deleted; discovery will skip it (undo with 'ccresu enable %s')\n", id, id)
	return nil
}

// reportAlreadyDeleted is the idempotent success: the session is already gone,
// so the operator gets the same answer as a fresh delete rather than an error
// they cannot act on.
func (a *App) reportAlreadyDeleted(id string, tombstone storage.DeletedSession) error {
	if a.json {
		return a.writeJSON(a.stdout, map[string]any{
			"session_id": id,
			"deleted":    true,
			"already":    true,
			"state":      string(tombstone.State),
			"deleted_at": tombstone.DeletedAt.UTC().Format(time.RFC3339Nano),
			"restore":    "ccresu enable " + id,
		})
	}
	fmt.Fprintf(a.stdout, "session %s: already deleted at %s; nothing to do (undo with 'ccresu enable %s')\n",
		id, formatTime(tombstone.DeletedAt, time.UTC), id)
	return nil
}

// nextAttemptReport is the JSON form of a resolved next attempt. It carries both
// the machine-readable instant and the humanised countdown, so a script needs no
// duration formatting of its own and a log line needs no timestamp arithmetic.
func nextAttemptReport(next recovery.NextAttempt, now time.Time) map[string]any {
	report := map[string]any{
		"scheduled": next.Scheduled,
		"reason":    next.Reason,
		"human":     recovery.HumanizeUntil(next.At, now),
	}
	if next.Scheduled {
		report["at"] = next.At.UTC().Format(time.RFC3339Nano)
	} else {
		report["at"] = nil
	}
	return report
}

// optional returns err when the row is missing, and the value otherwise, so a
// JSON report can distinguish "no attempt yet" from "attempt zero".
func optional[T any](err error, value T) any {
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	return value
}

// emptyDash renders an empty string as "-".
func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// cmdHistory prints the attempts and state transitions of one session.
func (a *App) cmdHistory(ctx context.Context, args []string) error {
	fs := a.flags("history", "history <session-id> [--limit N]")
	limit := fs.Int("limit", 50, "maximum transitions to show")
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	id, err := a.sessionArg(fs, "history")
	if err != nil {
		return err
	}
	if *limit < 0 {
		return usagef("--limit must not be negative")
	}

	rt, release, err := a.openRuntime(ctx)
	if err != nil {
		return err
	}
	defer release()

	// Read the session first so `history` reports "not found" for an unknown id
	// instead of an empty, misleading page.
	session, err := rt.Store.GetSession(ctx, id)
	if err != nil {
		return fmt.Errorf("session %s: %w", id, err)
	}
	attempts, err := rt.Store.ListAttempts(ctx, id, 0)
	if err != nil {
		return fmt.Errorf("attempts for %s: %w", id, err)
	}
	transitions, err := rt.Store.ListTransitions(ctx, id, *limit)
	if err != nil {
		return fmt.Errorf("transitions for %s: %w", id, err)
	}
	if a.json {
		return a.writeJSON(a.stdout, map[string]any{
			"session_id":  session.ID,
			"state":       session.State,
			"attempts":    attempts,
			"transitions": transitions,
		})
	}

	loc := a.displayLocation(rt)
	a.field(a.stdout, "session", session.ID)
	a.field(a.stdout, "state", session.State)
	fmt.Fprintln(a.stdout, "")

	if len(attempts) == 0 {
		fmt.Fprintln(a.stdout, "No recovery attempts recorded.")
	} else {
		rows := make([][]string, 0, len(attempts))
		for _, attempt := range attempts {
			result := "in progress"
			switch {
			case attempt.Success != nil && *attempt.Success:
				result = "succeeded"
			case attempt.Success != nil:
				result = "failed"
			}
			rows = append(rows, []string{
				fmt.Sprint(attempt.AttemptNumber),
				string(attempt.State),
				formatTime(attempt.StartedAt, loc),
				formatTime(attempt.FinishedAt, loc),
				result,
				truncateText(attempt.Error, 40),
			})
		}
		a.table(a.stdout, []string{"#", "STATE", "STARTED", "FINISHED", "RESULT", "ERROR"}, rows)
		fmt.Fprintln(a.stdout, "")
	}

	if len(transitions) == 0 {
		fmt.Fprintln(a.stdout, "No state transitions recorded.")
		return nil
	}
	rows := make([][]string, 0, len(transitions))
	for _, transition := range transitions {
		rows = append(rows, []string{
			formatTime(transition.OccurredAt, loc),
			string(transition.FromState) + " -> " + string(transition.ToState),
			truncateText(transition.Reason, 60),
		})
	}
	a.table(a.stdout, []string{"WHEN", "TRANSITION", "REASON"}, rows)
	return nil
}

// cmdRecover resumes one session now.
func (a *App) cmdRecover(ctx context.Context, args []string) error {
	fs := a.flags("recover", "recover <session-id> [--message TEXT] [--force]")
	message := fs.String("message", "", "continuation message to send instead of the configured one")
	force := fs.Bool("force", false, "try even though the attempt budget is spent")
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	id, err := a.sessionArg(fs, "recover")
	if err != nil {
		return err
	}

	rt, release, err := a.openRuntime(ctx)
	if err != nil {
		return err
	}
	defer release()

	manager, err := a.requireRecovery(rt)
	if err != nil {
		return err
	}
	// Check existence here so an unknown id exits 3 ("not found") rather than 4
	// ("exists but not eligible"); recovery reports both through ErrNotEligible.
	if _, err := rt.Store.GetSession(ctx, id); err != nil {
		return fmt.Errorf("recover %s: %w", id, err)
	}
	outcome, err := manager.RecoverRequest(ctx, recovery.Request{
		SessionID: id,
		Message:   *message,
		Force:     *force,
	})
	if err != nil {
		return fmt.Errorf("recover %s: %w", id, err)
	}
	if err := a.reportOutcome(rt, outcome); err != nil {
		return err
	}
	if !outcome.Resumed {
		return withCode(ExitNotEligible, fmt.Errorf("session %s was not resumed (state %s)", id, outcome.State))
	}
	return nil
}

// cmdRetry retries a session whose attempt budget is spent.
func (a *App) cmdRetry(ctx context.Context, args []string) error {
	fs := a.flags("retry", "retry <session-id>")
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	id, err := a.sessionArg(fs, "retry")
	if err != nil {
		return err
	}

	rt, release, err := a.openRuntime(ctx)
	if err != nil {
		return err
	}
	defer release()

	manager, err := a.requireRecovery(rt)
	if err != nil {
		return err
	}
	if _, err := rt.Store.GetSession(ctx, id); err != nil {
		return fmt.Errorf("retry %s: %w", id, err)
	}
	outcome, err := manager.Retry(ctx, id)
	if err != nil {
		return fmt.Errorf("retry %s: %w", id, err)
	}
	if err := a.reportOutcome(rt, outcome); err != nil {
		return err
	}
	if !outcome.Resumed {
		return withCode(ExitNotEligible, fmt.Errorf("session %s was not resumed (state %s)", id, outcome.State))
	}
	return nil
}

// reportOutcome prints one recovery result.
func (a *App) reportOutcome(rt *Runtime, outcome recovery.Outcome) error {
	if a.json {
		return a.writeJSON(a.stdout, outcome)
	}
	loc := a.displayLocation(rt)
	a.field(a.stdout, "session", outcome.SessionID)
	a.field(a.stdout, "state", outcome.State)
	a.field(a.stdout, "resumed", yesNo(outcome.Resumed))
	if outcome.Attempt.ID != 0 {
		a.field(a.stdout, "attempt", outcome.Attempt.AttemptNumber)
		a.field(a.stdout, "verification", describeVerification(outcome))
	}
	if outcome.State == storage.StateRetryWait {
		a.field(a.stdout, "next attempt in", formatDuration(outcome.Delay))
	}
	if outcome.Attempt.Error != "" {
		a.field(a.stdout, "error", outcome.Attempt.Error)
	}
	a.field(a.stdout, "when", formatTime(rt.Clock.Now().UTC(), loc))
	return nil
}

// describeVerification renders the proof that the original conversation
// continued, so a failed resume is never mistaken for a successful one.
//
// The wording names what is actually proven: the job reported the session it
// continues, and that continuation's transcript grew. It used to say "session id
// preserved", which was the print-mode proof and is no longer even evaluable --
// a background job continues the conversation under its own session id.
func describeVerification(outcome recovery.Outcome) string {
	verification := outcome.Verification
	if !verification.Proven {
		if missing := verification.Missing(); len(missing) > 0 {
			return "not proven: " + strings.Join(missing, "; ")
		}
		return "not proven"
	}
	return "proven: the job named its continuation and that transcript grew"
}

// requireRecovery returns the recovery manager or a clear error when automatic
// recovery is switched off.
func (a *App) requireRecovery(rt *Runtime) (*recovery.Manager, error) {
	if rt.Recovery == nil {
		return nil, withCode(ExitNotEligible, fmt.Errorf("%w: recovery is not configured", recovery.ErrDisabled))
	}
	return rt.Recovery, nil
}

// cmdEnable allows recovery of one session.
func (a *App) cmdEnable(ctx context.Context, args []string) error {
	return a.setEnabled(ctx, args, true)
}

// cmdDisable stops recovery of one session.
func (a *App) cmdDisable(ctx context.Context, args []string) error {
	return a.setEnabled(ctx, args, false)
}

// setEnabled implements enable and disable.
func (a *App) setEnabled(ctx context.Context, args []string, enabled bool) error {
	command := "enable"
	if !enabled {
		command = "disable"
	}
	fs := a.flags(command, command+" <session-id>")
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	id, err := a.sessionArg(fs, command)
	if err != nil {
		return err
	}

	rt, release, err := a.openRuntime(ctx)
	if err != nil {
		return err
	}
	defer release()

	// `enable` is also the undo for `sessions delete`: a deleted session has no
	// row left to enable, so the tombstone is removed and the row is rebuilt
	// before enable runs. Without this, the only documented escape from a delete
	// would be a command that answers "not found".
	var restored bool
	if enabled && !existsSession(ctx, rt, id) {
		if _, restoreErr := rt.Store.RestoreSession(ctx, id); restoreErr == nil {
			restored = true
		} else if !errors.Is(restoreErr, storage.ErrNotFound) {
			return fmt.Errorf("%s %s: %w", command, id, restoreErr)
		}
	}

	if err := rt.Store.SetSessionEnabled(ctx, id, enabled); err != nil {
		return fmt.Errorf("%s %s: %w", command, id, err)
	}
	if a.json {
		return a.writeJSON(a.stdout, map[string]any{
			"session_id": id,
			"enabled":    enabled,
			"restored":   restored,
		})
	}
	fmt.Fprintf(a.stdout, "session %s: enabled=%s\n", id, yesNo(enabled))
	if restored {
		fmt.Fprintf(a.stdout, "session %s: restored after a delete; discovery will refresh it\n", id)
	}
	return nil
}

// existsSession reports whether the store still knows the session, without
// turning "missing" into an error the caller has to interpret.
func existsSession(ctx context.Context, rt *Runtime, id string) bool {
	_, err := rt.Store.GetSession(ctx, id)
	return err == nil
}

// stringList is a repeatable string flag.
type stringList []string

// String implements flag.Value.
func (l *stringList) String() string { return strings.Join(*l, ",") }

// Set implements flag.Value.
func (l *stringList) Set(value string) error {
	*l = append(*l, value)
	return nil
}

// detectedSummary renders a detector verdict for `ccresu inspect --detect`.
func detectedSummary(event detector.UsageLimitEvent) string {
	if !event.IsUsageLimit() {
		return "no usage limit (" + string(event.Classification) + ")"
	}
	return fmt.Sprintf("%s limit at confidence %.2f", event.Limit, event.Confidence)
}
