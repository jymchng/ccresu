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
	// fingerprint, not by the session's state.
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
	// The transcript must have grown by the time verification looks, otherwise
	// the resume is correctly judged unproven.
	h.provider.Statuses[canonicalSessionID] = claude.SessionStatus{
		Session:    claude.Session{ID: canonicalSessionID, Recoverable: true},
		Transcript: claude.TranscriptInfo{Path: "/tmp/t.jsonl", Size: 400, Lines: 9},
	}
	h.provider.OnResume = func(context.Context, string, string) (claude.ResumeResult, error) {
		h.provider.Statuses[canonicalSessionID] = claude.SessionStatus{
			Session:    claude.Session{ID: canonicalSessionID, Recoverable: true},
			Transcript: claude.TranscriptInfo{Path: "/tmp/t.jsonl", Size: 4096, Lines: 42},
		}
		return claude.ResumeResult{
			RequestedSessionID: canonicalSessionID,
			SessionID:          canonicalSessionID,
			Verified:           true,
			Subtype:            "success",
			Result:             "continued",
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
	// WAITING_FOR_RESET belongs to the scheduler and must be left alone here.
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateWaitingForReset
	})
	const retryID = "44444444-4444-4444-8444-444444444444"
	h.seedSession(t, retryID, func(s *storage.Session) {
		s.State = storage.StateRetryWait
		s.TranscriptLines = 1
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
			SessionID:          retryID,
			Verified:           true,
			Subtype:            "success",
			Result:             "continued",
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
		t.Errorf("attempts for the retry-wait session = %d, want 1", len(retried))
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
	waitForRun(t, func() bool { return daemon.Steps() >= 1 })

	h.clock.Advance(2 * time.Minute)
	waitForRun(t, func() bool { return daemon.Steps() >= 2 })

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
	waitForRun(t, func() bool { return h.notifier.runCount() == 1 })
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
	waitForRun(t, func() bool { return strings.Contains(h.stdout.String(), "daemon started") })
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
