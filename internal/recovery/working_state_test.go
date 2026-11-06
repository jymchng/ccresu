package recovery

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jymchng/ccresu/internal/claude"
)

// TestRecoverLeavesAStillWorkingJobInTheWorkingState covers the RESUMING ->
// WORKING half of the loop.
//
// The verifier checks once (the test manager's timeout is negative), so a job
// that is still `working` when it is checked is not a failed resume: the
// dispatch is real and the conversation is being continued right now. Claiming
// RESUMED would be a lie about work that has not finished, and failing would
// spend an attempt on a resume that is in progress. The row therefore rests in
// WORKING with the job id on it, which is what the daemon's follow stage polls.
func TestRecoverLeavesAStillWorkingJobInTheWorkingState(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, recoverableSession(sessionA))
	ctx := context.Background()

	provider := growthProvider(10, 12)
	provider.jobFn = func(_ int, jobID string) (claude.AgentEntry, error) {
		return claude.AgentEntry{
			StateID:   jobID,
			SessionID: sessionB,
			Kind:      "background",
			Name:      "nightly-audit",
			State:     "working",
		}, nil
	}
	manager := testManager(t, store, provider, nil)

	outcome, err := manager.Recover(ctx, sessionA)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if outcome.State != StateWorking {
		t.Fatalf("outcome state = %s, want WORKING: the job is still working", outcome.State)
	}
	if !outcome.Resumed {
		t.Error("Resumed = false, want true: the dispatch did happen")
	}
	if outcome.Verification.Proven == false {
		t.Errorf("verification = %+v, want the continuation proven", outcome.Verification)
	}
	if outcome.Verification.JobState != "working" {
		t.Errorf("JobState = %q, want the state the CLI reported", outcome.Verification.JobState)
	}

	stored, err := store.GetSession(ctx, sessionA)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	if stored.State != StateWorking {
		t.Errorf("persisted state = %s, want WORKING", stored.State)
	}
	// The job handle is what a later pass polls, so it has to be on the row: a
	// daemon restart must be able to follow a job it never dispatched.
	if stored.JobID != jobID {
		t.Errorf("JobID = %q, want %q", stored.JobID, jobID)
	}
	if stored.ContinuationSessionID != sessionB {
		t.Errorf("ContinuationSessionID = %q, want %q", stored.ContinuationSessionID, sessionB)
	}

	// The attempt is a success even though the turn has not finished: what was
	// verified is the dispatch and the continuation, not the assistant's answer.
	attempts, err := store.ListAttempts(ctx, sessionA, 0)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(attempts))
	}
	if attempts[0].Success == nil || !*attempts[0].Success {
		t.Errorf("attempt success = %v, want true", attempts[0].Success)
	}
	if attempts[0].State != StateWorking {
		t.Errorf("attempt state = %s, want WORKING", attempts[0].State)
	}

	// RESUMING -> WORKING must be on disk with a reason that names the job, or
	// the operator cannot explain why a dispatched resume is not RESUMED yet.
	transitions, err := store.ListTransitions(ctx, sessionA, 0)
	if err != nil {
		t.Fatalf("list transitions: %v", err)
	}
	var found bool
	for _, transition := range transitions {
		if transition.FromState == StateResuming && transition.ToState == StateWorking {
			found = true
			if !strings.Contains(transition.Reason, jobID) ||
				!strings.Contains(transition.Reason, "working") {
				t.Errorf("reason = %q, want it to name job %s and say it is working",
					transition.Reason, jobID)
			}
		}
	}
	if !found {
		t.Errorf("transitions = %+v, want a recorded RESUMING -> WORKING move", transitions)
	}
}

// TestWorkingIsSettledForRecoveryAndPendingForRestart pins the two answers that
// stop a WORKING row from being resumed twice while still letting a restarted
// daemon pick it up.
func TestWorkingIsSettledForRecoveryAndPendingForRestart(t *testing.T) {
	// Settled is what the recovery engine checks before dispatching: the job is
	// already working on the conversation, so a second resume would duplicate it.
	if !Settled(StateWorking) {
		t.Error("Settled(WORKING) = false, want true: automatic recovery must not resume it again")
	}
	// Pending is what a restarted daemon checks: the job is still out there, so
	// the row is work in progress and must be picked back up.
	if !Pending(StateWorking) {
		t.Error("Pending(WORKING) = false, want true: a restart must resume following the job")
	}
	// It is not a terminal state, and nothing may claim it is finished.
	if Terminal(StateWorking) {
		t.Error("Terminal(WORKING) = true, want false: the job has not stopped")
	}

	// The refusal paths: recovery may not walk WORKING back into a check or a new
	// dispatch, because the turn it would ask about is the one already running.
	for _, to := range []State{StateCheckingSession, StateCheckingLimit, StateResuming, StateRunning} {
		if CanTransition(StateWorking, to) {
			t.Errorf("WORKING -> %s is legal, want it refused: the job is already running", to)
		}
	}
	// What the follow stage and the operator can do from WORKING.
	for _, to := range []State{StateResumed, StateRetryWait, StateDiscovered,
		StateUnrecoverable, StateUserTerminated, StateDisabled} {
		if !CanTransition(StateWorking, to) {
			t.Errorf("WORKING -> %s is illegal, want it allowed", to)
		}
	}
}

// TestRecoverRefusesAWorkingSession is the operator-facing refusal: `recover` on
// a row whose job is still running must not dispatch a second job.
//
// The refusal is ErrNotEligible -- the same answer a completed session gets --
// because there is nothing to recover while the continuation is being written.
func TestRecoverRefusesAWorkingSession(t *testing.T) {
	store := newTestStore(t)
	session := recoverableSession(sessionA)
	session.State = StateWorking
	session.JobID = jobID
	seedSession(t, store, session)
	provider := &providerStub{}
	manager := testManager(t, store, provider, nil)

	outcome, err := manager.Recover(context.Background(), sessionA)
	if !errors.Is(err, ErrNotEligible) {
		t.Fatalf("Recover error = %v, want ErrNotEligible", err)
	}
	if outcome.State != StateWorking {
		t.Errorf("outcome state = %s, want it unchanged as WORKING", outcome.State)
	}
	if provider.resumeCount() != 0 {
		t.Error("a WORKING session must not be dispatched again")
	}
	stored, readErr := store.GetSession(context.Background(), sessionA)
	if readErr != nil {
		t.Fatalf("re-read session: %v", readErr)
	}
	if stored.State != StateWorking || stored.JobID != jobID {
		t.Errorf("stored = %s / job %q, want WORKING / %q", stored.State, stored.JobID, jobID)
	}
	attempts, listErr := store.ListAttempts(context.Background(), sessionA, 0)
	if listErr != nil {
		t.Fatalf("list attempts: %v", listErr)
	}
	if len(attempts) != 0 {
		t.Errorf("attempts = %d, want none: the dispatch was refused", len(attempts))
	}
}
