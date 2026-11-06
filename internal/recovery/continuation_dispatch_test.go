package recovery

import (
	"context"
	"strings"
	"testing"

	"github.com/jymchng/ccresu/internal/claude"
	"github.com/jymchng/ccresu/internal/storage"
)

// resumedSession is a row whose conversation already moved: the tracked id is
// sessionA, but the job that continued it reported sessionB, and the transcript
// that keeps growing is sessionB's.
//
// Its state is RESET_TIME_REACHED because that is where the loop stands when the
// next dispatch happens: detection found sessionB's next limit in sessionB's
// transcript, the wait was scheduled, and the reset arrived.
func resumedSession() storage.Session {
	session := recoverableSession(sessionA)
	session.State = StateResetTimeReached
	session.ContinuationSessionID = sessionB
	session.ContinuationTranscriptPath = "/home/u/.claude/projects/p/" + sessionB + ".jsonl"
	return session
}

// TestTheSecondCycleDispatchesAtTheContinuationSession is the repeat the operator
// asked for. Once a job has reported that the conversation continued as
// sessionB, a later limit must be resumed at sessionB: `claude --resume` at the
// tracked id would re-open the conversation that was abandoned, and the
// transcript the verifier watches would never grow.
func TestTheSecondCycleDispatchesAtTheContinuationSession(t *testing.T) {
	store := newTestStore(t)
	seedSession(t, store, resumedSession())
	provider := &providerStub{statusFn: func(call int, sessionID string) (claude.SessionStatus, error) {
		if call == 1 {
			return statusFor(sessionID, 10, 200), nil
		}
		return statusFor(sessionID, 12, 240), nil
	}}

	manager := testManager(t, store, provider, nil)
	outcome, err := manager.Recover(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if !outcome.Resumed || outcome.State != StateResumed {
		t.Fatalf("outcome = %s resumed=%v, want a proven RESUME", outcome.State, outcome.Resumed)
	}

	provider.mu.Lock()
	resumes := append([]resumeCall(nil), provider.resumes...)
	provider.mu.Unlock()
	if len(resumes) != 1 {
		t.Fatalf("resume calls = %d, want exactly one", len(resumes))
	}
	if resumes[0].sessionID != sessionB {
		t.Errorf("resumed %q, want %q: that is where the conversation lives",
			resumes[0].sessionID, sessionB)
	}
	if !strings.Contains(outcome.Attempt.Command, sessionB) {
		t.Errorf("attempt command = %q, want the dispatch at %q", outcome.Attempt.Command, sessionB)
	}

	stored, err := store.GetSession(context.Background(), sessionA)
	if err != nil {
		t.Fatalf("re-read session: %v", err)
	}
	// The tracked row is still the row: the conversation continued under the id
	// it already recorded, so nothing about it moved.
	if stored.ID != sessionA || stored.ContinuationSessionID != sessionB {
		t.Errorf("session = %s continuation %q, want %s / %q",
			stored.ID, stored.ContinuationSessionID, sessionA, sessionB)
	}
	// The continuation transcript is re-read from the session that holds it, so
	// the row points at the file the stub reports for sessionB rather than at a
	// stale path.
	if !strings.Contains(stored.ContinuationTranscriptPath, sessionB) {
		t.Errorf("continuation transcript = %q, want %q's transcript",
			stored.ContinuationTranscriptPath, sessionB)
	}
	// The line count this attempt was proved at is written down: it is the mark
	// the rediscovery stage compares against, so that a still-working
	// continuation is never resumed twice and new material still can be planned.
	// Both marks carry the verification's LinesAfter (12), because the same
	// reading is the attempt's mark and the baseline the first look compares to.
	if stored.ContinuationTranscriptLines != 12 {
		t.Errorf("continuation lines = %d, want 12: the attempt's mark",
			stored.ContinuationTranscriptLines)
	}
	if stored.RediscoverLines != 12 {
		t.Errorf("rediscover lines = %d, want 12: the baseline for still-growing",
			stored.RediscoverLines)
	}
}

// TestTheBaselineMeasuresTheContinuationTranscript proves the growth the verifier
// demands is measured where the conversation actually is. The row's own snapshot
// describes the abandoned file, so using it would either prove nothing or -- with
// a zero snapshot -- prove growth for a file that never changed.
func TestTheBaselineMeasuresTheContinuationTranscript(t *testing.T) {
	baseline := BaselineFromSession(resumedSession())
	if baseline.SessionID != sessionB {
		t.Errorf("baseline session = %q, want %q", baseline.SessionID, sessionB)
	}
	if baseline.TranscriptPath != resumedSession().ContinuationTranscriptPath {
		t.Errorf("baseline transcript = %q, want the continuation's", baseline.TranscriptPath)
	}
	if baseline.TranscriptLines != 0 {
		t.Errorf("baseline lines = %d, want 0: the row's snapshot measures another file",
			baseline.TranscriptLines)
	}

	// A row that has never been resumed is measured on its own file.
	plain := recoverableSession(sessionA)
	if got := BaselineFromSession(plain); got.SessionID != sessionA ||
		got.TranscriptPath != plain.TranscriptPath || got.TranscriptLines != plain.TranscriptLines {
		t.Errorf("baseline = %+v, want the row's own transcript", got)
	}
}

// TestDispatchTarget documents the one rule: a recorded continuation wins.
func TestDispatchTarget(t *testing.T) {
	if got := dispatchTarget(resumedSession()); got != sessionB {
		t.Errorf("dispatchTarget = %q, want %q", got, sessionB)
	}
	if got := dispatchTarget(recoverableSession(sessionA)); got != sessionA {
		t.Errorf("dispatchTarget = %q, want the tracked id %q", got, sessionA)
	}
}
