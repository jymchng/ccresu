package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
)

// handoverJobID is the job the dispatch reports. It is not derived from the
// session id, because a resume creates a job of its own and the loop has to work
// with whatever the CLI said.
const handoverJobID = "7c4d2e19"

// countCalls reports how often a provider method was called, so a test can prove
// the daemon did not dispatch a second resume.
func countCalls(provider *claude.FakeProvider, name string) int {
	n := 0
	for _, call := range provider.Calls() {
		if call.Name == name {
			n++
		}
	}
	return n
}

// findTransition reports the recorded edge from -> to, if it happened.
func findTransition(transitions []storage.Transition, from, to storage.State) (storage.Transition, bool) {
	for _, transition := range transitions {
		if transition.FromState == from && transition.ToState == to {
			return transition, true
		}
	}
	return storage.Transition{}, false
}

// TestRecoveryHandsAWorkingJobOverToTheDaemon drives the whole WORKING loop in
// one flow.
//
// The two halves of the loop are tested separately elsewhere: the recovery tests
// prove a job that is still `working` leaves the row in WORKING, and the daemon
// tests prove a row that starts WORKING ends RESPUMED. Neither proves the
// handover between them, and the handover is the part that can silently break:
// the recovery writes the job id, the continuation id and the continuation
// transcript path, and the daemon follows that job by id alone, possibly after a
// restart. This test therefore recovers a real session through
// Recovery.Recover, asserts exactly what the recovery persisted, then flips the
// job to `done` and lets one daemon pass finish the job.
func TestRecoveryHandsAWorkingJobOverToTheDaemon(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// The dispatch has to find the session it resumes, and the resume has to
	// report a job and a continuation session of its own -- that is what the real
	// CLI does, and it is why the loop has to follow the job.
	h.provider.SetSession(claude.Session{
		ID:             canonicalSessionID,
		ProjectPath:    "/work/project",
		TranscriptPath: "/home/u/.claude/projects/-work-project/" + canonicalSessionID + ".jsonl",
		Type:           claude.SessionTypeInteractive,
		State:          "idle",
		Recoverable:    true,
	})
	h.provider.ResumeResults[canonicalSessionID] = claude.ResumeResult{
		JobID:                 handoverJobID,
		ContinuationSessionID: continuationID,
	}
	// The continuation transcript exists and holds content, which is what proves
	// the conversation continues under the new id.
	h.provider.Statuses[continuationID] = claude.SessionStatus{
		Session:    claude.Session{ID: continuationID, TranscriptPath: continuationPath},
		Transcript: claude.TranscriptInfo{Path: continuationPath, SessionID: continuationID, Size: 4096, Lines: 120},
	}
	// The row as a session that just hit its limit, with the snapshot the growth
	// proof is measured against.
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateResetTimeReached
		s.TranscriptSize = 2000
		s.TranscriptLines = 10
	})

	// First half: dispatch. The fake's job stays `working`, because the manager
	// checks once (the harness timeout is negative).
	outcome, err := h.rt.Recovery.Recover(ctx, canonicalSessionID)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if outcome.State != storage.StateWorking {
		t.Fatalf("recovery left the row in %s, want %s: the job is still working",
			outcome.State, storage.StateWorking)
	}
	if !outcome.Resumed {
		t.Error("Resumed = false, want true: the dispatch did happen")
	}

	working, err := h.store.GetSession(ctx, canonicalSessionID)
	if err != nil {
		t.Fatalf("re-read session after recovery: %v", err)
	}
	if working.State != storage.StateWorking {
		t.Fatalf("persisted state = %s, want %s", working.State, storage.StateWorking)
	}
	// These three facts are the handover. The daemon has nothing else to go on.
	if working.JobID != handoverJobID {
		t.Errorf("JobID = %q, want %q", working.JobID, handoverJobID)
	}
	if working.ContinuationSessionID != continuationID {
		t.Errorf("ContinuationSessionID = %q, want %q", working.ContinuationSessionID, continuationID)
	}
	if working.ContinuationTranscriptPath != continuationPath {
		t.Errorf("ContinuationTranscriptPath = %q, want %q", working.ContinuationTranscriptPath, continuationPath)
	}

	transitions, err := h.store.ListTransitions(ctx, canonicalSessionID, 0)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	resumingToWorking, ok := findTransition(transitions, storage.StateResuming, storage.StateWorking)
	if !ok {
		t.Fatalf("no RESUMING -> WORKING transition was recorded; got %v", transitions)
	}
	if !strings.Contains(resumingToWorking.Reason, handoverJobID) {
		t.Errorf("transition reason = %q, want it to name the job %q",
			resumingToWorking.Reason, handoverJobID)
	}

	// Second half: the job stops, and one daemon pass must finish the handover.
	// The job entry carries a name the row has never seen, so the refresh is
	// observable.
	h.provider.SetJob(dispatchJob(handoverJobID, "done"))

	daemon := NewDaemon(h.rt, time.Minute)
	if err := daemon.Step(ctx); err != nil {
		t.Fatalf("daemon step: %v", err)
	}

	finished, err := h.store.GetSession(ctx, canonicalSessionID)
	if err != nil {
		t.Fatalf("re-read session after the daemon pass: %v", err)
	}
	if finished.State != storage.StateResumed {
		t.Fatalf("state = %s, want %s after the job stopped", finished.State, storage.StateResumed)
	}
	if finished.Name != "nightly-audit" {
		t.Errorf("name = %q, want the name the agents listing reported (%q)", finished.Name, "nightly-audit")
	}
	if finished.JobID != handoverJobID {
		t.Errorf("JobID = %q, want it kept: the attempt that dispatched it is still on the row", finished.JobID)
	}
	if finished.ContinuationSessionID != continuationID {
		t.Errorf("ContinuationSessionID = %q, want %q", finished.ContinuationSessionID, continuationID)
	}
	if finished.ContinuationTranscriptPath != continuationPath {
		t.Errorf("ContinuationTranscriptPath = %q, want %q", finished.ContinuationTranscriptPath, continuationPath)
	}
	// The original transcript and its snapshot stay where they were: they measure
	// the conversation that stopped, not the one that is running.
	if finished.TranscriptPath != working.TranscriptPath {
		t.Errorf("transcript = %q, want the original path %q untouched", finished.TranscriptPath, working.TranscriptPath)
	}

	transitions, err = h.store.ListTransitions(ctx, canonicalSessionID, 0)
	if err != nil {
		t.Fatalf("list transitions after the daemon pass: %v", err)
	}
	workingToResumed, ok := findTransition(transitions, storage.StateWorking, storage.StateResumed)
	if !ok {
		t.Fatalf("no WORKING -> RESUMED transition was recorded; got %v", transitions)
	}
	if !strings.Contains(workingToResumed.Reason, handoverJobID) {
		t.Errorf("transition reason = %q, want it to name the job %q",
			workingToResumed.Reason, handoverJobID)
	}

	// The pass followed the job the recovery dispatched; it did not resume the
	// session a second time. A second dispatch would duplicate the work the job
	// is doing, which is the failure this handover exists to prevent.
	if got := countCalls(h.provider, "ResumeSession"); got != 1 {
		t.Errorf("ResumeSession calls = %d, want 1: the daemon must follow the job, not dispatch again", got)
	}
	if got := countCalls(h.provider, "BackgroundJob"); got < 1 {
		t.Errorf("BackgroundJob calls = %d, want at least the dispatch's own poll", got)
	}
}

// TestFollowWorkingReportsAListFailure covers the listing failure of the follow
// stage. The stage cannot follow what it cannot list, and saying so is the
// difference between "nothing is working" and "we could not look".
func TestFollowWorkingReportsAListFailure(t *testing.T) {
	h := newHarness(t)
	daemon := NewDaemon(h.rt, time.Minute)
	if err := h.store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	err := daemon.followWorking(context.Background())
	if err == nil {
		t.Fatal("followWorking returned no error for a store it cannot read")
	}
	if !strings.Contains(err.Error(), "list working sessions") {
		t.Errorf("error = %q, want it to say what could not be listed", err)
	}
	if !errors.Is(err, storage.ErrClosed) {
		t.Errorf("error = %v, want it to wrap storage.ErrClosed", err)
	}
}

// TestTheDaemonKeepsAWorkingSessionWhenTheJobCannotBeRead covers the difference
// between "the job is gone" and "the job could not be read".
//
// A job the CLI no longer lists will never report again, so the row is closed
// (that case has its own test). A job whose listing *failed* may be working this
// very second: closing the row would record a stop that never happened and let
// the next pass resume a conversation that is already running, so the failure is
// reported and the row is left WORKING for the next pass to re-read.
func TestTheDaemonKeepsAWorkingSessionWhenTheJobCannotBeRead(t *testing.T) {
	h := newHarness(t)
	const jobID = "9f3a1b2c"
	h.seedSession(t, canonicalSessionID, func(s *storage.Session) {
		s.State = storage.StateWorking
		s.JobID = jobID
		s.ContinuationSessionID = continuationID
		s.ContinuationTranscriptPath = continuationPath
	})
	h.provider.BackgroundJobErr = errors.New("claude agents --json: exit status 1")

	daemon := NewDaemon(h.rt, time.Minute)
	err := daemon.Step(context.Background())
	if err == nil {
		t.Fatal("Step reported no error although the job listing failed")
	}
	if !strings.Contains(err.Error(), "follow "+canonicalSessionID) {
		t.Errorf("error = %q, want it to wrap the follow failure for %s", err, canonicalSessionID)
	}
	if !strings.Contains(err.Error(), "claude agents --json: exit status 1") {
		t.Errorf("error = %q, want it to keep the provider's reason", err)
	}

	stored, err := h.store.GetSession(context.Background(), canonicalSessionID)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if stored.State != storage.StateWorking {
		t.Errorf("state = %q, want %q: a read failure is not proof the job stopped",
			stored.State, storage.StateWorking)
	}
	// The rest of the pass ran, and it must not have dispatched a second resume
	// for a session whose job may still be working.
	if got := countCalls(h.provider, "ResumeSession"); got != 0 {
		t.Errorf("ResumeSession calls = %d, want 0: a working session must not be resumed again", got)
	}
}
