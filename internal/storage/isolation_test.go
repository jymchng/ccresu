package storage_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jymchng/ccresu/internal/storage"
)

// TestVerifyWALFilesExistOnDisk proves the database really is in WAL mode: the
// -wal sidecar only exists while a WAL-mode database is open and writable.
func TestVerifyWALFilesExistOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wal.db")
	store := openAt(t, path)
	defer store.Close()
	if _, err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	mustSeedSession(t, store, "wal-session")

	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("no -wal sidecar: %v (journal_mode is probably not WAL)", err)
	}

	var mode string
	if err := store.DB().QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	// And it survives a clean close/reopen, because WAL is recorded in the file.
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopened := openAt(t, path)
	defer reopened.Close()
	if err := reopened.DB().QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode after reopen = %q, want wal", mode)
	}
}

// TestVerifyForeignKeysEnforcedBySQLite inserts an orphan row with raw SQL, so
// the check is SQLite's own constraint rather than the repository's Go-side
// validation.
func TestVerifyForeignKeysEnforcedBySQLite(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	_, err := store.DB().ExecContext(ctx,
		`INSERT INTO usage_limits (session_id, limit_type, detected_at) VALUES ('ghost', 'other', 1)`)
	if err == nil {
		t.Fatal("SQLite accepted an orphan usage_limits row: foreign_keys is not enforced")
	}
	if !contains(err.Error(), "FOREIGN KEY") {
		t.Fatalf("error is not a foreign-key violation: %v", err)
	}

	// The same for a transition pointing at a missing session.
	if _, err := store.DB().ExecContext(ctx,
		`INSERT INTO state_transitions (session_id, from_state, to_state, occurred_at) VALUES ('ghost', 'RUNNING', 'RESUMED', 1)`); err == nil {
		t.Fatal("SQLite accepted an orphan state_transitions row")
	}

	// And the declared uniqueness is a real index, not just a comment.
	session := mustSeedSession(t, store, "unique-session")
	if _, err := store.DB().ExecContext(ctx,
		`INSERT INTO recovery_attempts (session_id, attempt_number, state, started_at) VALUES (?, 1, 'RESUMING', 1)`,
		session.ID); err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx,
		`INSERT INTO recovery_attempts (session_id, attempt_number, state, started_at) VALUES (?, 1, 'RESUMING', 1)`,
		session.ID); err == nil {
		t.Fatal("duplicate (session_id, attempt_number) was accepted")
	}

	// A notification needs a unique dedupe key.
	if _, err := store.DB().ExecContext(ctx,
		`INSERT INTO notifications (event, dedupe_key, created_at, updated_at) VALUES ('waiting', 'k', 1, 1)`); err != nil {
		t.Fatalf("first notification: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx,
		`INSERT INTO notifications (event, dedupe_key, created_at, updated_at) VALUES ('waiting', 'k', 1, 1)`); err == nil {
		t.Fatal("duplicate dedupe_key was accepted")
	}
}

// TestVerifyTwoStoresShareOneDatabase is the daemon-plus-CLI case: two separate
// Store instances (so two connection pools, like two processes) on one file must
// both be able to write without "database is locked".
func TestVerifyTwoStoresShareOneDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	ctx := context.Background()

	daemon := openAt(t, path)
	defer daemon.Close()
	if _, err := daemon.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cli := openAt(t, path)
	defer cli.Close()
	if applied, err := cli.Migrate(ctx); err != nil {
		t.Fatalf("cli migrate: %v", err)
	} else if len(applied) != 0 {
		t.Fatalf("cli re-applied migrations: %v", applied)
	}

	mustSeedSession(t, daemon, "shared-session")

	// Writes from both pools, interleaved, must all land.
	const rounds = 15
	var wg sync.WaitGroup
	errs := make(chan error, 2*rounds)
	for _, store := range []*storage.Store{daemon, cli} {
		wg.Add(1)
		go func(store *storage.Store) {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				if _, err := store.RecordUsageLimit(ctx, storage.UsageLimit{
					SessionID: "shared-session", LimitType: storage.LimitTypeOther,
					RawMessage: fmt.Sprintf("round %d", i), Confidence: 0.5,
				}); err != nil {
					errs <- err
					return
				}
				if _, _, err := store.EnqueueNotification(ctx, storage.Notification{
					Event: storage.EventWaiting, DedupeKey: fmt.Sprintf("shared-%d-%p", i, store),
					Body: "waiting",
				}); err != nil {
					errs <- err
					return
				}
			}
		}(store)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("cross-store write failed: %v", err)
	}

	limits, err := daemon.ListUsageLimits(ctx, "shared-session", 0)
	if err != nil {
		t.Fatalf("list limits: %v", err)
	}
	if len(limits) != 2*rounds {
		t.Fatalf("recorded %d limits across two stores, want %d", len(limits), 2*rounds)
	}

	// A write through one pool must be immediately visible through the other.
	if err := cli.SetSetting(ctx, "written_by_cli", "yes"); err != nil {
		t.Fatalf("cli set setting: %v", err)
	}
	value, ok, err := daemon.GetSetting(ctx, "written_by_cli")
	if err != nil || !ok || value != "yes" {
		t.Fatalf("daemon cannot see cli write: %q ok=%t err=%v", value, ok, err)
	}

	// Attempt numbering must stay unique across pools too.
	attemptA, err := daemon.StartAttempt(ctx, "shared-session", 0, "a")
	if err != nil {
		t.Fatalf("daemon attempt: %v", err)
	}
	attemptB, err := cli.StartAttempt(ctx, "shared-session", 0, "b")
	if err != nil {
		t.Fatalf("cli attempt: %v", err)
	}
	if attemptA.AttemptNumber == attemptB.AttemptNumber {
		t.Fatalf("both stores allocated attempt number %d", attemptA.AttemptNumber)
	}
}

// TestVerifyBusyTimeoutIsHonoured proves a writer waits for a lock instead of
// failing instantly: a second connection holds a write transaction, and the
// store's write must block then succeed within the busy timeout.
func TestVerifyBusyTimeoutIsHonoured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")

	store, err := storage.Open(context.Background(), storage.Options{
		Path: path, BusyTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	if _, err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// A separate raw connection holds the write lock for 300ms.
	blocker, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(3000)&_txlock=immediate")
	if err != nil {
		t.Fatalf("open blocker: %v", err)
	}
	defer blocker.Close()
	tx, err := blocker.Begin()
	if err != nil {
		t.Fatalf("blocker begin: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO settings (key, value, updated_at) VALUES ('blocker', 'x', 1)`); err != nil {
		t.Fatalf("blocker insert: %v", err)
	}

	released := make(chan struct{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = tx.Commit()
		close(released)
	}()

	start := time.Now()
	if err := store.SetSetting(context.Background(), "writer", "y"); err != nil {
		t.Fatalf("store write failed instead of waiting: %v", err)
	}
	waited := time.Since(start)
	<-released
	if waited < 200*time.Millisecond {
		t.Fatalf("write returned after %s, so it did not wait for the lock", waited)
	}
	if value, _, err := store.GetSetting(context.Background(), "writer"); err != nil || value != "y" {
		t.Fatalf("writer setting not stored: %q %v", value, err)
	}
}

// TestVerifyRollbackRestoresExactSchema proves a rollback is the true inverse of
// the migrations, not just "the table is gone".
func TestVerifyRollbackRestoresExactSchema(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{Path: filepath.Join(t.TempDir(), "schema.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	columnsOf := func(table string) []string {
		t.Helper()
		rows, err := store.DB().QueryContext(ctx, "PRAGMA table_info("+table+")")
		if err != nil {
			t.Fatalf("table_info %s: %v", table, err)
		}
		defer rows.Close()
		var names []string
		for rows.Next() {
			var (
				cid, notNull, pk int
				name, ctype      string
				defaultValue     any
			)
			if err := rows.Scan(&cid, &name, &ctype, &notNull, &defaultValue, &pk); err != nil {
				t.Fatal(err)
			}
			names = append(names, name)
		}
		return names
	}

	// Migrate to version 1 only, snapshot, then migrate fully and roll back.
	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	latest, err := store.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Rollback(ctx, 1); err != nil {
		t.Fatalf("rollback to 1: %v", err)
	}
	v1Columns := columnsOf("sessions")
	if len(v1Columns) == 0 {
		t.Fatal("sessions table missing at version 1")
	}

	if _, err := store.Migrate(ctx); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	fullColumns := columnsOf("sessions")
	if len(fullColumns) <= len(v1Columns) {
		t.Fatalf("migration added no columns: v1=%v latest=%v", v1Columns, fullColumns)
	}

	if _, err := store.Rollback(ctx, 1); err != nil {
		t.Fatalf("rollback to 1 again: %v", err)
	}
	if got := columnsOf("sessions"); fmt.Sprint(got) != fmt.Sprint(v1Columns) {
		t.Fatalf("rollback did not restore the version-1 schema:\n got %v\nwant %v", got, v1Columns)
	}
	if version, err := store.Version(ctx); err != nil || version != 1 {
		t.Fatalf("Version after rollback = %d, %v (latest was %d)", version, err, latest)
	}
}

// TestVerifyReopenSurvivesDirtyShutdown proves an unclosed store's committed data
// survives, which is what "restart/reboot resume" depends on.
func TestVerifyReopenSurvivesDirtyShutdown(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dirty.db")

	first := openAt(t, path)
	if _, err := first.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := first.UpsertSession(ctx, storage.Session{
		ID: "dirty-session", Enabled: true, Type: storage.SessionTypeInteractive,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := first.TransitionSession(ctx, "dirty-session", storage.StateWaitingForReset, "limit detected", 0); err != nil {
		t.Fatalf("transition: %v", err)
	}
	if _, err := first.RecordUsageLimit(ctx, storage.UsageLimit{
		SessionID: "dirty-session", LimitType: storage.LimitTypeFiveHour, Confidence: 0.9,
	}); err != nil {
		t.Fatalf("record limit: %v", err)
	}
	// Deliberately do NOT Close: simulate a kill.

	second := openAt(t, path)
	defer second.Close()
	session, err := second.GetSession(ctx, "dirty-session")
	if err != nil {
		t.Fatalf("session lost after dirty shutdown: %v", err)
	}
	if session.State != storage.StateWaitingForReset {
		t.Fatalf("state = %s, want WAITING_FOR_RESET", session.State)
	}
	if _, err := second.LatestUsageLimit(ctx, "dirty-session"); err != nil {
		t.Fatalf("usage limit lost after dirty shutdown: %v", err)
	}
	if _, err := second.Migrate(ctx); err != nil {
		t.Fatalf("migrate after dirty shutdown: %v", err)
	}
}

// TestVerifyStoreIsUsableFromManyGoroutinesWithoutCorruption hammers every
// repository at once; a partial write or lost migration would surface here.
func TestVerifyStoreIsUsableFromManyGoroutinesWithoutCorruption(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	run := func(fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil {
				errs <- err
			}
		}()
	}

	for i := 0; i < 6; i++ {
		i := i
		run(func() error {
			_, err := store.UpsertSession(ctx, storage.Session{
				ID: fmt.Sprintf("multi-%d", i), Enabled: true, ProjectPath: "/p",
			})
			return err
		})
	}
	// Seed one session so dependent writers have a parent.
	mustSeedSession(t, store, "multi-parent")
	for i := 0; i < 8; i++ {
		i := i
		run(func() error {
			if _, err := store.RecordUsageLimit(ctx, storage.UsageLimit{
				SessionID: "multi-parent", LimitType: storage.LimitTypeOther,
				RawMessage: fmt.Sprintf("msg %d", i),
			}); err != nil {
				return err
			}
			if _, err := store.TransitionSession(ctx, "multi-parent", storage.StateRunning, "step", 0); err != nil {
				return err
			}
			if _, _, err := store.EnqueueNotification(ctx, storage.Notification{
				SessionID: "multi-parent", Event: storage.EventWaiting,
				DedupeKey: fmt.Sprintf("multi-%d", i), Body: "waiting",
			}); err != nil {
				return err
			}
			if err := store.SetSetting(ctx, fmt.Sprintf("key-%d", i), "v"); err != nil {
				return err
			}
			_, err := store.ListSessions(ctx, storage.SessionFilter{})
			return err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent repository use failed: %v", err)
	}

	sessions, err := store.ListSessions(ctx, storage.SessionFilter{})
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 7 {
		t.Fatalf("stored %d sessions, want 7", len(sessions))
	}
	limits, err := store.ListUsageLimits(ctx, "multi-parent", 0)
	if err != nil || len(limits) != 8 {
		t.Fatalf("stored %d limits, %v", len(limits), err)
	}
	settings, err := store.ListSettings(ctx)
	if err != nil || len(settings) != 8 {
		t.Fatalf("stored %d settings, %v", len(settings), err)
	}
	// The database is still sound after all that.
	var integrity string
	if err := store.DB().QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity_check = %q", integrity)
	}
}

// TestVerifyInvalidPathIsRejected keeps a misconfigured storage path from
// silently creating a database somewhere unexpected.
func TestVerifyInvalidPathIsRejected(t *testing.T) {
	ctx := context.Background()
	if _, err := storage.Open(ctx, storage.Options{Path: ""}); err == nil {
		t.Fatal("empty path accepted")
	}
	if _, err := storage.Open(ctx, storage.Options{Path: "/proc/nope/sub/db.sqlite"}); err == nil {
		t.Fatal("unwritable path accepted")
	}
	if _, err := storage.Open(ctx, storage.Options{Path: "/dev/null/db.sqlite"}); err == nil {
		t.Fatal("path below a file accepted")
	}
	if _, err := storage.Open(ctx, storage.Options{Path: "\x00bad"}); err == nil {
		t.Fatal("NUL byte in path accepted")
	}
	// A relative path is made absolute, so a later chdir cannot move the database.
	rel := "verify-relative.db"
	store, err := storage.Open(ctx, storage.Options{Path: rel})
	if err != nil {
		t.Fatalf("relative path: %v", err)
	}
	defer store.Close()
	if !filepath.IsAbs(store.Path()) {
		t.Fatalf("Path() = %q, want absolute", store.Path())
	}
	if _, err := os.Stat(store.Path()); err != nil {
		t.Fatalf("database not created at %q: %v", store.Path(), err)
	}
	if err := os.Remove(store.Path()); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup: %v", err)
	}
	_ = os.Remove(store.Path() + "-wal")
	_ = os.Remove(store.Path() + "-shm")
}
