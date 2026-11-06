package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/config"
	"github.com/jymchng/ccresu/internal/storage"
)

// recordLimit stores the banner a resume was dispatched for, which is the banner
// detection must not plan a second time.
func recordLimit(t *testing.T, h *harness, sessionID, message string) {
	t.Helper()
	if _, err := h.store.RecordUsageLimit(context.Background(), storage.UsageLimit{
		SessionID:  sessionID,
		LimitType:  storage.LimitTypeFiveHour,
		RawMessage: message,
		DetectedAt: harnessBaseTime,
	}); err != nil {
		t.Fatalf("record the usage limit: %v", err)
	}
}

// countLimits reports how many usage limits the store holds for a session.
func countLimits(t *testing.T, h *harness, sessionID string) int {
	t.Helper()
	limits, err := h.store.ListUsageLimits(context.Background(), sessionID, 0)
	if err != nil {
		t.Fatalf("list usage limits: %v", err)
	}
	return len(limits)
}

// resumeCalls reports how many times the daemon dispatched a resume.
func resumeCalls(h *harness) int {
	calls := 0
	for _, call := range h.provider.Calls() {
		if call.Name == "ResumeSession" {
			calls++
		}
	}
	return calls
}

// resumedSession seeds a resumed row whose last proved resume saw markLines in the
// continuation transcript, with a banner already recorded for it.
func resumedSession(t *testing.T, h *harness, markLines, rediscoverLines int64) storage.Session {
	t.Helper()
	session := h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateResumed
		s.ContinuationSessionID = continuationID
		s.ContinuationTranscriptPath = continuationPath
		s.ContinuationTranscriptLines = markLines
		s.RediscoverLines = rediscoverLines
	})
	recordLimit(t, h, session.ID, limitBanner)
	return session
}

// TestAStillGrowingContinuationIsNotResumedTwice is the guard the goal asks for:
// a conversation that is still producing output must be left running, not
// dispatched a second time because the banner that caused the first resume is
// still in its transcript.
func TestAStillGrowingContinuationIsNotResumedTwice(t *testing.T) {
	h := newHarness(t)
	session := resumedSession(t, h, 6, 6)
	// The same limit banner the resume was dispatched for is still the newest
	// thing in the continuation transcript, and the file gained three lines since
	// the last look: the conversation is working.
	h.seedTranscript(continuationPath, claude.Entry{Text: limitBanner})
	h.transcripts.lines[continuationPath] = 9
	// The pass interval is the first deferral, so it is pinned rather than left to
	// the configuration default.
	h.cfg.Claude.DiscoveryInterval = config.NewDuration(time.Minute)

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}

	stored, err := h.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if stored.State != storage.StateResumed {
		t.Errorf("state = %q, want %q: a still-growing continuation stays resumed", stored.State, storage.StateResumed)
	}
	if stored.RediscoverLines != 9 {
		t.Errorf("rediscover lines = %d, want 9: the look is recorded", stored.RediscoverLines)
	}
	if stored.ContinuationTranscriptLines != 6 {
		t.Errorf("continuation lines = %d, want 6: the attempt's mark is never advanced",
			stored.ContinuationTranscriptLines)
	}
	// The first deferral is the daemon's own interval: the soonest the row could
	// have been looked at again anyway.
	if stored.RetryAt == nil || !stored.RetryAt.Equal(harnessBaseTime.Add(time.Minute)) {
		t.Errorf("retry at = %v, want %v", stored.RetryAt, harnessBaseTime.Add(time.Minute))
	}
	if got := countLimits(t, h, session.ID); got != 1 {
		t.Errorf("usage limits = %d, want 1: the banner was already answered", got)
	}
	if got := resumeCalls(h); got != 0 {
		t.Errorf("resume calls = %d, want 0: the continuation is already running", got)
	}

	transitions, err := h.store.ListTransitions(context.Background(), session.ID, 0)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	var deferred bool
	for _, transition := range transitions {
		if transition.FromState == storage.StateResumed && transition.ToState == storage.StateResumed {
			deferred = true
			if !strings.Contains(transition.Reason, "still working") {
				t.Errorf("deferral reason = %q, want it to say the continuation is still working", transition.Reason)
			}
		}
		if transition.ToState == storage.StateDiscovered {
			t.Errorf("transition = %+v, want none back to discovery while the continuation is working", transition)
		}
	}
	if !deferred {
		t.Errorf("transitions = %+v, want a RESUMED -> RESUMED deferral", transitions)
	}
}

// TestTheDeferralBacksOffUpToTheQuotaWindow proves the wait grows while the
// continuation keeps working and never exceeds the configured quota window.
func TestTheDeferralBacksOffUpToTheQuotaWindow(t *testing.T) {
	h := newHarness(t)
	// The row was deferred for one minute at harnessBaseTime and that deferral has
	// now elapsed, exactly as it would when the daemon wakes up.
	session := resumedSession(t, h, 6, 6)
	if err := h.store.SetSessionRetryAt(context.Background(), session.ID, harnessBaseTime.Add(time.Minute)); err != nil {
		t.Fatalf("seed the previous deferral: %v", err)
	}
	h.seedTranscript(continuationPath, claude.Entry{Text: limitBanner})
	h.transcripts.lines[continuationPath] = 9
	h.clock.Set(harnessBaseTime.Add(time.Minute))

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}
	stored, err := h.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	// Twice the previous deferral: one minute of waiting bought two.
	if stored.RetryAt == nil || !stored.RetryAt.Equal(harnessBaseTime.Add(3*time.Minute)) {
		t.Errorf("retry at = %v, want %v: the deferral doubles", stored.RetryAt, harnessBaseTime.Add(3*time.Minute))
	}

	// A window shorter than the next doubling caps it: the deferral may never
	// outlast the quota window, because a continuation cannot work longer than
	// that without a new limit of its own being visible.
	h.cfg.Recovery.RediscoveryWindow = config.NewDuration(90 * time.Second)
	h.clock.Set(harnessBaseTime.Add(3 * time.Minute))
	h.transcripts.lines[continuationPath] = 12
	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("step after the deferral elapsed: %v", err)
	}
	stored, err = h.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("re-read session after the cap: %v", err)
	}
	if stored.RetryAt == nil || !stored.RetryAt.Equal(harnessBaseTime.Add(3*time.Minute).Add(90*time.Second)) {
		t.Errorf("retry at = %v, want the 90s window applied: %v", stored.RetryAt,
			harnessBaseTime.Add(3*time.Minute).Add(90*time.Second))
	}
}

// TestADeferredRowIsLeftAlone proves the backoff actually defers: a row inside its
// deferral is not re-measured, not re-transitioned and not detected, which is what
// stops a still-working conversation from being resumed twice.
func TestADeferredRowIsLeftAlone(t *testing.T) {
	h := newHarness(t)
	session := resumedSession(t, h, 6, 6)
	if err := h.store.SetSessionRetryAt(context.Background(), session.ID, harnessBaseTime.Add(time.Hour)); err != nil {
		t.Fatalf("seed the deferral: %v", err)
	}
	// A limit that is not the recorded one is in the transcript: inside the
	// deferral it must still be ignored, because the conversation is running.
	h.seedTranscript(continuationPath, claude.Entry{Text: "You've hit your 5-hour limit \u00b7 resets 12pm (Europe/Berlin)"})
	h.transcripts.lines[continuationPath] = 9

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}

	stored, err := h.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if stored.State != storage.StateResumed {
		t.Errorf("state = %q, want %q", stored.State, storage.StateResumed)
	}
	if stored.RediscoverLines != 6 {
		t.Errorf("rediscover lines = %d, want 6: a deferred row is not measured again", stored.RediscoverLines)
	}
	if stored.RetryAt == nil || !stored.RetryAt.Equal(harnessBaseTime.Add(time.Hour)) {
		t.Errorf("retry at = %v, want the seeded deferral kept", stored.RetryAt)
	}
	if got := countLimits(t, h, session.ID); got != 1 {
		t.Errorf("usage limits = %d, want 1: a deferred row is not detected", got)
	}
	transitions, err := h.store.ListTransitions(context.Background(), session.ID, 0)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	if len(transitions) != 0 {
		t.Errorf("transitions = %+v, want none while the deferral lasts", transitions)
	}
}

// TestAQuiescentContinuationReturnsToDiscovery is the other half: once the file
// stops growing there is nothing running to duplicate, so the row goes back to
// discovery with the deferral cleared -- and the banner that produced the last
// resume is still not planned a second time.
func TestAQuiescentContinuationReturnsToDiscovery(t *testing.T) {
	h := newHarness(t)
	session := resumedSession(t, h, 6, 6)
	if err := h.store.SetSessionRetryAt(context.Background(), session.ID, harnessBaseTime.Add(-time.Minute)); err != nil {
		t.Fatalf("seed an elapsed deferral: %v", err)
	}
	// Same line count as the last look: the conversation is not producing output.
	h.seedTranscript(continuationPath, claude.Entry{Text: limitBanner})
	h.transcripts.lines[continuationPath] = 6

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}

	stored, err := h.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if stored.State != storage.StateDiscovered {
		t.Errorf("state = %q, want %q", stored.State, storage.StateDiscovered)
	}
	if stored.RetryAt != nil {
		t.Errorf("retry at = %v, want the deferral cleared by the move", stored.RetryAt)
	}
	// Not grown past the attempt's mark, so the banner in the transcript is the
	// one that resume already answered.
	if got := countLimits(t, h, session.ID); got != 1 {
		t.Errorf("usage limits = %d, want 1: the same banner is not replanned", got)
	}
	if got := resumeCalls(h); got != 0 {
		t.Errorf("resume calls = %d, want 0", got)
	}
}

// TestTheFirstLookRecordsTheBaseline covers a resumed row the rediscovery stage has
// never measured: without a baseline there is no growth to compare, so the first
// look records one and hands the row back to discovery rather than deferring for
// ever.
func TestTheFirstLookRecordsTheBaseline(t *testing.T) {
	h := newHarness(t)
	session := resumedSession(t, h, 4, 0)
	h.seedTranscript(continuationPath, claude.Entry{Text: "the conversation moved on"})
	h.transcripts.lines[continuationPath] = 4

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}

	stored, err := h.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if stored.State != storage.StateDiscovered {
		t.Errorf("state = %q, want %q", stored.State, storage.StateDiscovered)
	}
	if stored.RediscoverLines != 4 {
		t.Errorf("rediscover lines = %d, want 4: the first look records the baseline", stored.RediscoverLines)
	}
	if stored.RetryAt != nil {
		t.Errorf("retry at = %v, want no deferral on the first look", stored.RetryAt)
	}
}

// TestAnUnmeasurableTranscriptIsNotTreatedAsGrowth proves a transcript that cannot
// be read does not park the row at RESUMED for ever, and is not mistaken for
// growth either: detection reads the same file and plans nothing.
func TestAnUnmeasurableTranscriptIsNotTreatedAsGrowth(t *testing.T) {
	h := newHarness(t)
	session := resumedSession(t, h, 6, 6)
	// Nothing is seeded for continuationPath, so the fake reports os.ErrNotExist,
	// exactly as a transcript Claude Code has not written yet does.

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}

	stored, err := h.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if stored.State != storage.StateDiscovered {
		t.Errorf("state = %q, want %q: an unreadable transcript is not growth", stored.State, storage.StateDiscovered)
	}
	if stored.RediscoverLines != 6 {
		t.Errorf("rediscover lines = %d, want 6: an unmeasurable look records nothing", stored.RediscoverLines)
	}
}

// TestTheBannerOfTheLastAttemptIsNotReplanned is the detection half of the guard:
// the same banner, in a transcript that has not grown past the last attempt's mark,
// describes work ccresu already answered.
func TestTheBannerOfTheLastAttemptIsNotReplanned(t *testing.T) {
	h := newHarness(t)
	session := h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateDiscovered
		s.ContinuationSessionID = continuationID
		s.ContinuationTranscriptPath = continuationPath
		s.ContinuationTranscriptLines = 6
	})
	recordLimit(t, h, session.ID, limitBanner)
	h.seedTranscript(continuationPath, claude.Entry{Text: limitBanner})
	h.transcripts.lines[continuationPath] = 6

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.detectIn(context.Background(), session); err != nil {
		t.Fatalf("detect: %v", err)
	}
	if got := countLimits(t, h, session.ID); got != 1 {
		t.Errorf("usage limits = %d, want 1: the banner was already answered", got)
	}
}

// TestGrownMaterialCanNameItsOwnLimit is the bypass the goal allows: once the
// transcript has grown past the mark the last attempt recorded, a banner in it is
// new material and may be planned even if its text is identical.
func TestGrownMaterialCanNameItsOwnLimit(t *testing.T) {
	h := newHarness(t)
	session := h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateDiscovered
		s.ContinuationSessionID = continuationID
		s.ContinuationTranscriptPath = continuationPath
		s.ContinuationTranscriptLines = 6
	})
	recordLimit(t, h, session.ID, limitBanner)
	h.seedTranscript(continuationPath, claude.Entry{Text: limitBanner})
	h.transcripts.lines[continuationPath] = 9

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.detectIn(context.Background(), session); err != nil {
		t.Fatalf("detect: %v", err)
	}
	if got := countLimits(t, h, session.ID); got != 2 {
		t.Errorf("usage limits = %d, want 2: material past the last attempt may name a limit", got)
	}
	stored, err := h.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if stored.State != storage.StateWaitingForReset {
		t.Errorf("state = %q, want %q", stored.State, storage.StateWaitingForReset)
	}
}
