package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
)

// continuationID is the session a background job continues the conversation
// under. The real CLI reports an id of its own, which is the whole reason the
// loop has to follow it.
const continuationID = "22222222-2222-4222-8222-222222222222"

// continuationPath is the transcript of that continuation session.
const continuationPath = "/home/u/.claude/projects/-work-project/" + continuationID + ".jsonl"

// dispatchJob is the background job a resume created, as the CLI reports it.
func dispatchJob(jobID, state string) claude.AgentEntry {
	return claude.AgentEntry{
		StateID:   jobID,
		SessionID: continuationID,
		CWD:       "/work/project",
		Kind:      string(claude.SessionTypeBackground),
		Name:      "nightly-audit",
		Status:    "busy",
		State:     state,
	}
}

// TestDaemonKeepsFollowingAWorkingJobAndRefreshesItsName covers the first half of
// the loop: while the dispatched job is still working the row must stay WORKING
// and pick up the name the agents listing reports, so `ccresu sessions` shows
// what the operator calls the conversation rather than a UUID prefix.
func TestDaemonKeepsFollowingAWorkingJobAndRefreshesItsName(t *testing.T) {
	h := newHarness(t)
	const jobID = "9f3a1b2c"
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateWorking
		s.JobID = jobID
		s.ContinuationSessionID = continuationID
		s.ContinuationTranscriptPath = continuationPath
	})
	h.provider.SetJob(dispatchJob(jobID, "working"))

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}

	stored, err := h.store.GetSession(context.Background(), canonicalSessionID)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if stored.State != storage.StateWorking {
		t.Errorf("state = %q, want %q: the job is still working", stored.State, storage.StateWorking)
	}
	if stored.Name != "nightly-audit" {
		t.Errorf("name = %q, want the name the agents listing reports", stored.Name)
	}
	if stored.ContinuationSessionID != continuationID {
		t.Errorf("continuation = %q, want %q", stored.ContinuationSessionID, continuationID)
	}
}

// TestDaemonClosesTheLoopWhenTheJobStops covers the other half: once the job is
// no longer working the row leaves WORKING, and the continuation the job reports
// is recorded -- which is what detection reads next. The row is seeded without a
// continuation on purpose: a daemon that restarted after the dispatch has only
// the job id, and must recover the rest from the job itself.
func TestDaemonClosesTheLoopWhenTheJobStops(t *testing.T) {
	h := newHarness(t)
	const jobID = "9f3a1b2c"
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateWorking
		s.JobID = jobID
	})
	h.provider.SetJob(dispatchJob(jobID, "done"))
	h.provider.Statuses[continuationID] = claude.SessionStatus{
		Session:    claude.Session{ID: continuationID, TranscriptPath: continuationPath},
		Transcript: claude.TranscriptInfo{Path: continuationPath, SessionID: continuationID, Size: 4096, Lines: 120},
	}

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}

	stored, err := h.store.GetSession(context.Background(), canonicalSessionID)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if stored.State != storage.StateResumed {
		t.Errorf("state = %q, want %q: the job stopped working", stored.State, storage.StateResumed)
	}
	if stored.ContinuationSessionID != continuationID {
		t.Errorf("continuation = %q, want the id the job reported (%q)", stored.ContinuationSessionID, continuationID)
	}
	if stored.ContinuationTranscriptPath != continuationPath {
		t.Errorf("continuation transcript = %q, want %q", stored.ContinuationTranscriptPath, continuationPath)
	}
	// The original transcript and its snapshot stay where they were: they measure
	// the conversation that stopped, not the one that is running.
	if stored.TranscriptPath != "/home/u/.claude/projects/-work-project/"+canonicalSessionID+".jsonl" {
		t.Errorf("transcript = %q, want the original path untouched", stored.TranscriptPath)
	}
}

// TestDaemonClosesALoopWhoseJobIsGone proves the daemon does not poll a job that
// can never report again. Anything other than WORKING is a lie here: nothing is
// working on the continuation.
func TestDaemonClosesALoopWhoseJobIsGone(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateWorking
		s.JobID = "deadbeef"
	})

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}

	stored, err := h.store.GetSession(context.Background(), canonicalSessionID)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if stored.State != storage.StateResumed {
		t.Errorf("state = %q, want %q for a job the CLI no longer lists", stored.State, storage.StateResumed)
	}
}

// TestDaemonNeverClaimsToFollowAJobItDoesNotHave proves an empty job id is
// reported rather than pretended away: a WORKING row that names no job cannot be
// polled, so it must not stay WORKING for ever.
func TestDaemonNeverClaimsToFollowAJobItDoesNotHave(t *testing.T) {
	h := newHarness(t)
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateWorking
	})

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}

	stored, err := h.store.GetSession(context.Background(), canonicalSessionID)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if stored.State != storage.StateResumed {
		t.Errorf("state = %q, want %q when there is no job to poll", stored.State, storage.StateResumed)
	}
}

// TestDaemonRedisoversAResumedSession proves a recovery is not a dead end. The
// row was left RESUMED by the pass that verified the resume; the next pass must
// pick it up, record why, and put it back at the top of the cycle, otherwise the
// loop runs exactly once and the session is parked for ever with
// "none (already resumed)".
func TestDaemonRedisoversAResumedSession(t *testing.T) {
	h := newHarness(t)
	session := h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateResumed
		s.ContinuationSessionID = continuationID
		s.ContinuationTranscriptPath = continuationPath
	})
	// The continuation is still working through the same conversation: no new
	// banner yet, so nothing is planned. The state must still move, because
	// DISCOVERED is what re-reads this transcript on every later pass.
	h.seedTranscript(continuationPath, claude.Entry{Text: "continuing the work"})

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}

	stored, err := h.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if stored.State != storage.StateDiscovered {
		t.Fatalf("state = %q, want %q: a resumed session must be revisited", stored.State, storage.StateDiscovered)
	}

	transitions, err := h.store.ListTransitions(context.Background(), session.ID, 0)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	var moved *storage.Transition
	for i := range transitions {
		if transitions[i].FromState == storage.StateResumed && transitions[i].ToState == storage.StateDiscovered {
			moved = &transitions[i]
		}
	}
	if moved == nil {
		t.Fatalf("transitions = %+v, want a recorded RESUMED -> DISCOVERED move", transitions)
	}
	if !strings.Contains(moved.Reason, "revisited") {
		t.Errorf("reason = %q, want an explanation of the rediscovery", moved.Reason)
	}
	// The evidence the resumed conversation is under is untouched: the next cycle
	// reads the continuation, and the original transcript stays as the snapshot.
	if stored.ContinuationSessionID != continuationID || stored.ContinuationTranscriptPath != continuationPath {
		t.Errorf("continuation = %q (%q), want it kept",
			stored.ContinuationSessionID, stored.ContinuationTranscriptPath)
	}
}

// TestDaemonLeavesAFreshlyResumedSessionForTheNextPass pins the timing. The pass
// that proves a resume ends RESUMED; rediscovery belongs to a later pass, so a
// resume is never immediately re-examined with the transcript it was just proven
// on.
func TestDaemonLeavesAFreshlyResumedSessionForTheNextPass(t *testing.T) {
	h := newHarness(t)
	const jobID = "9f3a1b2c"
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateWorking
		s.JobID = jobID
	})
	h.provider.SetJob(dispatchJob(jobID, "done"))

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("first step: %v", err)
	}
	stored, err := h.store.GetSession(context.Background(), canonicalSessionID)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if stored.State != storage.StateResumed {
		t.Fatalf("state after the following pass = %q, want %q", stored.State, storage.StateResumed)
	}

	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("second step: %v", err)
	}
	stored, err = h.store.GetSession(context.Background(), canonicalSessionID)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if stored.State != storage.StateDiscovered {
		t.Errorf("state after the next pass = %q, want %q", stored.State, storage.StateDiscovered)
	}
}

// TestRediscoverLeavesFinishedSessionsAlone keeps the stage narrow: only RESUMED
// is revisited. A settled session stays settled, so rediscovery cannot resurrect
// work an operator ended.
func TestRediscoverLeavesFinishedSessionsAlone(t *testing.T) {
	h := newHarness(t)
	states := map[string]storage.State{
		"aaaaaaaa-0000-4000-8000-000000000101": storage.StateCompleted,
		"aaaaaaaa-0000-4000-8000-000000000102": storage.StateUserTerminated,
		"aaaaaaaa-0000-4000-8000-000000000103": storage.StateDisabled,
		"aaaaaaaa-0000-4000-8000-000000000104": storage.StateUnrecoverable,
	}
	for id, state := range states {
		h.seedSession(t, id, func(s *storage.Session) { s.State = state })
	}

	if err := NewDaemon(h.rt, time.Minute).rediscover(context.Background()); err != nil {
		t.Fatalf("rediscover: %v", err)
	}

	for id, want := range states {
		stored, err := h.store.GetSession(context.Background(), id)
		if err != nil {
			t.Fatalf("re-read %s: %v", id, err)
		}
		if stored.State != want {
			t.Errorf("state of %s = %q, want %q", id, stored.State, want)
		}
	}
}

// TestRediscoverNeedsAStore keeps the stage honest about a bare runtime: like
// every other stage it is best-effort and must not panic without a database.
func TestRediscoverNeedsAStore(t *testing.T) {
	if err := (&Daemon{runtime: &Runtime{}}).rediscover(context.Background()); err != nil {
		t.Errorf("rediscover without a store = %v, want nil", err)
	}
}

// TestRediscoverReportsAListFailure proves the stage surfaces a database error
// instead of silently skipping the pass, so a broken store is visible in the
// daemon's joined error rather than looking like "no resumed sessions".
func TestRediscoverReportsAListFailure(t *testing.T) {
	h := newHarness(t)
	rt := h.rt
	if err := h.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if err := NewDaemon(rt, time.Minute).rediscover(context.Background()); err == nil {
		t.Error("rediscover over a closed store = nil, want an error")
	}
}

// TestTheLoopRepeatsOnTheContinuationsOwnTranscript is the repeat the operator
// asked for: the resumed conversation continues under its own session id, so its
// own transcript is where the next usage limit appears. Detection must read that
// file -- not the abandoned one -- and plan the next wait from it.
func TestTheLoopRepeatsOnTheContinuationsOwnTranscript(t *testing.T) {
	h := newHarness(t)
	session := h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateResumed
		s.ContinuationSessionID = continuationID
		s.ContinuationTranscriptPath = continuationPath
	})
	// The limit that caused the first resume is already recorded. It carries a
	// different banner from the next one, exactly as two real limit messages do:
	// the reset instant they name differs.
	if _, err := h.store.RecordUsageLimit(context.Background(), storage.UsageLimit{
		SessionID:  session.ID,
		LimitType:  storage.LimitTypeFiveHour,
		RawMessage: limitBanner,
		DetectedAt: harnessBaseTime,
	}); err != nil {
		t.Fatalf("seed the first limit: %v", err)
	}
	// The original transcript is not growing; the continuation's is.
	h.seedTranscript(session.TranscriptPath, claude.Entry{Text: "the abandoned conversation"})
	h.seedTranscript(continuationPath,
		claude.Entry{Text: "continuing the work"},
		claude.Entry{Text: "You've hit your 5-hour limit \u00b7 resets 12pm (Europe/Berlin)"})

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.Step(context.Background()); err != nil {
		t.Fatalf("step: %v", err)
	}

	limits, err := h.store.ListUsageLimits(context.Background(), session.ID, 10)
	if err != nil {
		t.Fatalf("list limits: %v", err)
	}
	if len(limits) != 2 {
		t.Fatalf("usage limits = %d, want 2: the continuation hit its next limit", len(limits))
	}
	var found bool
	for _, limit := range limits {
		if strings.Contains(limit.ResetRaw, "12pm") {
			found = true
		}
	}
	if !found {
		t.Errorf("limits = %+v, want one carrying the continuation transcript's reset time", limits)
	}

	stored, err := h.store.GetSession(context.Background(), session.ID)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if stored.State != storage.StateWaitingForReset {
		t.Errorf("state = %q, want %q: the next wait is planned", stored.State, storage.StateWaitingForReset)
	}

	// The new wait was planned from the top of the cycle: the pass revisits the
	// resumed session first (RESUMED -> DISCOVERED) and only then re-reads the
	// continuation transcript, so a second recovery does not depend on a banner
	// that happens to differ from the last recorded one.
	transitions, err := h.store.ListTransitions(context.Background(), session.ID, 0)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	visited, detected := -1, -1
	for i, transition := range transitions {
		switch {
		case transition.FromState == storage.StateResumed && transition.ToState == storage.StateDiscovered:
			visited = i
		case transition.ToState == storage.StateUsageLimitDetected && detected < 0:
			detected = i
		}
	}
	if visited < 0 {
		t.Fatalf("transitions = %+v, want the resumed session to be revisited", transitions)
	}
	if detected < 0 || detected < visited {
		t.Errorf("transitions = %+v, want the rediscovery (%d) before the detection (%d)", transitions, visited, detected)
	}
}
