package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jymchng/ccresu/internal/storage"
)

// jobSession is the row the two handle setters are exercised on.
const jobSession = "11111111-1111-4111-8111-111111111111"

// openHandleStore opens an empty, migrated store for the setters below.
//
// It is deliberately not the package's shared newStore helper: the closed-store
// cases shut the database down themselves, and newStore's cleanup reports that
// second close as a failure.
func openHandleStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.Open(context.Background(), storage.Options{Memory: true})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil && !errors.Is(err, storage.ErrClosed) {
			t.Errorf("close store: %v", err)
		}
	})
	if _, err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

// seedHandleSession records the session the setters update.
func seedHandleSession(t *testing.T, store *storage.Store) {
	t.Helper()
	if _, err := store.UpsertSession(context.Background(), storage.Session{
		ID:          jobSession,
		ProjectPath: "/work/project",
		TranscriptPath: "/home/u/.claude/projects/-work-project/" +
			jobSession + ".jsonl",
		Type:    storage.SessionTypeInteractive,
		State:   storage.StateWorking,
		Enabled: true,
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
}

// mustReadSession reads the row back, failing the test if it cannot.
func mustReadSession(t *testing.T, store *storage.Store) storage.Session {
	t.Helper()
	session, err := store.GetSession(context.Background(), jobSession)
	if err != nil {
		t.Fatalf("read back session: %v", err)
	}
	return session
}

// TestSetSessionJobRecordsAndClearsTheHandle covers the column the follow stage
// polls: the job id a dispatch created must survive a daemon restart, and an
// empty id must clear it rather than leave a handle nothing can resolve.
func TestSetSessionJobRecordsAndClearsTheHandle(t *testing.T) {
	store := openHandleStore(t)
	seedHandleSession(t, store)
	ctx := context.Background()

	// The value is trimmed, because it comes from a CLI banner whose whitespace
	// is not part of the name `claude agents --json` resolves.
	if err := store.SetSessionJob(ctx, jobSession, "  9f3a1b2c  "); err != nil {
		t.Fatalf("SetSessionJob: %v", err)
	}
	if stored := mustReadSession(t, store); stored.JobID != "9f3a1b2c" {
		t.Errorf("JobID = %q, want %q", stored.JobID, "9f3a1b2c")
	}

	// Replacing the handle is normal: a second cycle dispatches a second job, and
	// the row must poll the newest one.
	if err := store.SetSessionJob(ctx, jobSession, "deadbeef"); err != nil {
		t.Fatalf("SetSessionJob (replace): %v", err)
	}
	if stored := mustReadSession(t, store); stored.JobID != "deadbeef" {
		t.Errorf("JobID = %q, want the newest dispatch's job", stored.JobID)
	}

	// An empty id is an instruction to forget the handle, not an error: a WORKING
	// row with no job must be closable rather than stuck naming a job that never
	// existed.
	if err := store.SetSessionJob(ctx, jobSession, "   "); err != nil {
		t.Fatalf("SetSessionJob (clear): %v", err)
	}
	if stored := mustReadSession(t, store); stored.JobID != "" {
		t.Errorf("JobID = %q, want it cleared", stored.JobID)
	}

	// An unknown session is reported, so a caller cannot believe it recorded a
	// job on a row that does not exist.
	if err := store.SetSessionJob(ctx, "99999999-9999-4999-8999-999999999999", "9f3a1b2c"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("unknown session = %v, want ErrNotFound", err)
	}

	// A closed store reports that too, instead of silently dropping the write.
	closed := openHandleStore(t)
	if err := closed.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := closed.SetSessionJob(ctx, jobSession, "9f3a1b2c"); !errors.Is(err, storage.ErrClosed) {
		t.Errorf("closed store = %v, want ErrClosed", err)
	}
}

// TestSetSessionNameRecordsAndForgetsTheName covers the display half: the name
// `claude agents --all --json` reports is what `ccresu sessions` shows, and it
// must be forgettable so a stale name does not describe a conversation that has
// moved on.
func TestSetSessionNameRecordsAndForgetsTheName(t *testing.T) {
	store := openHandleStore(t)
	seedHandleSession(t, store)
	ctx := context.Background()

	if err := store.SetSessionName(ctx, jobSession, "  nightly-audit  "); err != nil {
		t.Fatalf("SetSessionName: %v", err)
	}
	if stored := mustReadSession(t, store); stored.Name != "nightly-audit" {
		t.Errorf("Name = %q, want %q", stored.Name, "nightly-audit")
	}

	// A renamed job reports a new name, and the newest reading wins.
	if err := store.SetSessionName(ctx, jobSession, "nightly-audit-2"); err != nil {
		t.Fatalf("SetSessionName (rename): %v", err)
	}
	if stored := mustReadSession(t, store); stored.Name != "nightly-audit-2" {
		t.Errorf("Name = %q, want the newest name", stored.Name)
	}

	if err := store.SetSessionName(ctx, jobSession, ""); err != nil {
		t.Fatalf("SetSessionName (clear): %v", err)
	}
	if stored := mustReadSession(t, store); stored.Name != "" {
		t.Errorf("Name = %q, want it forgotten", stored.Name)
	}

	if err := store.SetSessionName(ctx, "99999999-9999-4999-8999-999999999999", "ghost"); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("unknown session = %v, want ErrNotFound", err)
	}

	closed := openHandleStore(t)
	if err := closed.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := closed.SetSessionName(ctx, jobSession, "nightly-audit"); !errors.Is(err, storage.ErrClosed) {
		t.Errorf("closed store = %v, want ErrClosed", err)
	}
}
