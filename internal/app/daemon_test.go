package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/config"
	"github.com/jymchng/ccresu/internal/detector"
	"github.com/jymchng/ccresu/internal/recovery"
	"github.com/jymchng/ccresu/internal/scheduler"
	"github.com/jymchng/ccresu/internal/storage"
	"github.com/jymchng/ccresu/internal/telegram"
)

// limitBanner is a transcript line the shipped detector recognises as a five-hour
// usage limit. It carries no reset phrase, so the scheduler must poll.
const limitBanner = "You've hit your 5-hour limit \u00b7 resets soon"

// seedTranscript makes the fake transcript store serve entries for path.
func (h *harness) seedTranscript(path string, entries ...claude.Entry) {
	if h.transcripts.entries == nil {
		h.transcripts.entries = map[string][]claude.Entry{}
	}
	h.transcripts.entries[path] = entries
}

// discoveredSession is one session as the Claude Code provider would report it.
func discoveredSession(id, transcriptPath string, kind claude.SessionType) claude.Session {
	return claude.Session{
		ID:              id,
		Type:            kind,
		ProjectPath:     "/work/project",
		TranscriptPath:  transcriptPath,
		TranscriptSize:  512,
		TranscriptLines: 7,
		Recoverable:     true,
	}
}

func TestDaemonStepDiscoversSessions(t *testing.T) {
	h := newHarness(t)
	h.provider.SetSession(discoveredSession(canonicalSessionID, "/tmp/a.jsonl", claude.SessionTypeBackground))

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}
	if daemon.Steps() != 1 {
		t.Errorf("steps = %d, want 1", daemon.Steps())
	}

	session, err := h.store.GetSession(context.Background(), canonicalSessionID)
	if err != nil {
		t.Fatalf("session was not persisted: %v", err)
	}
	if session.TranscriptPath != "/tmp/a.jsonl" {
		t.Errorf("transcript path = %q", session.TranscriptPath)
	}
	// A background agent session is stored as headless, which is the name the
	// database uses for the same thing.
	if session.Type != storage.SessionTypeHeadless {
		t.Errorf("type = %q, want %q", session.Type, storage.SessionTypeHeadless)
	}
	if session.Provider != "claude" {
		t.Errorf("provider = %q", session.Provider)
	}
}

func TestDaemonDetectsAUsageLimitOnce(t *testing.T) {
	h := newHarness(t)
	session := h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.TranscriptLines = 3
	})
	h.seedTranscript(session.TranscriptPath, claude.Entry{Text: "hello"}, claude.Entry{Text: limitBanner})

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}

	limit, err := h.store.LatestUsageLimit(context.Background(), canonicalSessionID)
	if err != nil {
		t.Fatalf("usage limit was not recorded: %v", err)
	}
	if limit.LimitType != storage.LimitTypeFiveHour {
		t.Errorf("limit type = %q", limit.LimitType)
	}
	if !strings.Contains(limit.RawMessage, "5-hour limit") {
		t.Errorf("raw message = %q", limit.RawMessage)
	}

	// The reset phrase is absent, so the schedule must poll rather than wait for
	// a fixed instant.
	sched, err := h.rt.Scheduler.Restore(context.Background())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(sched) != 1 {
		t.Fatalf("schedules = %d, want 1", len(sched))
	}
	if !sched[0].Unknown || sched[0].ResetAt.IsZero() == false {
		t.Errorf("schedule = %+v, want an unknown reset with no instant", sched[0])
	}

	stored, err := h.store.GetSession(context.Background(), canonicalSessionID)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if stored.State != storage.StateWaitingForReset {
		t.Errorf("state = %q, want %q", stored.State, storage.StateWaitingForReset)
	}

	// A known reset cannot produce a "waiting" message on top of the detection
	// message; an unknown one needs both.
	queued := h.notifier.queued()
	kinds := make([]storage.NotificationEvent, 0, len(queued))
	for _, req := range queued {
		kinds = append(kinds, req.Event)
	}
	if len(kinds) != 2 || kinds[0] != storage.EventLimitDetected || kinds[1] != storage.EventWaiting {
		t.Errorf("queued notifications = %v", kinds)
	}

	// Re-reading the same banner must record nothing: detection is idempotent by
	// fingerprint, not by the session's state. The row is re-read first because
	// the pass has already parked it, and passing the stale pre-pass value would
	// be testing a session that no longer exists.
	session, err = h.store.GetSession(context.Background(), canonicalSessionID)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if err := daemon.detectIn(context.Background(), session); err != nil {
		t.Fatalf("second detection: %v", err)
	}
	limits, err := h.store.ListUsageLimits(context.Background(), canonicalSessionID, 0)
	if err != nil {
		t.Fatalf("list usage limits: %v", err)
	}
	if len(limits) != 1 {
		t.Errorf("usage limits = %d, want 1", len(limits))
	}
	if got := len(h.notifier.queued()); got != 2 {
		t.Errorf("queued notifications after the second pass = %d, want 2", got)
	}
}

func TestDaemonDetectionSkipsBlockedSources(t *testing.T) {
	h := newHarness(t)
	session := h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.TranscriptLines = 3
	})
	h.seedTranscript(session.TranscriptPath, claude.Entry{Text: limitBanner})
	daemon := NewDaemon(h.rt, time.Minute)
	ctx := context.Background()

	// A transcript that cannot be read is normal, not fatal.
	h.transcripts.err = errors.New("transcript vanished")
	if err := daemon.detectIn(ctx, session); err != nil {
		t.Errorf("unreadable transcript: %v", err)
	}
	h.transcripts.err = nil

	// A session with no transcript path cannot be inspected.
	noPath := session
	noPath.TranscriptPath = ""
	if err := daemon.detectIn(ctx, noPath); err != nil {
		t.Errorf("session without a transcript: %v", err)
	}

	// Monitoring is opt-out: with detection off nothing is recorded.
	h.cfg.Monitoring.DetectUsageLimits = false
	if err := daemon.detect(ctx); err != nil {
		t.Errorf("detect with monitoring off: %v", err)
	}
	if _, err := h.store.LatestUsageLimit(ctx, canonicalSessionID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("a usage limit was recorded with detection disabled: %v", err)
	}

	// An empty transcript tail carries no text to analyse.
	h.cfg.Monitoring.DetectUsageLimits = true
	h.seedTranscript(session.TranscriptPath, claude.Entry{})
	if err := daemon.detectIn(ctx, session); err != nil {
		t.Errorf("empty transcript: %v", err)
	}
	if _, err := h.store.LatestUsageLimit(ctx, canonicalSessionID); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("a usage limit was recorded from an empty transcript: %v", err)
	}
}

func TestDaemonActsOnADueSchedule(t *testing.T) {
	h := newHarness(t)
	session := h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateWaitingForReset
		s.TranscriptLines = 1
		s.TranscriptSize = 10
	})
	// The transcript has a measurement before the turn; the resort below is only
	// the diagnostic a post-resume read records.
	h.provider.Statuses[canonicalSessionID] = claude.SessionStatus{
		Session:    claude.Session{ID: canonicalSessionID, Recoverable: true},
		Transcript: claude.TranscriptInfo{Path: "/tmp/t.jsonl", Size: 400, Lines: 9},
	}
	// The resume reports exit zero, and that exit status is the verdict. The
	// transcript the fake appends to is measured afterwards and recorded as a
	// diagnostic only.
	h.provider.OnResume = func(context.Context, string, string) (claude.ResumeResult, error) {
		h.provider.Statuses[canonicalSessionID] = claude.SessionStatus{
			Session:    claude.Session{ID: canonicalSessionID, Recoverable: true},
			Transcript: claude.TranscriptInfo{Path: "/tmp/t.jsonl", Size: 4096, Lines: 42},
		}
		return claude.ResumeResult{
			RequestedSessionID: canonicalSessionID,
			ExitCode:           0,
		}, nil
	}

	due := harnessBaseTime.Add(-time.Minute)
	if _, err := h.rt.Scheduler.Save(context.Background(), scheduler.Schedule{
		SessionID: session.ID,
		ResetAt:   due,
		CreatedAt: harnessBaseTime.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("save schedule: %v", err)
	}

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}

	attempts, err := h.store.ListAttempts(context.Background(), canonicalSessionID, 0)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].Success == nil || !*attempts[0].Success {
		t.Errorf("attempt = %+v, want a success", attempts[0])
	}

	stored, err := h.store.GetSession(context.Background(), canonicalSessionID)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if stored.State != storage.StateResumed {
		t.Errorf("state = %q, want %q", stored.State, storage.StateResumed)
	}

	// A recovered session's wait is over, so nothing is left to poll.
	remaining, err := h.rt.Scheduler.Restore(context.Background())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("schedules = %d, want 0", len(remaining))
	}
}

func TestDaemonAdvancesAFutureScheduleWithoutRecovering(t *testing.T) {
	h := newHarness(t)
	session := h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateWaitingForReset
	})
	if _, err := h.rt.Scheduler.Save(context.Background(), scheduler.Schedule{
		SessionID:  session.ID,
		ResetAt:    harnessBaseTime.Add(time.Hour),
		NextPollAt: harnessBaseTime.Add(time.Hour),
	}); err != nil {
		t.Fatalf("save schedule: %v", err)
	}

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}
	if calls := h.provider.Calls(); len(calls) > 0 {
		for _, call := range calls {
			if call.Name == "ResumeSession" {
				t.Fatalf("a schedule that is not due was resumed")
			}
		}
	}
	schedules, err := h.rt.Scheduler.Restore(context.Background())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(schedules) != 1 {
		t.Errorf("schedules = %d, want the wait to survive", len(schedules))
	}
}

func TestDaemonRecoversPendingSessions(t *testing.T) {
	h := newHarness(t)
	// A session parked on a usage-limit reset belongs to the scheduler: it has no
	// retry instant, so this stage must leave it alone.
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateWaitingForReset
	})
	// A session parked by a failed attempt carries the backoff instant instead,
	// and nothing else reads it, so this stage is what retries it.
	const retryID = "44444444-4444-4444-8444-444444444444"
	h.seedSession(t, retryID, func(s *storage.Session) {
		s.State = storage.StateWaitingForReset
		s.TranscriptLines = 1
		due := h.rt.Clock.Now().UTC().Add(-time.Minute)
		s.RetryAt = &due
	})
	// A retry that is not due yet must not be woken early.
	const laterID = "55555555-5555-4555-8555-555555555555"
	h.seedSession(t, laterID, func(s *storage.Session) {
		s.State = storage.StateWaitingForReset
		s.TranscriptLines = 1
		later := h.rt.Clock.Now().UTC().Add(time.Hour)
		s.RetryAt = &later
	})
	h.provider.Statuses[retryID] = claude.SessionStatus{
		Session:    claude.Session{ID: retryID, Recoverable: true},
		Transcript: claude.TranscriptInfo{Path: "/tmp/r.jsonl", Size: 400, Lines: 9},
	}
	h.provider.OnResume = func(context.Context, string, string) (claude.ResumeResult, error) {
		h.provider.Statuses[retryID] = claude.SessionStatus{
			Session:    claude.Session{ID: retryID, Recoverable: true},
			Transcript: claude.TranscriptInfo{Path: "/tmp/r.jsonl", Size: 4096, Lines: 42},
		}
		return claude.ResumeResult{
			RequestedSessionID: retryID,
			ExitCode:           0,
		}, nil
	}

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.recoverPending(context.Background()); err != nil {
		t.Fatalf("recover pending: %v", err)
	}

	waiting, err := h.store.ListAttempts(context.Background(), canonicalSessionID, 0)
	if err != nil {
		t.Fatalf("attempts: %v", err)
	}
	if len(waiting) != 0 {
		t.Errorf("a waiting session was resumed by the retry stage")
	}
	retried, err := h.store.ListAttempts(context.Background(), retryID, 0)
	if err != nil {
		t.Fatalf("attempts: %v", err)
	}
	if len(retried) != 1 {
		t.Errorf("attempts for the due retry = %d, want 1: a failed attempt must be retried when its backoff ends", len(retried))
	}
	early, err := h.store.ListAttempts(context.Background(), laterID, 0)
	if err != nil {
		t.Fatalf("attempts: %v", err)
	}
	if len(early) != 0 {
		t.Errorf("attempts for a retry that is not due yet = %d, want 0", len(early))
	}
}

func TestDaemonDeliversQueuedNotifications(t *testing.T) {
	h := newHarness(t)
	h.notifier.sent = telegram.Result{Sent: 2}

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.deliver(context.Background()); err != nil {
		t.Fatalf("deliver: %v", err)
	}

	h.notifier.sendErr = errors.New("the Bot API is unreachable")
	if err := daemon.deliver(context.Background()); err == nil {
		t.Error("deliver hid a queue failure")
	} else if !strings.Contains(err.Error(), "deliver notifications") {
		t.Errorf("err = %v, want the stage to be named", err)
	}

	// Notifications disabled is simply nothing to do.
	h.rt.Notifier = nil
	if err := daemon.deliver(context.Background()); err != nil {
		t.Errorf("deliver without a notifier: %v", err)
	}
}

func TestDaemonStepJoinsStageFailures(t *testing.T) {
	h := newHarness(t)
	h.provider.DiscoverErr = errors.New("claude agents failed")
	h.notifier.sendErr = errors.New("bot is down")

	daemon := NewDaemon(h.rt, time.Minute)
	err := daemon.Step(context.Background())
	if err == nil {
		t.Fatal("Step hid the failing stages")
	}
	// Every stage ran: one broken dependency must not stop the others, and each
	// failure is named.
	for _, want := range []string{"discover", "deliver"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want the %q stage named", err, want)
		}
	}
	if daemon.Steps() != 1 {
		t.Errorf("steps = %d, want the pass to be counted", daemon.Steps())
	}
}

func TestDaemonStepStopsOnACancelledContext(t *testing.T) {
	h := newHarness(t)
	daemon := NewDaemon(h.rt, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := daemon.Step(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if daemon.Steps() != 0 {
		t.Errorf("steps = %d, want a cancelled pass not to be counted", daemon.Steps())
	}
}

func TestDaemonRunStopsWhenTheContextEnds(t *testing.T) {
	h := newHarness(t)
	h.provider.SetSession(discoveredSession(canonicalSessionID, "/tmp/a.jsonl", claude.SessionTypeInteractive))
	daemon := NewDaemon(h.rt, time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx) }()

	// The first pass runs immediately: a daemon must not wait an interval before
	// looking at anything.
	waitForRun(t, "the daemon to run its first pass", func() bool { return daemon.Steps() >= 1 })

	h.clock.Advance(2 * time.Minute)
	waitForRun(t, "the daemon to wake on the advanced clock", func() bool { return daemon.Steps() >= 2 })

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil after a clean stop", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after the context ended")
	}

	// Shutdown is cooperative: the notifier drain must have finished too.
	if got := h.notifier.runCount(); got != 1 {
		t.Errorf("notifier runs = %d, want 1", got)
	}
	if _, err := h.store.GetSession(context.Background(), canonicalSessionID); err != nil {
		t.Errorf("the discovered session was not persisted: %v", err)
	}
}

func TestDaemonRunReportsADrainFailure(t *testing.T) {
	h := newHarness(t)
	drainErr := errors.New("telegram drain crashed")
	h.notifier.drainErr = drainErr

	daemon := NewDaemon(h.rt, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemon.Run(ctx) }()
	waitForRun(t, "the notifier to drain once", func() bool { return h.notifier.runCount() == 1 })
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, drainErr) {
			t.Fatalf("Run = %v, want the drain failure", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestDaemonRunRequiresItsInputs(t *testing.T) {
	if err := NewDaemon(nil, time.Minute).Run(context.Background()); err == nil {
		t.Error("Run accepted a nil runtime")
	}
	daemon := NewDaemon(newHarness(t).rt, time.Minute)
	if err := daemon.Run(nil); err == nil { //nolint:staticcheck // a nil context is exactly what is under test
		t.Error("Run accepted a nil context")
	}
}

func TestNewDaemonUsesTheConfiguredInterval(t *testing.T) {
	h := newHarness(t)
	h.cfg.Claude.DiscoveryInterval = config.NewDuration(45 * time.Second)
	daemon := NewDaemon(h.rt, 0)
	if daemon.interval != 45*time.Second {
		t.Errorf("interval = %s, want the configured 45s", daemon.interval)
	}

	h.cfg.Claude.DiscoveryInterval = config.NewDuration(0)
	if daemon := NewDaemon(h.rt, 0); daemon.interval != DefaultDaemonInterval {
		t.Errorf("interval = %s, want %s", daemon.interval, DefaultDaemonInterval)
	}
	if daemon := NewDaemon(h.rt, 5*time.Second); daemon.interval != 5*time.Second {
		t.Errorf("interval = %s, want the explicit 5s", daemon.interval)
	}
	if daemon := NewDaemon(nil, -1); daemon.interval != DefaultDaemonInterval {
		t.Errorf("interval = %s, want the default", daemon.interval)
	}
}

func TestDaemonTranscriptAndMappingHelpers(t *testing.T) {
	// Newest lines come first so a banner split across lines is still joined.
	entries := []claude.Entry{
		{Text: "oldest"},
		{Text: ""},
		{Text: "middle"},
		{Text: "newest"},
	}
	text := transcriptText(entries)
	if !strings.HasPrefix(text, "newest\nmiddle\noldest") {
		t.Errorf("transcriptText = %q", text)
	}
	if got := transcriptText(nil); got != "" {
		t.Errorf("transcriptText(nil) = %q", got)
	}
	// The join is bounded so a hostile transcript cannot grow without limit.
	huge := make([]claude.Entry, 0, 200)
	for i := 0; i < 200; i++ {
		huge = append(huge, claude.Entry{Text: strings.Repeat("x", 100)})
	}
	if got := len(transcriptText(huge)); got > 4100 {
		t.Errorf("transcriptText length = %d, want it bounded", got)
	}

	if got := limitType(detector.LimitFiveHour); got != storage.LimitTypeFiveHour {
		t.Errorf("limitType(five hour) = %q", got)
	}
	if got := limitType(detector.LimitKind("invented")); got != storage.LimitTypeOther {
		t.Errorf("limitType(invented) = %q, want the fallback", got)
	}

	if got := plural(1) + "|" + plural(0) + "|" + plural(2); got != "|s|s" {
		t.Errorf("plural = %q", got)
	}
}

func TestCmdDaemonRunsOnePass(t *testing.T) {
	h := newHarness(t)
	h.provider.SetSession(discoveredSession(canonicalSessionID, "/tmp/a.jsonl", claude.SessionTypeInteractive))

	code, stdout, stderr := h.exec(t, "daemon", "--once")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr %q)", code, stderr)
	}
	if !strings.Contains(stdout, "reconcile pass complete") || !strings.Contains(stdout, "1 session)") {
		t.Errorf("stdout = %q", stdout)
	}
	if _, err := h.store.GetSession(context.Background(), canonicalSessionID); err != nil {
		t.Errorf("the pass did not persist the session: %v", err)
	}
}

func TestCmdDaemonRefusesUsageAndDisabledMonitoring(t *testing.T) {
	h := newHarness(t)
	if code, _, _ := h.exec(t, "daemon", "extra"); code != ExitUsage {
		t.Errorf("exit = %d, want %d for an argument", code, ExitUsage)
	}
	if code, _, _ := h.exec(t, "daemon", "--interval", "-1s"); code != ExitUsage {
		t.Errorf("exit = %d, want %d for a negative interval", code, ExitUsage)
	}

	h.cfg.Monitoring.Enabled = false
	code, _, stderr := h.exec(t, "daemon", "--once")
	if code != ExitFailure {
		t.Fatalf("exit = %d, want %d", code, ExitFailure)
	}
	if !strings.Contains(stderr, "monitoring is disabled") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestCmdDaemonStopsOnASignal(t *testing.T) {
	h := newHarness(t)
	done := make(chan int, 1)
	go func() { done <- h.run("daemon") }()
	waitForRun(t, "the daemon command to report that it started", func() bool { return strings.Contains(h.stdout.String(), "daemon started") })
	h.signals <- os.Interrupt

	select {
	case code := <-done:
		if code != ExitOK {
			t.Fatalf("exit = %d, want %d (stderr %q)", code, ExitOK, h.stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not stop after the signal")
	}
	if !strings.Contains(h.stdout.String(), "daemon stopped") {
		t.Errorf("stdout = %q", h.stdout.String())
	}
}

// TestDaemonLeavesAPlannedRefusalWaitAlone pins the daemon's half of the park.
//
// A refusal is not a schedule to advance: the manager has already replaced the
// stale poll this pass loaded with the reset the banner named. Advancing the
// stale copy afterwards would save it back over the fresh wait, so the session
// would go on polling a reset it already knows.
func TestDaemonLeavesAPlannedRefusalWaitAlone(t *testing.T) {
	h := newHarness(t)
	session := h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateWaitingForReset
		s.TranscriptLines = 8470
		s.TranscriptSize = 27472239
	})
	h.provider.Statuses[canonicalSessionID] = claude.SessionStatus{
		Session:    claude.Session{ID: canonicalSessionID, Recoverable: true},
		Transcript: claude.TranscriptInfo{Path: "/tmp/t.jsonl", Size: 27472239, Lines: 8470},
	}
	h.provider.OnResume = func(context.Context, string, string) (claude.ResumeResult, error) {
		return claude.ResumeResult{}, &claude.CLIError{
			What:     "claude -p --resume",
			ExitCode: 1,
			Stdout:   []byte(usageLimitBanner),
			Kind:     claude.ErrUsageLimitInForce,
		}
	}

	// An unknown-reset poll that has come due: this is the stale wait the pass
	// loads, and the one Advance would write back.
	if _, err := h.rt.Scheduler.Save(context.Background(), scheduler.Schedule{
		SessionID:  session.ID,
		Unknown:    true,
		NextPollAt: harnessBaseTime.Add(-time.Minute),
		Attempts:   3,
	}); err != nil {
		t.Fatalf("save schedule: %v", err)
	}

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}

	schedules, err := h.rt.Scheduler.Restore(context.Background())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("schedules = %d, want the planned wait", len(schedules))
	}
	got := schedules[0]
	want := time.Date(2026, 1, 6, 9, 0, 0, 0, time.UTC)
	if got.Unknown {
		t.Errorf("the stale poll overwrote the planned wait: %+v", got)
	}
	if !got.ResetAt.Equal(want) {
		t.Errorf("reset at = %s, want the instant the banner named (%s)", got.ResetAt, want)
	}
	if got.Attempts != 0 {
		t.Errorf("polls = %d, want 0: the plan replaced the poll, it was not advanced", got.Attempts)
	}
	if got.UsageLimitID == 0 {
		t.Error("the planned wait lost its usage-limit row")
	}
}

// TestDaemonAdvancesThePollWhenTheRefusalCouldNotPlan covers the other half of
// the same branch: a resume the CLI refused reports ErrLimitNotCleared, but a
// parker that could not plan leaves the refusal with no instant, so no schedule
// was written and the due poll is still the only wait there is. It must be
// advanced, otherwise an unknown-reset schedule stays due and every pass asks
// the same question again.
func TestDaemonAdvancesThePollWhenTheRefusalCouldNotPlan(t *testing.T) {
	h := newHarness(t)
	session := h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateWaitingForReset
		s.TranscriptLines = 8470
		s.TranscriptSize = 27472239
	})
	h.provider.Statuses[canonicalSessionID] = claude.SessionStatus{
		Session:    claude.Session{ID: canonicalSessionID, Recoverable: true},
		Transcript: claude.TranscriptInfo{Path: "/tmp/t.jsonl", Size: 27472239, Lines: 8470},
	}
	// The resume runs and is refused by the CLI: this is the only way ccresu
	// learns the window has not reopened.
	h.provider.ResumeErr = &claude.CLIError{
		What:     "claude --resume",
		ExitCode: 1,
		Stdout:   []byte(usageLimitBanner),
		Kind:     claude.ErrUsageLimitInForce,
	}
	manager, err := recovery.New(recovery.Config{
		Store:       h.store,
		Provider:    h.provider,
		Clock:       h.clock,
		Enabled:     true,
		MaxAttempts: 3,
		// A parker that cannot plan: the refusal is still a wait, but it names no
		// instant, so the manager writes no schedule and the due poll stands.
		LimitPark: func(context.Context, string, string) (recovery.Wait, error) {
			return recovery.Wait{}, errors.New("cannot resolve the reset phrase")
		},
	})
	if err != nil {
		t.Fatalf("recovery manager: %v", err)
	}
	h.rt.Recovery = manager

	if _, err := h.rt.Scheduler.Save(context.Background(), scheduler.Schedule{
		SessionID:  session.ID,
		Unknown:    true,
		NextPollAt: harnessBaseTime.Add(-time.Minute),
		Attempts:   2,
	}); err != nil {
		t.Fatalf("save schedule: %v", err)
	}

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}

	// The pass did run the command — the refusal is the CLI's answer — so exactly
	// one resume was attempted, and the second pass is what the advanced poll
	// buys rather than a second resume in this one.
	resumes := 0
	for _, call := range h.provider.Calls() {
		if call.Name == "ResumeSession" {
			resumes++
		}
	}
	if resumes != 1 {
		t.Fatalf("resume calls = %d, want 1: the refusal comes from the resume", resumes)
	}

	schedules, err := h.rt.Scheduler.Restore(context.Background())
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("schedules = %d, want the poll to survive", len(schedules))
	}
	got := schedules[0]
	if !got.Unknown {
		t.Errorf("the poll was replaced by a wait nothing planned: %+v", got)
	}
	if got.Attempts != 3 {
		t.Errorf("polls = %d, want 3: a due poll must advance", got.Attempts)
	}
	if !got.NextPollAt.After(harnessBaseTime) {
		t.Errorf("next poll at = %s, want a future instant; a due poll retries every pass", got.NextPollAt)
	}

	stored, err := h.store.GetSession(context.Background(), canonicalSessionID)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if stored.State != storage.StateWaitingForReset {
		t.Errorf("state = %s, want %s", stored.State, storage.StateWaitingForReset)
	}
}

// TestScannedStatesFollowsTheRescanSwitch pins the filter itself: DISCOVERED is
// read always, and RESUMED — the state a limit can also land in, and the one the
// switch adds — is appended after it in lifecycle order so a log line naming the
// two reads naturally.
func TestScannedStatesFollowsTheRescanSwitch(t *testing.T) {
	h := newHarness(t)
	daemon := NewDaemon(h.rt, time.Minute)
	names := func(states []storage.State) string {
		parts := make([]string, 0, len(states))
		for _, state := range states {
			parts = append(parts, string(state))
		}
		return strings.Join(parts, ",")
	}

	h.cfg.Monitoring.RescanResumed = false
	if got, want := names(daemon.scannedStates()), "DISCOVERED"; got != want {
		t.Errorf("scannedStates with the rescan off = %q, want %q", got, want)
	}

	h.cfg.Monitoring.RescanResumed = true
	if got, want := names(daemon.scannedStates()), "DISCOVERED,RESUMED"; got != want {
		t.Errorf("scannedStates with the rescan on = %q, want %q", got, want)
	}

	// A daemon with no runtime still answers: the filter is read before the stage
	// has anything to look at, and a nil dereference there would take the whole
	// pass down.
	if got, want := names((&Daemon{}).scannedStates()), "DISCOVERED"; got != want {
		t.Errorf("scannedStates with no runtime = %q, want %q", got, want)
	}
}

// TestDetectRescanScope is the scope of the automatic rescan, end to end through
// the stage rather than through the filter alone.
//
// With monitoring.rescan_resumed on, a limit that lands in a RESUMED transcript
// parks the session in WAITING_FOR_RESET. With it off that state is never read
// — which is exactly the defect `ccresu search-limit` worked around by hand —
// while DISCOVERED is still read, because it is the state a limit is expected in.
//
// The collapse shrank this test's cast without changing its claim: RETRY_WAIT and
// WAITING_FOR_INPUT became DISCOVERED, which the scan reads unconditionally, and
// RUNNING became RESUMED, which is what the switch adds. The four rows it used to
// seed were therefore two situations, and they are spelled as two now.
func TestDetectRescanScope(t *testing.T) {
	const (
		discoveredID = "22222222-2222-4222-8222-222222222222"
		resumedID    = "33333333-3333-4333-8333-333333333333"
	)
	seeded := map[string]storage.State{
		discoveredID: storage.StateDiscovered,
		resumedID:    storage.StateResumed,
	}

	run := func(t *testing.T, rescan bool) *harness {
		t.Helper()
		h := newHarness(t)
		h.cfg.Monitoring.RescanResumed = rescan
		for id, state := range seeded {
			session := h.seedSession(t, id, func(s *storage.Session) {
				s.State = state
				s.TranscriptLines = 2
			})
			h.seedTranscript(session.TranscriptPath,
				claude.Entry{Text: "hello"},
				claude.Entry{Text: limitBanner})
		}
		daemon := NewDaemon(h.rt, time.Minute)
		if err := daemon.detect(context.Background()); err != nil {
			t.Fatalf("detect: %v", err)
		}
		return h
	}

	t.Run("on", func(t *testing.T) {
		h := run(t, true)
		for id := range seeded {
			if _, err := h.store.LatestUsageLimit(context.Background(), id); err != nil {
				t.Errorf("%s: usage limit was not recorded: %v", id, err)
			}
			stored, err := h.store.GetSession(context.Background(), id)
			if err != nil {
				t.Fatalf("%s: read session: %v", id, err)
			}
			if stored.State != storage.StateWaitingForReset {
				t.Errorf("%s: state = %s, want %s", id, stored.State, storage.StateWaitingForReset)
			}
		}
	})

	t.Run("off", func(t *testing.T) {
		h := run(t, false)
		// The unconditional half of the filter is unchanged: DISCOVERED is read.
		if _, err := h.store.LatestUsageLimit(context.Background(), discoveredID); err != nil {
			t.Errorf("DISCOVERED was not scanned with the rescan off: %v", err)
		}
		// RESUMED is what the switch adds: it is not read, and nothing about it
		// moves.
		if _, err := h.store.LatestUsageLimit(context.Background(), resumedID); !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("RESUMED was scanned with the rescan off: %v", err)
		}
		stored, err := h.store.GetSession(context.Background(), resumedID)
		if err != nil {
			t.Fatalf("read session: %v", err)
		}
		if stored.State != storage.StateResumed {
			t.Errorf("state = %s, want it unchanged at %s", stored.State, storage.StateResumed)
		}
	})
}
