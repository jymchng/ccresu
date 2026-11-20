package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strconv"
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
		case subcommandSearchLimit:
			return a.cmdSearchLimit(ctx, rest)
		}
	}

	fs := a.flags("sessions", sessionsUsage)
	all := fs.Bool("all", false, "include disabled and settled sessions")
	limit := fs.Int("limit", 0, "maximum sessions to list (0 means all)")
	forceTUI := fs.Bool("tui", false, "open the interactive browser even when stdout is not a terminal")
	noTUI := fs.Bool("no-tui", false, "print the table even when stdout is a terminal")
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
	// Decided before the runtime is opened, so a contradictory pair of flags
	// fails as a usage error instead of after touching the database.
	useTUI, err := a.resolveSessionsUI(*forceTUI, *noTUI)
	if err != nil {
		return err
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

	if useTUI {
		// The browser re-reads the store on every action, so it gets the filter
		// rather than this already-loaded page.
		return a.runSessionBrowser(ctx, rt, browserOptions{All: *all, States: filter.States, Limit: *limit})
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
	rows := make([][]string, 0, len(sessions))
	for _, session := range sessions {
		note := session.Reason
		if note == "" {
			note = session.LastError
		}
		next := nextAttemptCell(recovery.NextAttemptFor(session, schedulePtr(scheduleBySession, session.ID), now), now)
		rows = append(rows, []string{
			shortID(session.ID),
			string(session.State),
			next,
			yesNo(session.Enabled),
			yesNo(session.Recoverable),
			truncateText(session.ProjectPath, 40),
			// PRD 6 R12: where the session actually runs is its own column with
			// its own label, because a background session's worktree and its
			// launch directory are different facts and an operator should see
			// the difference rather than infer it.
			runsInCell(session),
			formatTime(session.LastSeenAt, loc),
			truncateText(note, 40),
		})
	}
	a.table(a.stdout, []string{"SESSION", "STATE", "NEXT ATTEMPT", "ENABLED", "RECOVERABLE", "PROJECT", "RUNS IN", "LAST SEEN", "NOTE"}, rows)
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
	return a.writeSessionDetail(ctx, rt, id, a.stdout)
}

// sessionDetail is everything the status page says about one session.
//
// It is a value rather than a set of locals so the page can be rendered in two
// halves: the browser paints the store's half from the keystroke and fills the
// supervisor's in afterwards, while `sessions status` renders the whole thing in
// one pass and may wait for the CLI.
type sessionDetail struct {
	Session     storage.Session
	Limit       storage.UsageLimit
	LimitErr    error
	Attempt     storage.RecoveryAttempt
	AttemptErr  error
	Schedule    storage.Schedule
	ScheduleErr error
	Transitions []storage.Transition
	Spend       storage.SpendRecord
	Limits      recovery.SpendLimits
	Next        recovery.NextAttempt
	Now         time.Time
	Loc         *time.Location
}

// loadSessionDetail reads one session and everything the status page shows about
// it.
//
// It reads the store and nothing else: the supervisor is asked by the caller, so
// the browser can paint the page before a slow CLI answers, and a batch command
// can still wait for the answer. Every failure that is not "there is no such row
// yet" is returned, because a page that silently omits a section is a page that
// lies about the session.
func (a *App) loadSessionDetail(ctx context.Context, rt *Runtime, id string) (sessionDetail, error) {
	var detail sessionDetail
	session, err := rt.Store.GetSession(ctx, id)
	if err != nil {
		return sessionDetail{}, fmt.Errorf("session %s: %w", id, err)
	}
	detail.Session = session
	detail.Loc = a.displayLocation(rt)

	limit, limitErr := rt.Store.LatestUsageLimit(ctx, id)
	if limitErr != nil && !errors.Is(limitErr, storage.ErrNotFound) {
		return sessionDetail{}, fmt.Errorf("latest usage limit for %s: %w", id, limitErr)
	}
	detail.Limit, detail.LimitErr = limit, limitErr
	attempt, attemptErr := rt.Store.LatestAttempt(ctx, id)
	if attemptErr != nil && !errors.Is(attemptErr, storage.ErrNotFound) {
		return sessionDetail{}, fmt.Errorf("latest attempt for %s: %w", id, attemptErr)
	}
	detail.Attempt, detail.AttemptErr = attempt, attemptErr
	schedule, scheduleErr := rt.Store.GetSchedule(ctx, id)
	if scheduleErr != nil && !errors.Is(scheduleErr, storage.ErrNotFound) {
		return sessionDetail{}, fmt.Errorf("schedule for %s: %w", id, scheduleErr)
	}
	detail.Schedule, detail.ScheduleErr = schedule, scheduleErr
	transitions, err := rt.Store.ListTransitions(ctx, id, 5)
	if err != nil {
		return sessionDetail{}, fmt.Errorf("transitions for %s: %w", id, err)
	}
	detail.Transitions = transitions
	// PRD 5: the same figure the spend caps are compared against, and the caps
	// themselves, so `inspect` answers "why is this session parked" without a
	// second opinion computed somewhere else.
	spend, err := rt.Store.SessionSpend(ctx, id)
	if err != nil {
		return sessionDetail{}, fmt.Errorf("spend for %s: %w", id, err)
	}
	detail.Spend = spend
	detail.Limits = spendLimits(rt)

	detail.Now = rt.Clock.Now()
	var scheduleArg *storage.Schedule
	if scheduleErr == nil {
		scheduleArg = &schedule
	}
	detail.Next = recovery.NextAttemptFor(session, scheduleArg, detail.Now)
	return detail, nil
}

// writeSessionDetail prints everything ccresu knows about one session to w.
//
// It is separated from inspectSession so the interactive sessions browser can
// render the very same page: pressing Enter on a row shows this output rather
// than a second, thinner view that could drift from `sessions status`. The browser
// renders the same two halves through writeDetailFacts and writeDetailTail, with
// the supervisor block filled in between them when the CLI answers.
func (a *App) writeSessionDetail(ctx context.Context, rt *Runtime, id string, w io.Writer) error {
	detail, err := a.loadSessionDetail(ctx, rt, id)
	if err != nil {
		return err
	}
	if a.json {
		return a.writeJSON(w, map[string]any{
			"session":      detail.Session,
			"usage_limit":  optional(detail.LimitErr, detail.Limit),
			"attempt":      optional(detail.AttemptErr, detail.Attempt),
			"schedule":     optional(detail.ScheduleErr, detail.Schedule),
			"transitions":  detail.Transitions,
			"next_attempt": nextAttemptReport(detail.Next, detail.Now),
			"spend":        spendReport(detail.Spend, detail.Limits),
			"supervisor":   a.supervisorReport(ctx, rt, detail.Attempt, detail.AttemptErr),
		})
	}

	a.writeDetailFacts(w, detail)
	// PRD 3 R10: the supervisor is the only thing that knows what the session is
	// doing and what it last printed, so `inspect` asks it on demand rather than
	// ccresu tailing files behind its back. Nothing here is polled.
	// A provider with no supervisor surface leaves the block out of the text
	// rendering entirely: "unavailable" would describe the provider rather than the
	// session. The JSON rendering keeps it, where a script can branch on it.
	if reader, ok := rt.Provider.(SupervisorReader); ok {
		a.writeSupervisorDetail(w, a.readSupervisor(ctx, reader, detail.Attempt, detail.AttemptErr))
	}
	a.writeDetailTail(w, detail)
	return nil
}

// writeDetailFacts prints the store's half of the status page: what ccresu knows
// about the session, what it will do next, and what it recorded about the last
// attempt.
//
// It never asks the supervisor, so the browser can paint it from a keystroke.
func (a *App) writeDetailFacts(w io.Writer, detail sessionDetail) {
	a.field(w, "session", detail.Session.ID)
	a.field(w, "state", detail.Session.State)
	a.field(w, "enabled", yesNo(detail.Session.Enabled))
	a.field(w, "recoverable", yesNo(detail.Session.Recoverable))
	if detail.Session.Reason != "" {
		a.field(w, "reason", detail.Session.Reason)
	}
	if detail.Session.LastError != "" {
		a.field(w, "last error", detail.Session.LastError)
	}
	a.field(w, "project", emptyDash(detail.Session.ProjectPath))
	// PRD 6 R12: the second directory, labelled distinctly. "same as project"
	// says the session never moved; a dash would leave the reader guessing
	// between "not observed" and "identical".
	a.field(w, "runs in", runsInDetail(detail.Session))
	a.field(w, "transcript", emptyDash(detail.Session.TranscriptPath))
	a.field(w, "type", detail.Session.Type)
	a.field(w, "last seen", formatTime(detail.Session.LastSeenAt, detail.Loc))
	// PRD 5: what the session is recorded to have cost, next to the caps it is
	// measured against. It is one line because this page is also the sessions
	// browser's detail screen, and every extra line pushes a section off the
	// first view. A cap that is off says "off" rather than "$0", because "$0"
	// reads as "any spend is over the cap".
	a.field(w, "spend", spendLine(detail.Spend, detail.Limits))

	// The next attempt is its own block, above the raw records, because it is the
	// question an operator actually opens `inspect` to answer.
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Next attempt")
	if detail.Next.Scheduled {
		a.field(w, "at", formatTime(detail.Next.At, time.UTC)+" ("+recovery.HumanizeUntil(detail.Next.At, detail.Now)+")")
	} else {
		a.field(w, "at", "none")
	}
	a.field(w, "why", detail.Next.Reason)

	if detail.LimitErr == nil {
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Latest usage limit")
		a.field(w, "type", detail.Limit.LimitType)
		a.field(w, "detected", formatTime(detail.Limit.DetectedAt, detail.Loc))
		reset := "-"
		if !detail.Limit.ResetAt.IsZero() {
			reset = formatTime(detail.Limit.ResetAt, detail.Loc)
		}
		a.field(w, "resets", reset)
		if detail.Limit.ResetRaw != "" {
			a.field(w, "raw", detail.Limit.ResetRaw)
		}
		a.field(w, "confidence", fmt.Sprintf("%.2f", detail.Limit.Confidence))
	}

	if detail.AttemptErr == nil {
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Latest attempt")
		a.field(w, "number", detail.Attempt.AttemptNumber)
		a.field(w, "state", detail.Attempt.State)
		// PRD 5 R4/R10: what this attempt cost, or "unknown" — never "$0" for a
		// figure the CLI did not report.
		a.field(w, "cost", formatCost(detail.Attempt.CostUSD))
		a.field(w, "started", formatTime(detail.Attempt.StartedAt, detail.Loc))
		a.field(w, "finished", formatTime(detail.Attempt.FinishedAt, detail.Loc))
		if detail.Attempt.Command != "" {
			a.field(w, "command", detail.Attempt.Command)
		}
		if detail.Attempt.Error != "" {
			a.field(w, "error", detail.Attempt.Error)
		}

		// PRD 2 / R13: what the supervisor published about the session when the
		// attempt was made, verbatim. This is the evidence the decision rested
		// on, so `inspect` must be able to say what was seen rather than what
		// was inferred from a file size.
		//
		// "not recorded" is a distinct answer, not an empty one. An attempt
		// recorded before the observation columns existed genuinely has no
		// observation, and rendering that as a blank state would present
		// "we did not look" as "nothing was there" (R8).
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Observed")
		observation := detail.Attempt.Observation
		if !observation.Recorded() {
			a.field(w, "state", "not recorded")
		} else {
			a.field(w, "state", emptyDash(observation.State))
			if observation.Status != "" {
				a.field(w, "status", observation.Status)
			}
			if observation.WaitingFor != "" {
				a.field(w, "waiting for", observation.WaitingFor)
			}
			a.field(w, "at", formatTime(observation.ObservedAt, detail.Loc))
			listed := "not recorded"
			if observation.Listed != nil {
				listed = yesNo(*observation.Listed)
			}
			a.field(w, "in listing", listed)
		}
	}
}

// writeDetailTail prints the rest of the status page: the stored wait and the
// recent state transitions.
func (a *App) writeDetailTail(w io.Writer, detail sessionDetail) {
	if detail.ScheduleErr == nil {
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Schedule")
		a.field(w, "due", formatTime(detail.Schedule.DueAt(), detail.Loc))
		a.field(w, "unknown reset", yesNo(detail.Schedule.Unknown))
		a.field(w, "polls", detail.Schedule.Attempts)
		if detail.Schedule.ResetRaw != "" {
			a.field(w, "phrase", detail.Schedule.ResetRaw)
		}
	}

	if len(detail.Transitions) > 0 {
		fmt.Fprintln(w, "")
		fmt.Fprintln(w, "Recent transitions")
		rows := make([][]string, 0, len(detail.Transitions))
		for _, transition := range detail.Transitions {
			rows = append(rows, []string{
				formatTime(transition.OccurredAt, detail.Loc),
				string(transition.FromState) + " -> " + string(transition.ToState),
				truncateText(transition.Reason, 48),
			})
		}
		a.table(w, []string{"WHEN", "TRANSITION", "REASON"}, rows)
	}
}

// The subcommands of `ccresu sessions`.
const (
	subcommandStatus = "status"
	subcommandDelete = "delete"
	// subcommandSearchLimit is the `sessions`-namespaced spelling of the
	// top-level `ccresu search-limit`. It is the same handler: the command is
	// about one session, so an operator who is already in `sessions` should not
	// have to leave it to find the verb.
	subcommandSearchLimit = "search-limit"
)

// sessionsUsage is the documented form of the sessions command.
const sessionsUsage = "sessions [--all] [--state STATE]... [--limit N] [--tui|--no-tui] | sessions status <session-id> | sessions delete <session-id> | sessions search-limit <session-id>"

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
	return word == subcommandStatus || word == subcommandDelete || word == subcommandSearchLimit
}

// sessionsFlagTakesValue reports whether a flag argument consumes the next
// argument. Flags written as --name=value carry their own value and must not be
// counted here.
func sessionsFlagTakesValue(arg string) bool {
	if strings.Contains(arg, "=") {
		return false
	}
	switch strings.TrimLeft(arg, "-") {
	case "limit", "state", "config", "c", "tail":
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

// spendLimits reports the caps the running manager enforces, so a renderer can
// show them next to the spend they are compared against.
//
// A runtime with no manager (a test, or a caller that built only a store) has no
// caps, which is the same answer as a configuration that sets none.
func spendLimits(rt *Runtime) recovery.SpendLimits {
	if rt == nil || rt.Recovery == nil {
		return recovery.SpendLimits{}
	}
	return rt.Recovery.Limits()
}

// spendReport is the JSON form of what a session is recorded to have cost and the
// caps it is measured against. `null` for a cap means "off", which is a different
// fact from a cap of zero that would bind everything.
func spendReport(spend storage.SpendRecord, limits recovery.SpendLimits) map[string]any {
	report := map[string]any{
		"known_usd": spend.KnownUSD,
		"known":     spend.Known,
		"unknown":   spend.Unknown,
	}
	if limits.SessionCapped() {
		report["session_cap_usd"] = limits.PerSession
	} else {
		report["session_cap_usd"] = nil
	}
	if limits.AttemptCapped() {
		report["attempt_cap_usd"] = limits.PerAttempt
	} else {
		report["attempt_cap_usd"] = nil
	}
	return report
}

// formatUSD renders a recorded cost the way an operator reads one: with a dollar
// sign and no rounded-away cents.
func formatUSD(v float64) string {
	return "$" + strconv.FormatFloat(v, 'f', -1, 64)
}

// formatCost renders an attempt's recorded cost, or "unknown" when the CLI
// reported none (PRD 5 R10). A nil cost must never read as "$0".
func formatCost(cost *float64) string {
	if cost == nil {
		return "unknown"
	}
	return formatUSD(*cost)
}

// capOrOff renders a configured cap, or the word that says none is configured.
func capOrOff(limit float64) string {
	if limit <= 0 {
		return "off"
	}
	return formatUSD(limit)
}

// spendLine renders the spend summary as one line, because `inspect` is also the
// sessions browser's detail page and every extra line pushes a section off the
// first screen.
func spendLine(spend storage.SpendRecord, limits recovery.SpendLimits) string {
	attempts := fmt.Sprintf("%d attempt(s)", spend.Known+spend.Unknown)
	if spend.Unknown > 0 {
		attempts = fmt.Sprintf("%d attempt(s), %d with no reported cost", spend.Known+spend.Unknown, spend.Unknown)
	}
	return fmt.Sprintf("%s recorded across %s (session cap %s, attempt cap %s)",
		formatUSD(spend.KnownUSD), attempts,
		capOrOff(limits.PerSession), capOrOff(limits.PerAttempt))
}

// runsInCell renders where a session runs for the `sessions` table: a dash when
// it runs where it started, because repeating one path in two columns is noise
// rather than information (PRD 6 R12).
func runsInCell(session storage.Session) string {
	if strings.TrimSpace(session.RunsInPath) == "" || session.RunsInPath == session.ProjectPath {
		return "-"
	}
	return truncateText(session.RunsInPath, 40)
}

// runsInDetail renders the run directory for `inspect`: the path when it differs,
// the word that says why it is not repeated when it does not, and "unknown" when
// no observation has recorded one yet.
func runsInDetail(session storage.Session) string {
	runsIn := strings.TrimSpace(session.RunsInPath)
	switch {
	case runsIn == "":
		return "unknown (no observation has recorded one; the project directory is used)"
	case runsIn == strings.TrimSpace(session.ProjectPath):
		return runsIn + " (same as the project)"
	default:
		return runsIn
	}
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
		spend, err := rt.Store.SessionSpend(ctx, id)
		if err != nil {
			return fmt.Errorf("spend for %s: %w", id, err)
		}
		return a.writeJSON(a.stdout, map[string]any{
			"session_id":  session.ID,
			"state":       session.State,
			"attempts":    attempts,
			"transitions": transitions,
			"spend":       spendReport(spend, spendLimits(rt)),
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
				formatCost(attempt.CostUSD),
				result,
				truncateText(attempt.Error, 40),
			})
		}
		a.table(a.stdout, []string{"#", "STATE", "STARTED", "FINISHED", "COST", "RESULT", "ERROR"}, rows)
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
		// PRD 5 R11: this is a human asking, so the spend caps do not bind it.
		// The attempt's cost is still recorded, and the override is logged.
		Manual: true,
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

// cmdSearchLimit looks for a usage limit in a session the operator has
// established is idle.
//
// The command exists because RESUMED is Settled: a resumed session is never
// re-observed, so a limit that lands in its transcript after the resume leaves it
// stuck in RESUMED for ever while the daemon runs happily next to it. Not looking
// is deliberate — it is what stops a finished conversation being resumed twice —
// so the read cannot be automatic. It is an operator act instead, and the
// operator is the authority on whether the session has gone idle.
//
// Nothing is resumed and nothing is spent: the command reads a transcript, and
// when it finds a banner it parks the session on the wait the banner names.
func (a *App) cmdSearchLimit(ctx context.Context, args []string) error {
	const command = "search-limit"
	fs := a.flags(command, command+" <session-id> [--tail N]")
	tail := fs.Int("tail", defaultTranscriptTail, "how many transcript lines to search")
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	id, err := a.sessionArg(fs, command)
	if err != nil {
		return err
	}
	if *tail <= 0 {
		return usagef("--tail must be positive")
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
	// Read the session here so an unknown id exits 3 ("not found") rather than 4
	// ("exists but not eligible"); the search reports the latter through
	// ErrNotEligible.
	session, err := rt.Store.GetSession(ctx, id)
	if err != nil {
		return fmt.Errorf("%s %s: %w", command, id, err)
	}

	result, err := manager.SearchLimit(ctx, id, transcriptLimitScan(rt, session, *tail))
	if err != nil {
		return fmt.Errorf("%s %s: %w", command, id, err)
	}
	return a.reportLimitSearch(rt, result)
}

// reportLimitSearch prints what one search found.
//
// A search that found nothing is a success, not a failure: the operator asked a
// question and got the answer. The exit code stays 0 and the message says plainly
// that the session was left alone, so a script reading the exit code is not told
// that something broke when nothing did.
func (a *App) reportLimitSearch(rt *Runtime, result recovery.LimitSearch) error {
	if a.json {
		return a.writeJSON(a.stdout, map[string]any{
			"session_id":     result.SessionID,
			"found":          result.Found,
			"state":          string(result.State),
			"banner":         result.Banner,
			"reason":         result.Reason,
			"known_reset":    result.Wait.Known,
			"reset_at":       formatJSONTime(result.Wait.At),
			"wait":           formatDuration(result.Wait.Delay),
			"usage_limit_id": result.Wait.LimitID,
		})
	}

	a.field(a.stdout, "session", result.SessionID)
	a.field(a.stdout, "usage limit found", yesNo(result.Found))
	a.field(a.stdout, "state", result.State)
	if !result.Found {
		a.field(a.stdout, "next", "nothing was changed; run this again once the session has gone idle")
		return nil
	}
	a.field(a.stdout, "banner", truncateText(result.Banner, 120))
	a.field(a.stdout, "reset known", yesNo(result.Wait.Known))
	a.field(a.stdout, "next attempt", formatTime(result.Wait.At, a.displayLocation(rt)))
	a.field(a.stdout, "wait", formatDuration(result.Wait.Delay))
	a.field(a.stdout, "reason", result.Reason)
	return nil
}

// formatJSONTime renders an instant for a JSON report, or an empty string when
// there is none, so a consumer never has to parse a zero time.
func formatJSONTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
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
	if outcome.Delay > 0 {
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
// A proven resume has had two shapes since print mode was retired: the
// supervisor accepted a handoff, or it published a state that establishes the
// conversation continued. Naming growth in either case would describe evidence
// ccresu no longer collects as proof, so the proof is rendered from the
// verification itself (recovery.Verification.Reason) rather than from a fixed
// sentence.
func describeVerification(outcome recovery.Outcome) string {
	verification := outcome.Verification
	if !verification.Proven {
		if missing := verification.Missing(); len(missing) > 0 {
			return "not proven: " + strings.Join(missing, "; ")
		}
		return "not proven"
	}
	return "proven: " + verification.Reason()
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

// supervisorReport is what `inspect` learned from the supervisor on demand (PRD 3
// R10), and why it could not learn it when it could not.
//
// The status is a word rather than a bool because four answers are different facts
// and an operator acts differently on each: "running" (here is what it says), "not
// running" (the documented exit-1 answer, and the next handoff starts one),
// "unavailable" (this provider has no supervisor surface) and "unreadable" (it was
// asked and failed, which is the one to look at).
type supervisorReport struct {
	Status  string   `json:"status"`
	State   string   `json:"state,omitempty"`
	Version string   `json:"version,omitempty"`
	Workers *int     `json:"workers,omitempty"`
	Socket  string   `json:"socket_dir,omitempty"`
	Argv    []string `json:"argv,omitempty"`
	// LogTarget is the supervisor state id the recent output was read from, empty
	// when there is none to read.
	LogTarget string `json:"log_target,omitempty"`
	// RecentOutput is the tail of `claude logs <state-id>`, newest last.
	RecentOutput []string `json:"recent_output,omitempty"`
	// DaemonOutput is the tail of `claude daemon logs` — the supervisor's own
	// log, which is where a failed handoff or a restart explains itself (R10).
	DaemonOutput []string `json:"daemon_output,omitempty"`
	// DaemonError is why the supervisor's log could not be read, when the read
	// failed while the supervisor itself answered.
	DaemonError string `json:"daemon_error,omitempty"`
	// Error explains an "unreadable" or "unavailable" status.
	Error string `json:"error,omitempty"`
	// Pending reports that the read is still in flight. The block then renders
	// its status line and nothing else, so a fill in progress cannot present an
	// unread log or a missing state id as a verdict.
	Pending bool `json:"-"`
}

// supervisorLogLines bounds how much recent output `inspect` prints: enough to see
// what the session last did, not enough to drown the page.
const supervisorLogLines = 20

// supervisorReport reads the supervisor on demand and turns it into one value the
// text and JSON renderings share.
//
// Every failure is a status rather than an error return: `inspect` is a report
// about a session, and refusing to print the session because the supervisor would
// not answer would hide the very facts an operator opened it to see.
func (a *App) supervisorReport(ctx context.Context, rt *Runtime, attempt storage.RecoveryAttempt, attemptErr error) supervisorReport {
	reader, ok := rt.Provider.(SupervisorReader)
	if !ok {
		// The JSON rendering reports the absence rather than omitting the key, so a
		// script can tell "ccresu cannot ask" from "the supervisor has nothing to
		// say".
		return supervisorReport{Status: "unavailable", Error: "this provider cannot read the supervisor"}
	}
	return a.readSupervisor(ctx, reader, attempt, attemptErr)
}

// readSupervisor asks the supervisor what it says now, and reads the session's
// recent output if the latest attempt recorded a state id to read it with.
func (a *App) readSupervisor(ctx context.Context, reader SupervisorReader, attempt storage.RecoveryAttempt, attemptErr error) supervisorReport {

	status, err := reader.DaemonStatus(ctx)
	report := supervisorReport{Argv: status.Argv}
	switch {
	case err != nil:
		report.Status = "unreadable"
		report.Error = err.Error()
		return report
	case !status.Running:
		// Exit 1 is the documented "the supervisor isn't running" answer, not a
		// failure: a machine that has never run `claude --bg` has no supervisor,
		// and the next handoff starts one.
		report.Status = "not running"
		return report
	}
	report.Status = "running"
	report.State = status.State
	if status.Version.Parsed {
		report.Version = status.Version.String()
	} else {
		report.Version = status.Version.Raw
	}
	if status.WorkersKnown {
		workers := status.Workers
		report.Workers = &workers
	}
	report.Socket = status.SocketDir

	// The supervisor's own log is read only while it is running: it is the file
	// its process follows, so a "not running" answer has nothing to read and
	// asking would report a failure where the honest answer is "there is no
	// supervisor yet" (R1). Both reads are on demand, for `inspect` only (R10).
	if daemon, daemonErr := reader.DaemonLogs(ctx); daemonErr != nil {
		report.DaemonError = daemonErr.Error()
	} else {
		report.DaemonOutput = lastLines(daemon.Text, supervisorLogLines)
	}

	// The recent output belongs to a session the supervisor hosts, and its name for
	// that session is the short state id the handoff printed. Without one there is
	// nothing to ask about, and saying so is better than printing an empty log as if
	// the session had printed nothing.
	if attemptErr != nil || strings.TrimSpace(attempt.HandoffStateID) == "" {
		return report
	}
	report.LogTarget = attempt.HandoffStateID
	logs, logErr := reader.SessionLogs(ctx, attempt.HandoffStateID)
	if logErr != nil {
		report.Error = logErr.Error()
		return report
	}
	report.RecentOutput = lastLines(logs.Text, supervisorLogLines)
	return report
}

// writeSupervisorDetail prints the supervisor report.
func (a *App) writeSupervisorDetail(w io.Writer, report supervisorReport) {
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Supervisor")
	a.field(w, "status", report.Status)
	if report.Pending {
		// A read in flight says only that it is in flight. Rendering the rest of
		// the block here would report "no recent output" about a log that has not
		// been read yet.
		return
	}
	if report.State != "" {
		a.field(w, "state", report.State)
	}
	if report.Version != "" {
		a.field(w, "version", report.Version)
	}
	if report.Workers != nil {
		a.field(w, "workers", *report.Workers)
	}
	if report.Socket != "" {
		a.field(w, "socket dir", report.Socket)
	}
	if len(report.Argv) > 0 {
		a.field(w, "command", "claude "+strings.Join(report.Argv, " "))
	}
	switch {
	case len(report.DaemonOutput) > 0:
		a.field(w, "supervisor log", fmt.Sprintf("last %d line(s) of `claude daemon logs`", len(report.DaemonOutput)))
		for _, line := range report.DaemonOutput {
			fmt.Fprintf(w, "  %s\n", truncateText(line, 200))
		}
	case report.DaemonError != "":
		a.field(w, "supervisor log", "`claude daemon logs` failed: "+truncateText(report.DaemonError, 200))
	default:
		a.field(w, "supervisor log", "`claude daemon logs` printed nothing")
	}
	switch {
	case report.LogTarget != "" && len(report.RecentOutput) > 0:
		a.field(w, "recent output", fmt.Sprintf("last %d line(s) of `claude logs %s`", len(report.RecentOutput), report.LogTarget))
		for _, line := range report.RecentOutput {
			fmt.Fprintf(w, "  %s\n", truncateText(line, 200))
		}
	case report.LogTarget != "":
		a.field(w, "recent output", fmt.Sprintf("`claude logs %s` printed nothing", report.LogTarget))
	default:
		a.field(w, "recent output", "not recorded: the latest attempt carries no supervisor state id")
	}
	if report.Error != "" {
		a.field(w, "error", truncateText(report.Error, 200))
	}
}

// lastLines returns at most n trailing lines of text.
func lastLines(text string, n int) []string {
	trimmed := strings.TrimRight(text, "\n")
	if strings.TrimSpace(trimmed) == "" || n <= 0 {
		return nil
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
