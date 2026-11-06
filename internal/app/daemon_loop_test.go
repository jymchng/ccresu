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
}
