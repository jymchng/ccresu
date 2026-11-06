package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	// defaultRediscoveryWindow is the shipped cap on how long a still-growing
	// continuation is deferred. It mirrors recovery.rediscovery_window so a
	// hand-built Daemon behaves like the shipped one.
	defaultRediscoveryWindow = 5 * time.Hour
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
		{"rediscover", d.rediscover},
		{"follow", d.followWorking},
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
		Name:            session.Name,
		Recoverable:     session.Recoverable,
		Reason:          session.Reason,
		TranscriptSize:  session.TranscriptSize,
		TranscriptLines: session.TranscriptLines,
	}
}

// rediscover decides what every resumed session needs next.
//
// A resume is not the end of a conversation, it is the start of the next window:
// the resumed conversation keeps running under its continuation session, and that
// session can hit a limit of its own. Nothing else would ever move a RESUMED row
// again, so without this stage a recovered session would be parked for ever --
// `ccresu inspect` used to report "none (already resumed)" and the loop the
// operator asked for ran exactly once.
//
// The one thing this stage must never do is resume a conversation twice. A
// continuation that is still producing output is still working, and dispatching a
// second resume for it would duplicate work that is already running, so the row is
// left RESUMED and deferred instead of being returned to discovery. The evidence
// is the row's own measurement: the continuation transcript's line count, compared
// against the count the last look recorded.
//
//   - a row whose deferral has not elapsed is not touched at all;
//   - a transcript that gained lines since the last look is still growing, so the
//     row stays RESUMED, the growth is recorded, a RESUMED -> RESUMED transition
//     notes it, and the next look is deferred by twice the previous deferral, up
//     to recovery.rediscovery_window;
//   - a transcript that did not grow (or one that cannot be measured yet) has
//     nothing running to duplicate, so the row goes back to DISCOVERED with a
//     fresh attempt budget, where the detect stage re-reads it.
//
// Only the row changes here: the transcript is read by the detect stage later in
// the same pass.
func (d *Daemon) rediscover(ctx context.Context) error {
	rt := d.runtime
	if rt.Store == nil {
		return nil
	}
	sessions, err := rt.Store.ListSessions(ctx, storage.SessionFilter{
		States: []storage.State{storage.StateResumed},
	})
	if err != nil {
		return fmt.Errorf("list resumed sessions: %w", err)
	}
	now := rt.Clock.Now().UTC()
	var errs []error
	for _, session := range sessions {
		if err := d.rediscoverOne(ctx, session, now); err != nil {
			errs = append(errs, fmt.Errorf("rediscover %s: %w", session.ID, err))
		}
	}
	return errors.Join(errs...)
}

// rediscoverOne applies the decision documented on rediscover to one row.
func (d *Daemon) rediscoverOne(ctx context.Context, session storage.Session, now time.Time) error {
	rt := d.runtime
	// A deferral that has not elapsed is the whole point of the backoff: the
	// conversation was still growing when this row was last looked at, so there
	// is nothing to decide until the instant ccresu promised.
	if session.RetryAt != nil && session.RetryAt.After(now) {
		rt.Log.Debug("resumed session deferred", "session", session.ID, "until", *session.RetryAt)
		return nil
	}

	lines, measured := d.continuationLines(session)
	switch {
	case measured && session.RediscoverLines > 0 && lines > session.RediscoverLines:
		previous := session.RediscoverLines
		if err := rt.Store.SetSessionRediscoverLines(ctx, session.ID, lines); err != nil {
			return fmt.Errorf("record rediscovery mark: %w", err)
		}
		reason := fmt.Sprintf(
			"the continuation is still working: %d lines, up from the %d last seen, so it is left to run",
			lines, previous)
		if _, err := rt.Store.TransitionSession(ctx, session.ID, storage.StateResumed, reason, 0); err != nil {
			return fmt.Errorf("record the deferral: %w", err)
		}
		// The transition clears any previous retry instant (it belongs to
		// RETRY_WAIT and to nothing else), so the new deferral is written after
		// it, never before.
		deferFor := d.rediscoveryDeferral(session, now)
		if err := rt.Store.SetSessionRetryAt(ctx, session.ID, now.Add(deferFor)); err != nil {
			return fmt.Errorf("defer the row: %w", err)
		}
		rt.Log.Info("continuation still growing", "session", session.ID,
			"lines", lines, "previous_lines", previous, "deferred_for", deferFor)
		return nil

	default:
		// Nothing is running: either the transcript stopped growing between two
		// looks, or it could not be measured (in which case the detect stage
		// reads the same file and reports the same failure). The baseline is
		// recorded first so the next look has something to compare against.
		if measured {
			if err := rt.Store.SetSessionRediscoverLines(ctx, session.ID, lines); err != nil {
				return fmt.Errorf("record rediscovery mark: %w", err)
			}
		}
		reason := "revisited: a resumed session starts the next cycle, so its continuation transcript is read again"
		if _, err := rt.Store.TransitionSession(ctx, session.ID, storage.StateDiscovered, reason, 0); err != nil {
			return fmt.Errorf("rediscover %s: %w", session.ID, err)
		}
		rt.Log.Debug("resumed session revisited", "session", session.ID,
			"continuation", session.ContinuationSessionID, "lines", lines)
		return nil
	}
}

// continuationLines measures the transcript a resumed row's conversation lives in.
//
// The continuation's own transcript is the file that keeps growing after a
// resume, so it is the one measured when it is known; otherwise the tracked
// transcript is. measured is false when nothing could be read, which callers must
// treat as "no comparison possible" rather than as zero lines.
func (d *Daemon) continuationLines(session storage.Session) (int64, bool) {
	path := strings.TrimSpace(session.ContinuationTranscriptPath)
	if path == "" {
		path = session.TranscriptPath
	}
	if path == "" || d.runtime.Transcripts == nil {
		return 0, false
	}
	info, err := d.runtime.Transcripts.ScanTranscript(path)
	if err != nil {
		d.runtime.Log.Debug("measure continuation transcript",
			"session", session.ID, "transcript", path, "error", err)
		return 0, false
	}
	return info.Lines, true
}

// rediscoveryDeferral is how long a still-growing continuation is left alone.
//
// The first deferral is the daemon's own interval: that is the soonest the row
// could have been looked at again anyway, so a growing continuation costs nothing
// extra there. Every further look that still sees growth doubles the previous
// deferral. The cap is recovery.rediscovery_window, the Claude quota window,
// because a continuation cannot work for longer than one quota window without a
// new limit of its own having to be visible: deferring past it would hide the
// very thing the loop exists to find.
//
// The previous deferral is recovered from the row rather than kept in memory:
// retry_at was written at the row's updated_at, so the gap between them is exactly
// that deferral. That is why a deferred row must not be written to until its
// deferral elapses, which is what rediscoverOne does.
func (d *Daemon) rediscoveryDeferral(session storage.Session, now time.Time) time.Duration {
	next := d.interval
	if next <= 0 {
		next = DefaultDaemonInterval
	}
	if session.RetryAt != nil && !session.UpdatedAt.IsZero() {
		if previous := session.RetryAt.Sub(session.UpdatedAt.UTC()); previous > 0 {
			next = previous * 2
		}
	}
	if window := d.rediscoveryWindow(); next > window {
		next = window
	}
	return next
}

// rediscoveryWindow is the configured cap on how long a growing continuation is
// deferred, falling back to the shipped default for a hand-built runtime.
func (d *Daemon) rediscoveryWindow() time.Duration {
	if d.runtime != nil && d.runtime.Config != nil {
		if window := d.runtime.Config.Recovery.RediscoveryWindow.Duration; window > 0 {
			return window
		}
	}
	return defaultRediscoveryWindow
}

// followWorking polls the background job of every WORKING session and closes the
// state out when the job is no longer working.
//
// This is the second half of a dispatched resume. The manager proves the
// continuation and leaves the row WORKING while the job is still running; from
// then on the question "is it still working?" is answered by the job itself,
// read through `claude agents --json`. Keeping the poll here rather than in the
// resume path means a daemon restart resumes following a job it never dispatched:
// the job id is on the row.
func (d *Daemon) followWorking(ctx context.Context) error {
	rt := d.runtime
	if rt.Provider == nil || rt.Store == nil {
		return nil
	}
	sessions, err := rt.Store.ListSessions(ctx, storage.SessionFilter{
		States: []storage.State{storage.StateWorking},
	})
	if err != nil {
		return fmt.Errorf("list working sessions: %w", err)
	}
	var errs []error
	for _, session := range sessions {
		if err := d.followOne(ctx, session); err != nil {
			errs = append(errs, fmt.Errorf("follow %s: %w", session.ID, err))
		}
	}
	return errors.Join(errs...)
}

// followOne reads one session's job and records what it says.
//
// A job that is still working keeps the row WORKING and refreshes the session
// name from the agents entry; a job that has stopped (or that the CLI no longer
// lists, which means it will never report again) moves the row to RESUMED, which
// is where detection picks the continuation transcript up for the next reset.
func (d *Daemon) followOne(ctx context.Context, session storage.Session) error {
	rt := d.runtime
	jobID := strings.TrimSpace(session.JobID)
	if jobID == "" {
		// Nothing to poll. Saying so and closing the state is honest; leaving the
		// row WORKING would promise polling that can never happen.
		_, err := rt.Store.TransitionSession(ctx, session.ID, storage.StateResumed,
			"the session is WORKING but no background job was recorded, so there is nothing to poll", 0)
		return err
	}

	entry, jobErr := rt.Provider.BackgroundJob(ctx, jobID)
	missing := errors.Is(jobErr, claude.ErrSessionNotFound)
	if jobErr != nil && !missing {
		return jobErr
	}
	if name := strings.TrimSpace(entry.Name); name != "" && name != session.Name {
		if err := rt.Store.SetSessionName(ctx, session.ID, name); err != nil {
			return err
		}
	}
	if !missing && agentEntryWorking(entry) {
		return nil
	}

	// The job has stopped working. Take the continuation id and its transcript
	// from the entry once more: the daemon may have restarted after the dispatch,
	// and this is the only moment the two facts are both available.
	if err := d.recordContinuation(ctx, session, entry); err != nil {
		return err
	}
	reason := fmt.Sprintf("the background job %s stopped working (state %s)", jobID, orUnknown(entry.State))
	if missing {
		reason = fmt.Sprintf("the background job %s is no longer listed, so it will not work again", jobID)
	}
	_, err := rt.Store.TransitionSession(ctx, session.ID, storage.StateResumed, reason, 0)
	return err
}

// recordContinuation stores the session id and transcript the job continued the
// conversation under, leaving the original transcript and its snapshot alone.
func (d *Daemon) recordContinuation(ctx context.Context, session storage.Session, entry claude.AgentEntry) error {
	continuationID := strings.TrimSpace(entry.SessionID)
	if continuationID == "" {
		return nil
	}
	path := session.ContinuationTranscriptPath
	if continuationID != session.ContinuationSessionID || path == "" {
		if status, err := d.runtime.Provider.GetSessionStatus(ctx, continuationID); err == nil {
			if transcript := strings.TrimSpace(status.Transcript.Path); transcript != "" {
				path = transcript
			}
		}
	}
	if continuationID == session.ContinuationSessionID && strings.TrimSpace(path) == strings.TrimSpace(session.ContinuationTranscriptPath) {
		return nil
	}
	return d.runtime.Store.SetSessionContinuation(ctx, session.ID, continuationID, path)
}

// agentEntryWorking reports whether an agents entry says the job is still busy.
//
// "working" is the only state that means busy in the CLI's vocabulary; any other
// value -- done, failed, stopped, or an unknown one from a newer CLI -- means the
// job will not work again, and polling it further would only delay detection.
func agentEntryWorking(entry claude.AgentEntry) bool {
	return strings.EqualFold(strings.TrimSpace(entry.State), "working")
}

// orUnknown renders an empty job state honestly.
func orUnknown(state string) string {
	if strings.TrimSpace(state) == "" {
		return "unknown"
	}
	return state
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
	// RESUMED is scanned too: after a resume the conversation continues in the
	// continuation session, whose transcript may already hold the next usage
	// limit. Skipping RESUMED would park that conversation until an operator
	// intervened, which is exactly what this loop exists to avoid.
	sessions, err := rt.Store.ListSessions(ctx, storage.SessionFilter{
		Enabled:     &enabled,
		Recoverable: &enabled,
		States:      []storage.State{storage.StateDiscovered, storage.StateRunning, storage.StateResumed},
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
	// A deferred row is still working: the rediscovery stage owns it until its
	// deferral elapses, and planning a wait for it here would resume a
	// conversation that is already running.
	if session.State == storage.StateResumed && session.RetryAt != nil &&
		session.RetryAt.After(rt.Clock.Now().UTC()) {
		return nil
	}
	// A dispatched resume moves the conversation into a new session, whose
	// transcript is a different file. That file is where the next reset time
	// will be written, so it is read when it is known; the original transcript
	// keeps the snapshot that proves growth and stays where it was.
	path := strings.TrimSpace(session.ContinuationTranscriptPath)
	if path == "" {
		path = session.TranscriptPath
	}
	if path == "" {
		return nil
	}
	entries, err := rt.Transcripts.TailEntries(path, d.tail)
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
	// The banner in the tail may be the very one that produced the last resume. A
	// transcript that has not grown past the line count that attempt recorded is
	// still describing work ccresu has already answered, so re-planning it would
	// dispatch a second resume for the same limit -- which is exactly the double
	// resume this stage must never cause. Only material the last attempt never saw
	// can name a limit of its own.
	if d.alreadyDetected(ctx, session.ID, event) && !d.grewPastLastAttempt(ctx, session, path) {
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

// grewPastLastAttempt reports whether the transcript being read holds material
// the last proved resume never saw.
//
// The mark it compares against is the continuation transcript's line count at the
// instant that attempt was proved (sessions.continuation_transcript_lines). A
// transcript that has not passed it cannot contain a new limit: the newest banner
// in it is the one ccresu already resumed for. Passing it means the conversation
// has moved on, so whatever limit the tail now names is a new one.
//
// A row with no mark (zero) has no attempt to duplicate: nothing was ever proved
// against this file, so the banner comparison alone decides, exactly as it did
// before the measurement existed. Zero is what every row stored before it carries,
// and treating it as "not grown" would freeze those rows for ever. The only row
// without a mark that may re-plan a banner ccresu already recorded is one that was
// deliberately returned to DISCOVERED, which is the re-open the loop depends on.
//
// A file that cannot be read proves nothing either, and the safe reading of "I
// cannot tell whether this is new" is "do not resume again": the detect stage has
// already logged why the transcript could not be read.
func (d *Daemon) grewPastLastAttempt(ctx context.Context, session storage.Session, path string) bool {
	if session.ContinuationTranscriptLines <= 0 {
		return session.State == storage.StateDiscovered
	}
	if d.runtime.Transcripts == nil || path == "" {
		return false
	}
	info, err := d.runtime.Transcripts.ScanTranscript(path)
	if err != nil {
		d.runtime.Log.Debug("measure transcript against the last attempt",
			"session", session.ID, "transcript", path, "error", err)
		return false
	}
	return info.Lines > session.ContinuationTranscriptLines
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
		//
		// WORKING belongs to the follow stage. The manager refuses such a row --
		// WORKING is settled, so Recover answers ErrNotEligible and this stage
		// ignores it -- but asking a question whose answer is always "no" hides who
		// owns the row. The daemon polls the job until it stops and only then moves
		// the session back to RESUMED, which is where a dispatch becomes legal
		// again once the continuation transcript shows a limit of its own.
		switch session.State {
		case storage.StateWaitingForReset, storage.StateResuming, storage.StateCheckingSession, storage.StateCheckingLimit, storage.StateWorking:
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
