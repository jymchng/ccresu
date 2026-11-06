package storage_test

import (
	"context"
	"testing"

	"github.com/jymchng/ccresu/internal/storage"
)

// markSession is a row that carries both continuation measurements.
const markSession = "22222222-2222-4222-8222-222222222222"

// TestContinuationMarksRoundTrip pins the two measurements migration 0009 added:
// they must land in their own columns and come back out unchanged. A column list
// and a scan list that disagree still compile and still run, so this test is what
// makes that class of mistake fail loudly instead of surfacing as a session that
// rediscovers from the wrong line count.
func TestContinuationMarksRoundTrip(t *testing.T) {
	store := openHandleStore(t)
	ctx := context.Background()

	// A session that was never resumed reports zero, not "unknown": zero is the
	// value every pre-existing row gets from the migration's DEFAULT 0.
	seedHandleSession(t, store)
	if stored := mustReadSession(t, store); stored.ContinuationTranscriptLines != 0 || stored.RediscoverLines != 0 {
		t.Errorf("fresh session marks = (%d, %d), want (0, 0)",
			stored.ContinuationTranscriptLines, stored.RediscoverLines)
	}

	// Distinct, non-adjacent values: if the two columns were written or scanned in
	// the wrong order, or one were dropped, the pair could not come back intact.
	inserted, err := store.UpsertSession(ctx, storage.Session{
		ID:          markSession,
		ProjectPath: "/work/project",
		TranscriptPath: "/home/u/.claude/projects/-work-project/" +
			markSession + ".jsonl",
		Type:                        storage.SessionTypeInteractive,
		State:                       storage.StateWorking,
		Enabled:                     true,
		ContinuationTranscriptLines: 412,
		RediscoverLines:             7,
	})
	if err != nil {
		t.Fatalf("upsert session with marks: %v", err)
	}
	if !inserted {
		t.Fatal("upsert did not insert a new session")
	}

	stored, err := store.GetSession(ctx, markSession)
	if err != nil {
		t.Fatalf("read back session: %v", err)
	}
	if stored.ContinuationTranscriptLines != 412 {
		t.Errorf("ContinuationTranscriptLines = %d, want 412", stored.ContinuationTranscriptLines)
	}
	if stored.RediscoverLines != 7 {
		t.Errorf("RediscoverLines = %d, want 7", stored.RediscoverLines)
	}

	// The marks must also survive the path repeated redetection uses: listing the
	// row rather than fetching it by id goes through the same scanner, and a
	// listing that lost them would silently disable both comparisons.
	sessions, err := store.ListSessions(ctx, storage.SessionFilter{})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	var listed *storage.Session
	for i := range sessions {
		if sessions[i].ID == markSession {
			listed = &sessions[i]
			break
		}
	}
	if listed == nil {
		t.Fatalf("session %s missing from the listing", markSession)
	}
	if listed.ContinuationTranscriptLines != 412 || listed.RediscoverLines != 7 {
		t.Errorf("listed marks = (%d, %d), want (412, 7)",
			listed.ContinuationTranscriptLines, listed.RediscoverLines)
	}
}
