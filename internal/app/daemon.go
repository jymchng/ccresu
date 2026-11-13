package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/detector"
	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/scheduler"
	"github.com/jymchng/ccresu/internal/storage"
	"github.com/jymchng/ccresu/internal/telegram"
)

// Daemon defaults. They mirror the configuration defaults so a hand-built
// Daemon still behaves like the shipped one.
const (
	// DefaultDaemonInterval is how often the reconcile pass runs.
	DefaultDaemonInterval = 30 * time.Second
	// defaultTranscriptTail is how many transcript lines the detector inspects.
	// One limit banner sits on the newest assistant line; a small window keeps a
	// re-scan cheap while still catching a banner written after a tool result.
	defaultTranscriptTail = 25
)

// Daemon is the watchdog loop.
//
// One pass ("step") is a complete reconcile: discover sessions, detect usage
// limits in the transcripts, act on schedules that are due, recover what is
// pending, and deliver queued notifications. Steps are separate from the loop so
// a test — and `ccresu daemon --once` — can run exactly one pass and inspect the
// result without timers.
type Daemon struct {
	runtime  *Runtime
	interval time.Duration
	// tail is how many transcript entries are inspected per session.
	tail int

	steps int
	mu    sync.Mutex
}

// NewDaemon builds a daemon over rt.
func NewDaemon(rt *Runtime, interval time.Duration) *Daemon {
	if interval <= 0 {
		interval = DefaultDaemonInterval
	}
	if rt != nil && rt.Config != nil && rt.Config.Claude.DiscoveryInterval.Duration > 0 {
		interval = rt.Config.Claude.DiscoveryInterval.Duration
	}
	return &Daemon{runtime: rt, interval: interval, tail: defaultTranscriptTail}
}

// Steps reports how many reconcile passes have completed, which is what a test
// asserts to prove the loop actually ran.
func (d *Daemon) Steps() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.steps
}

// Run reconciles until ctx is cancelled, then waits for every goroutine it
// started.
//
// Shutdown is cooperative: cancelling ctx stops the notifier drain and the loop,
// and Run does not return until both have finished, so no goroutine and no
// database write outlive the process.
func (d *Daemon) Run(ctx context.Context) error {
	if d == nil || d.runtime == nil {
		return errors.New("daemon needs a runtime")
	}
	if ctx == nil {
		return errors.New("daemon needs a context")
	}
	log := d.runtime.Log

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	notifierErr := make(chan error, 1)
	if notifier := d.runtime.Notifier; notifier != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := notifier.Run(ctx); err != nil &&
				!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
				notifierErr <- err
			}
		}()
	}

	// The first pass runs immediately: a daemon that has just started should not
	// wait a whole interval before looking at anything.
	if err := d.Step(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Warn("reconcile pass failed", "error", err)
	}

	timer := d.runtime.Clock.NewTimer(d.interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			cancel()
			wg.Wait()
			select {
			case err := <-notifierErr:
				return err
			default:
				return nil
			}
		case <-timer.C():
			if err := d.Step(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Warn("reconcile pass failed", "error", err)
			}
			timer.Reset(d.interval)
		}
	}
}

// Step runs one reconcile pass. Every stage is best-effort: a failure is joined
// into the returned error but never aborts the other stages, because a broken
// Telegram token must not stop recoveries.
func (d *Daemon) Step(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var errs []error
	for _, stage := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"discover", d.discover},
		{"detect", d.detect},
		{"schedule", d.actOnSchedules},
		{"recover", d.recoverPending},
		{"deliver", d.deliver},
	} {
		if err := stage.run(ctx); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			errs = append(errs, fmt.Errorf("%s: %w", stage.name, err))
			d.runtime.Log.Warn("daemon stage failed", "stage", stage.name, "error", err)
		}
	}
	d.mu.Lock()
	d.steps++
	d.mu.Unlock()
	return errors.Join(errs...)
}

// discover records every session the provider can see.
func (d *Daemon) discover(ctx context.Context) error {
	rt := d.runtime
	if rt.Provider == nil || rt.Store == nil {
		return nil
	}
	sessions, err := rt.Provider.Discover(ctx, claude.DiscoverFilter{})
	if err != nil {
		return fmt.Errorf("discover sessions: %w", err)
	}
	// Sessions the operator deleted are skipped, not upserted: discovery reads the
	// transcripts on disk, so without this the next pass would re-create the row
	// and the delete would look like it had failed. One query per pass, not one
	// per session.
	deleted, err := rt.Store.DeletedSessionIDs(ctx)
	if err != nil {
		return fmt.Errorf("list deleted sessions: %w", err)
	}
	for _, session := range sessions {
		if _, gone := deleted[session.ID]; gone {
			continue
		}
		if _, err := rt.Store.UpsertSession(ctx, storageSession(session)); err != nil {
			return fmt.Errorf("persist session %s: %w", session.ID, err)
		}
	}
	return nil
}

// storageSession converts a discovered session into its persisted form.
func storageSession(session claude.Session) storage.Session {
	kind := storage.SessionType(session.Type)
	if session.Type == claude.SessionTypeBackground {
		// The database names a background agent's session "headless".
		kind = storage.SessionTypeHeadless
	}
	if !kind.Valid() {
		kind = storage.SessionTypeUnknown
	}
	return storage.Session{
		ID:              session.ID,
		ProjectPath:     session.ProjectPath,
		TranscriptPath:  session.TranscriptPath,
		Provider:        "claude",
		Type:            kind,
		PID:             session.PID,
		Recoverable:     session.Recoverable,
		Reason:          session.Reason,
		TranscriptSize:  session.TranscriptSize,
		TranscriptLines: session.TranscriptLines,
	}
}

// detect scans transcripts for a usage limit and, on the first sighting, records
// it and schedules the wait.
//
// Detection is idempotent by comparison, not by luck: the newest usage-limit row
// for the session is the fingerprint, so re-reading the same banner on every pass
// records nothing and sends nothing.
func (d *Daemon) detect(ctx context.Context) error {
	rt := d.runtime
	if rt.Detector == nil || rt.Transcripts == nil || rt.Store == nil || rt.Scheduler == nil {
		return nil
	}
	if rt.Config != nil && (!rt.Config.Monitoring.Enabled || !rt.Config.Monitoring.DetectUsageLimits) {
		return nil
	}

	enabled := true
	sessions, err := rt.Store.ListSessions(ctx, storage.SessionFilter{
		Enabled:     &enabled,
		Recoverable: &enabled,
		States:      []storage.State{storage.StateDiscovered, storage.StateRunning},
	})
	if err != nil {
		return fmt.Errorf("list monitorable sessions: %w", err)
	}

	for _, session := range sessions {
		if err := d.detectIn(ctx, session); err != nil {
			return err
		}
	}
	return nil
}

// detectIn inspects one session transcript.
func (d *Daemon) detectIn(ctx context.Context, session storage.Session) error {
	rt := d.runtime
	if session.TranscriptPath == "" {
		return nil
	}
	entries, err := rt.Transcripts.TailEntries(session.TranscriptPath, d.tail)
	if err != nil {
		// A transcript that vanished is normal: Claude Code rewrites them. It is
		// not a reason to stop looking at every other session.
		rt.Log.Debug("read transcript tail", "session", session.ID, "error", err)
		return nil
	}

	text := transcriptText(entries)
	if text == "" {
		return nil
	}
	event := rt.Detector.Analyze(text)
	if !event.IsUsageLimit() {
		return nil
	}
	// A session returned to DISCOVERED is being re-opened on purpose: its last
	// recorded banner is the very reason it is back here, so skipping it as
	// "already seen" would leave the session discovered for ever. Re-planning is
	// what moves it to WAITING_FOR_RESET with a fresh resume time.
	if session.State != storage.StateDiscovered && d.alreadyDetected(ctx, session.ID, event) {
		return nil
	}

	now := rt.Clock.Now().UTC()
	sched, resolution, err := rt.Scheduler.PlanEvent(session.ID, event, now)
	if err != nil {
		return fmt.Errorf("plan schedule for %s: %w", session.ID, err)
	}
	recorded, err := rt.Store.RecordUsageLimit(ctx, storage.UsageLimit{
		SessionID: session.ID,
		LimitType: limitType(event.Limit),
		// Keep the banner searchable but bounded: a hostile transcript must not
		// be able to fill the database.
		RawMessage:    truncateText(event.Message, 2000),
		ResetAt:       sched.ResetAt,
		ResetTimezone: sched.ResetTimezone,
		ResetRaw:      sched.ResetRaw,
		Confidence:    event.Confidence,
		PausedGoal:    event.PausedGoal,
		DetectedAt:    now,
	})
	if err != nil {
		return fmt.Errorf("record usage limit for %s: %w", session.ID, err)
	}

	sched.UsageLimitID = recorded.ID
	if _, err := rt.Scheduler.Save(ctx, sched); err != nil {
		return fmt.Errorf("save schedule for %s: %w", session.ID, err)
	}
	if _, err := rt.Store.TransitionSession(ctx, session.ID, storage.StateUsageLimitDetected,
		"usage limit detected in the transcript", 0); err != nil {
		return fmt.Errorf("record the usage-limit state of %s: %w", session.ID, err)
	}
	waitingFor := "waiting for the reset time"
	if resolution.Known {
		waitingFor = "waiting until " + resolution.At.UTC().Format(time.RFC3339)
	}
	if _, err := rt.Store.TransitionSession(ctx, session.ID, storage.StateWaitingForReset, waitingFor, 0); err != nil {
		return fmt.Errorf("record the waiting state of %s: %w", session.ID, err)
	}
	rt.Log.Info("usage limit detected",
		"session", session.ID, "limit", string(event.Limit),
		"known_reset", resolution.Known, "reset_at", resolution.At)

	return d.notifyLimit(ctx, session, recorded, resolution, event)
}

// alreadyDetected reports whether this exact banner was already recorded.
func (d *Daemon) alreadyDetected(ctx context.Context, sessionID string, event detector.UsageLimitEvent) bool {
	latest, err := d.runtime.Store.LatestUsageLimit(ctx, sessionID)
	if err != nil {
		return false
	}
	return latest.ResetRaw == event.Reset.Raw &&
		latest.RawMessage == truncateText(event.Message, 2000)
}

// notifyLimit queues the "limit detected" and, when the reset time is unknown,
// the "waiting" messages.
func (d *Daemon) notifyLimit(ctx context.Context, session storage.Session, limit storage.UsageLimit,
	resolution scheduler.Resolution, event detector.UsageLimitEvent) error {
	notifier := d.runtime.Notifier
	if notifier == nil {
		return nil
	}
	data := telegram.EventData{
		SessionID:     session.ID,
		ProjectPath:   session.ProjectPath,
		LimitID:       limit.ID,
		LimitType:     limit.LimitType,
		ResetAt:       limit.ResetAt,
		ResetTimezone: limit.ResetTimezone,
		ResetRaw:      limit.ResetRaw,
		Reason:        event.Reason(),
	}
	for _, kind := range []storage.NotificationEvent{
		storage.EventLimitDetected,
		storage.EventWaiting,
	} {
		if !notifier.Enabled(kind) {
			continue
		}
		if kind == storage.EventWaiting && resolution.Known {
			// A known reset time does not need a "waiting" message on top of the
			// detection message: they would say the same thing twice.
			continue
		}
		if _, _, err := notifier.Notify(ctx, telegram.Request{
			Event:     kind,
			SessionID: session.ID,
			Data:      data,
		}); err != nil && !errors.Is(err, telegram.ErrDisabled) {
			return fmt.Errorf("queue %s notification for %s: %w", kind, session.ID, err)
		}
	}
	return nil
}

// transcriptText joins the newest transcript lines so a banner split across lines
// is still recognised.
func transcriptText(entries []claude.Entry) string {
	var text string
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Text == "" {
			continue
		}
		if text == "" {
			text = entries[i].Text
			continue
		}
		text += "\n" + entries[i].Text
		if len(text) > 4000 {
			break
		}
	}
	return text
}

// actOnSchedules recovers the sessions whose reset time (or next poll) has come.
func (d *Daemon) actOnSchedules(ctx context.Context) error {
	rt := d.runtime
	if rt.Scheduler == nil || rt.Recovery == nil {
		return nil
	}
	schedules, err := rt.Scheduler.Restore(ctx)
	if err != nil {
		return fmt.Errorf("load schedules: %w", err)
	}
	now := rt.Clock.Now().UTC()
	for _, sched := range schedules {
		if !sched.Due(now) {
			continue
		}
		d.markResetReached(ctx, sched.SessionID)
		outcome, recoverErr := rt.Recovery.RecoverRequest(ctx, recovery.Request{
			SessionID:    sched.SessionID,
			UsageLimitID: sched.UsageLimitID,
		})
		switch {
		case errors.Is(recoverErr, recovery.ErrLimitNotCleared):
			// The resume was refused because the limit is still in force, and the
			// manager has already recorded the refusal and saved the wait it
			// names. Advancing here would overwrite that schedule with the stale
			// one this pass loaded, so the fresh wait is left alone.
			rt.Log.Info("resume refused by the usage limit; waiting for the reset",
				"session", sched.SessionID)
		case recoverErr != nil && !errors.Is(recoverErr, recovery.ErrNotEligible):
			// Keep the schedule and back off: the limit may not have cleared yet.
			if _, err := rt.Scheduler.Advance(ctx, sched, now); err != nil {
				return fmt.Errorf("advance schedule for %s: %w", sched.SessionID, err)
			}
			rt.Log.Warn("scheduled recovery failed", "session", sched.SessionID, "error", recoverErr)
		case outcome.Resumed:
			if err := rt.Scheduler.Complete(ctx, sched.SessionID); err != nil {
				return fmt.Errorf("complete schedule for %s: %w", sched.SessionID, err)
			}
		default:
			if _, err := rt.Scheduler.Advance(ctx, sched, now); err != nil {
				return fmt.Errorf("advance schedule for %s: %w", sched.SessionID, err)
			}
		}
	}
	return nil
}

// markResetReached records that a waiting session's reset time has arrived.
//
// It is a separate step because the recovery state machine only offers
// RESET_TIME_REACHED -> CHECKING_SESSION: without this edge a due schedule would
// be rejected as an invalid transition, and the wait would never end.
func (d *Daemon) markResetReached(ctx context.Context, sessionID string) {
	rt := d.runtime
	session, err := rt.Store.GetSession(ctx, sessionID)
	if err != nil {
		rt.Log.Debug("read session for a due schedule", "session", sessionID, "error", err)
		return
	}
	if session.State != storage.StateWaitingForReset {
		return
	}
	if _, err := rt.Store.TransitionSession(ctx, sessionID, storage.StateResetTimeReached,
		"the scheduled reset time has arrived", 0); err != nil {
		rt.Log.Warn("record the reset for a due schedule", "session", sessionID, "error", err)
	}
}

// recoverPending recovers sessions that are waiting to be retried.
func (d *Daemon) recoverPending(ctx context.Context) error {
	rt := d.runtime
	if rt.Recovery == nil {
		return nil
	}
	pending, err := rt.Recovery.Pending(ctx)
	if err != nil {
		return fmt.Errorf("list pending sessions: %w", err)
	}
	for _, session := range pending {
		// WAITING_FOR_RESET belongs to the scheduler, and a session already being
		// recovered holds its own lock; skipping both keeps this stage from
		// fighting the one above it.
		switch session.State {
		case storage.StateWaitingForReset, storage.StateResuming, storage.StateCheckingSession, storage.StateCheckingLimit:
			continue
		}
		if _, err := rt.Recovery.Recover(ctx, session.ID); err != nil &&
			!errors.Is(err, recovery.ErrNotEligible) && !errors.Is(err, recovery.ErrAlreadyRunning) {
			rt.Log.Warn("recover pending session", "session", session.ID, "error", err)
		}
	}
	return nil
}

// deliver drains the notification queue.
func (d *Daemon) deliver(ctx context.Context) error {
	notifier := d.runtime.Notifier
	if notifier == nil {
		return nil
	}
	result, err := notifier.SendOnce(ctx)
	if err != nil {
		return fmt.Errorf("deliver notifications: %w", err)
	}
	if result.Sent > 0 || result.Failed > 0 {
		d.runtime.Log.Info("notifications delivered",
			"sent", result.Sent, "failed", result.Failed, "pending", result.RetryPending)
	}
	return nil
}

// cmdDaemon runs the watchdog loop.
func (a *App) cmdDaemon(ctx context.Context, args []string) error {
	fs := a.flags("daemon", "daemon [--once] [--interval DURATION]")
	once := fs.Bool("once", false, "run a single reconcile pass and exit")
	interval := fs.Duration("interval", 0, "reconcile interval (default: claude.discovery_interval)")
	if err := a.parseFlags(fs, args); err != nil {
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return usagef("daemon takes no arguments, got %q", rest[0])
	}
	if *interval < 0 {
		return usagef("--interval must not be negative")
	}

	rt, release, err := a.openRuntime(ctx)
	if err != nil {
		return err
	}
	defer release()

	if rt.Config != nil && !rt.Config.Monitoring.Enabled {
		return withCode(ExitFailure, errors.New("monitoring is disabled in the configuration"))
	}

	daemon := NewDaemon(rt, *interval)
	if *once {
		if err := daemon.Step(ctx); err != nil {
			return fmt.Errorf("reconcile pass: %w", err)
		}
		fmt.Fprintf(a.stdout, "reconcile pass complete (%d session%s)\n", countSessions(ctx, rt), plural(countSessions(ctx, rt)))
		return nil
	}

	fmt.Fprintf(a.stdout, "ccresu daemon started (interval %s)\n", daemon.interval)
	err = daemon.Run(ctx)
	if errors.Is(err, context.Canceled) {
		err = nil
	}
	if ctx.Err() != nil {
		fmt.Fprintln(a.stdout, "ccresu daemon stopped")
		return nil
	}
	if err != nil {
		return fmt.Errorf("daemon: %w", err)
	}
	fmt.Fprintln(a.stdout, "ccresu daemon stopped")
	return nil
}

// countSessions reports how many sessions are persisted, best-effort: it is used
// for a status line, so a failure simply reads as zero.
func countSessions(ctx context.Context, rt *Runtime) int {
	if rt == nil || rt.Store == nil {
		return 0
	}
	count, err := rt.Store.CountSessions(ctx, storage.SessionFilter{})
	if err != nil {
		return 0
	}
	return count
}

// plural returns the plural suffix for a count.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// limitType maps a detected limit kind onto the persisted enum.
//
// The two vocabularies are spelled identically for the kinds ccresu stores; the
// cast is explicit and validated so a future detector kind cannot silently become
// an invalid database row.
func limitType(kind detector.LimitKind) storage.LimitType {
	limit := storage.LimitType(string(kind))
	if !limit.Valid() {
		return storage.LimitTypeOther
	}
	return limit
}
