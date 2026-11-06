package storage_test

import (
	"context"
	"errors"
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

// TestTheLineMarksAreWrittenIndependently covers the two setters the rediscovery
// guard depends on. They are separate columns because they answer different
// questions, so a write to one must leave the other exactly as it was: a shared
// field would make the "has it moved past the last attempt?" comparison advance
// whenever "is it still growing?" was measured, and every re-read would look like
// new material.
func TestTheLineMarksAreWrittenIndependently(t *testing.T) {
	store := openHandleStore(t)
	seedHandleSession(t, store)
	ctx := context.Background()

	if err := store.SetSessionContinuationLines(ctx, jobSession, 42); err != nil {
		t.Fatalf("SetSessionContinuationLines: %v", err)
	}
	if err := store.SetSessionRediscoverLines(ctx, jobSession, 90); err != nil {
		t.Fatalf("SetSessionRediscoverLines: %v", err)
	}
	stored := mustReadSession(t, store)
	if stored.ContinuationTranscriptLines != 42 {
		t.Errorf("ContinuationTranscriptLines = %d, want 42", stored.ContinuationTranscriptLines)
	}
	if stored.RediscoverLines != 90 {
		t.Errorf("RediscoverLines = %d, want 90", stored.RediscoverLines)
	}

	// Advancing the rediscovery mark must not move the attempt's mark: that is the
	// whole reason there are two.
	if err := store.SetSessionRediscoverLines(ctx, jobSession, 120); err != nil {
		t.Fatalf("SetSessionRediscoverLines (advance): %v", err)
	}
	stored = mustReadSession(t, store)
	if stored.ContinuationTranscriptLines != 42 {
		t.Errorf("ContinuationTranscriptLines = %d, want 42 after a rediscovery look",
			stored.ContinuationTranscriptLines)
	}
	if stored.RediscoverLines != 120 {
		t.Errorf("RediscoverLines = %d, want 120", stored.RediscoverLines)
	}

	// A negative count is clamped rather than stored: a caller that measured
	// nothing must not leave a number a later comparison reads as real.
	if err := store.SetSessionContinuationLines(ctx, jobSession, -5); err != nil {
		t.Fatalf("SetSessionContinuationLines (negative): %v", err)
	}
	if stored := mustReadSession(t, store); stored.ContinuationTranscriptLines != 0 {
		t.Errorf("ContinuationTranscriptLines = %d, want 0", stored.ContinuationTranscriptLines)
	}

	// An unknown row is reported, so a caller cannot believe it recorded a
	// measurement nothing holds.
	if err := store.SetSessionRediscoverLines(ctx, "99999999-9999-4999-8999-999999999999", 3); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("unknown session = %v, want ErrNotFound", err)
	}

	// A closed store reports that too, instead of silently dropping the write.
	closed := openHandleStore(t)
	if err := closed.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := closed.SetSessionContinuationLines(ctx, jobSession, 1); !errors.Is(err, storage.ErrClosed) {
		t.Errorf("closed store = %v, want ErrClosed", err)
	}
}
