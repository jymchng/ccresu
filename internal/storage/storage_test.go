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

// fixedTime is the deterministic clock used by every test.
var fixedTime = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// newStore opens a migrated store in a temporary directory.
func newStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.Open(context.Background(), storage.Options{
		Path:  filepath.Join(t.TempDir(), "ccresu.db"),
		Clock: func() time.Time { return fixedTime },
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	if _, err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}

// openAt opens a store at an explicit path (for reopen tests).
func openAt(t *testing.T, path string) *storage.Store {
	t.Helper()
	store, err := storage.Open(context.Background(), storage.Options{Path: path})
	if err != nil {
		t.Fatalf("open store at %s: %v", path, err)
	}
	return store
}

// mustSeedSession inserts a session that dependent rows can reference.
func mustSeedSession(t *testing.T, store *storage.Store, id string) storage.Session {
	t.Helper()
	created, err := store.UpsertSession(context.Background(), storage.Session{
		ID:               id,
		ProjectPath:      "/work/project",
		TranscriptPath:   "/home/u/.claude/projects/-work-project/" + id + ".jsonl",
		Type:             storage.SessionTypeInteractive,
		Enabled:          true,
		Recoverable:      true,
		ProcessStartTime: 424242,
		PID:              1234,
		PPID:             1,
	})
	if err != nil {
		t.Fatalf("seed session %s: %v", id, err)
	}
	if !created {
		t.Fatalf("seed session %s: expected created=true", id)
	}
	session, err := store.GetSession(context.Background(), id)
	if err != nil {
		t.Fatalf("read seeded session %s: %v", id, err)
	}
	return session
}

func TestOpenCreatesRestrictedFiles(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "deep", "deeper")
	path := filepath.Join(nested, "ccresu.db")

	store, err := storage.Open(context.Background(), storage.Options{Path: path})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat db: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("database permissions are %o, want 600", perm)
	}
	dirInfo, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("database directory permissions are %o, want 700", perm)
	}
	if store.Path() != path {
		t.Fatalf("Path() = %q, want %q", store.Path(), path)
	}

	for _, pragma := range []string{"journal_mode", "foreign_keys", "busy_timeout"} {
		var value string
		if err := store.DB().QueryRow("PRAGMA " + pragma).Scan(&value); err != nil {
			t.Fatalf("read %s: %v", pragma, err)
		}
		if value == "" {
			t.Fatalf("%s is empty", pragma)
		}
	}
	var journalMode string
	if err := store.DB().QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}
}

func TestOpenRejectsBadOptions(t *testing.T) {
	for name, opts := range map[string]storage.Options{
		"empty path": {},
		"bad parent": {Path: "/proc/ccresu-forbidden/db.sqlite"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := storage.Open(context.Background(), opts); err == nil {
				t.Fatal("expected an error")
			}
		})
	}

	if _, err := storage.Open(context.Background(), storage.Options{Memory: true}); err != nil {
		t.Fatalf("memory store: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := storage.Open(ctx, storage.Options{Path: filepath.Join(t.TempDir(), "x.db")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestMemoryStoreMigrates(t *testing.T) {
	store, err := storage.Open(context.Background(), storage.Options{Memory: true})
	if err != nil {
		t.Fatalf("open memory: %v", err)
	}
	defer store.Close()
	if _, err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate memory: %v", err)
	}
	mustSeedSession(t, store, "memory-session")
	version, err := store.Version(context.Background())
	if err != nil || version == 0 {
		t.Fatalf("Version() = %d, %v", version, err)
	}
}

func TestClosedStoreRejectsEverything(t *testing.T) {
	store, err := storage.Open(context.Background(), storage.Options{Memory: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	ctx := context.Background()
	if err := store.Close(); !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("double close = %v, want ErrClosed", err)
	}
	if _, err := store.Migrate(ctx); !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("migrate after close = %v", err)
	}
	if _, err := store.UpsertSession(ctx, storage.Session{ID: "x"}); !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("upsert after close = %v", err)
	}
	if err := store.InTx(ctx, func(*sql.Tx) error { return nil }); err != nil {
		// InTx takes *sql.Tx; the closed check must fire first.
		if !errors.Is(err, storage.ErrClosed) {
			t.Fatalf("InTx after close = %v", err)
		}
	}
	var nilStore *storage.Store
	if _, err := nilStore.GetSession(ctx, "x"); !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("nil store GetSession = %v", err)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()

	version, err := store.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version < 2 {
		t.Fatalf("expected at least 2 migrations, got %d", version)
	}

	again, err := store.Migrate(ctx)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second migrate applied %v, want nothing", again)
	}

	statuses, err := store.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(statuses) != version {
		t.Fatalf("Status returned %d entries, want %d", len(statuses), version)
	}
	for _, status := range statuses {
		if !status.Applied {
			t.Fatalf("migration %d reported unapplied", status.Version)
		}
		if status.AppliedAt == 0 {
			t.Fatalf("migration %d has no applied_at", status.Version)
		}
		if status.Name == "" {
			t.Fatalf("migration %d has no name", status.Version)
		}
	}
}

func TestMigrateFromEmptyAndRollback(t *testing.T) {
	ctx := context.Background()
	store, err := storage.Open(ctx, storage.Options{Path: filepath.Join(t.TempDir(), "ccresu.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if version, err := store.Version(ctx); err != nil || version != 0 {
		t.Fatalf("Version on empty database = %d, %v", version, err)
	}

	applied, err := store.Migrate(ctx)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(applied) == 0 {
		t.Fatal("migrate applied nothing")
	}
	latest := applied[len(applied)-1]

	tables := func() map[string]bool {
		t.Helper()
		rows, err := store.DB().QueryContext(ctx,
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`)
		if err != nil {
			t.Fatalf("list tables: %v", err)
		}
		defer rows.Close()
		found := map[string]bool{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			found[name] = true
		}
		return found
	}

	for _, want := range []string{"sessions", "usage_limits", "recovery_attempts", "state_transitions", "notifications", "settings", "schedules", "schema_migrations", "deleted_sessions"} {
		if !tables()[want] {
			t.Fatalf("table %q missing after migrate: %v", want, tables())
		}
	}

	// Every migration's schema effect is present now.
	if !sessionColumns(ctx, t, store)["retry_at"] {
		t.Fatalf("next_attempt_at missing after migrate: %v", sessionColumns(ctx, t, store))
	}

	// Roll back the newest version. It is a data migration (it re-opens
	// PERMANENTLY_FAILED sessions), so no table changes; the point of asserting
	// it here is that the runner reports it and its rollback succeeds.
	reverted, err := store.Rollback(ctx, latest-1)
	if err != nil {
		t.Fatalf("rollback to %d: %v", latest-1, err)
	}
	if len(reverted) != 1 || reverted[0] != latest {
		t.Fatalf("rollback returned %v, want [%d]", reverted, latest)
	}
	if version, err := store.Version(ctx); err != nil || version != latest-1 {
		t.Fatalf("Version after rollback = %d, %v", version, err)
	}
	if !tables()["deleted_sessions"] {
		t.Fatalf("a data-only rollback must not drop deleted_sessions: %v", tables())
	}

	// Roll back the delete tombstones: the table goes with them.
	if _, err := store.Rollback(ctx, 4); err != nil {
		t.Fatalf("rollback to 4: %v", err)
	}
	if tables()["deleted_sessions"] {
		t.Fatalf("deleted_sessions survived its rollback: %v", tables())
	}

	// Roll back the retry instant: its column disappears and the tombstones that
	// the previous step already removed stay removed.
	if _, err := store.Rollback(ctx, 3); err != nil {
		t.Fatalf("rollback to 3: %v", err)
	}
	if sessionColumns(ctx, t, store)["retry_at"] {
		t.Fatalf("next_attempt_at survived its rollback: %v", sessionColumns(ctx, t, store))
	}

	// Roll back the schedules migration: its table goes with it.
	if _, err := store.Rollback(ctx, 2); err != nil {
		t.Fatalf("rollback to 2: %v", err)
	}
	if tables()["schedules"] {
		t.Fatalf("schedules survived rollback: %v", tables())
	}

	// Roll back the transcript-metrics migration too: its columns disappear.
	if _, err := store.Rollback(ctx, 1); err != nil {
		t.Fatalf("rollback to 1: %v", err)
	}
	columns := sessionColumns(ctx, t, store)
	if columns["transcript_size"] {
		t.Fatalf("transcript_size survived rollback: %v", columns)
	}

	// Roll back to zero: every table except the bookkeeping table is gone.
	remainingVersion, err := store.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	reverted, err = store.Rollback(ctx, 0)
	if err != nil {
		t.Fatalf("rollback to 0: %v", err)
	}
	if len(reverted) != remainingVersion {
		t.Fatalf("rollback to 0 reverted %v, want %d versions", reverted, remainingVersion)
	}
	remaining := tables()
	for _, gone := range []string{"sessions", "usage_limits", "recovery_attempts", "state_transitions", "notifications", "settings", "schedules", "deleted_sessions"} {
		if remaining[gone] {
			t.Fatalf("table %q survived full rollback: %v", gone, remaining)
		}
	}

	// And forward again: migrations must be replayable after a full rollback.
	reapplied, err := store.Migrate(ctx)
	if err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	if len(reapplied) != latest {
		t.Fatalf("re-migrate applied %v, want %d versions", reapplied, latest)
	}
	if !sessionColumns(ctx, t, store)["transcript_size"] {
		t.Fatal("transcript_size missing after re-migrate")
	}
}

// sessionColumns reports the columns of the sessions table.
func sessionColumns(ctx context.Context, t *testing.T, store *storage.Store) map[string]bool {
	t.Helper()
	rows, err := store.DB().QueryContext(ctx, `PRAGMA table_info(sessions)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var (
			cid      int
			name     string
			ctype    string
			notNull  int
			defaultV any
			primaryK int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &defaultV, &primaryK); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		columns[name] = true
	}
	return columns
}

func TestRollbackRejectsInvalidTargets(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	if _, err := store.Rollback(ctx, -1); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("negative target = %v", err)
	}
	if _, err := store.Rollback(ctx, 99); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("target beyond latest = %v", err)
	}
	latest, err := store.Version(ctx)
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if _, err := store.Rollback(ctx, latest+1); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("roll forward = %v", err)
	}
	var nilStore *storage.Store
	if _, err := nilStore.Rollback(ctx, 0); !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("nil store rollback = %v", err)
	}
	if _, err := nilStore.Migrate(ctx); !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("nil store migrate = %v", err)
	}
	if _, err := nilStore.Version(ctx); !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("nil store version = %v", err)
	}
	if _, err := nilStore.Status(ctx); !errors.Is(err, storage.ErrClosed) {
		t.Fatalf("nil store status = %v", err)
	}
}

// TestTamperedMigrationIsRejected proves an edited, already-applied migration is
// detected instead of silently diverging from the database.
func TestTamperedMigrationIsRejected(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	if _, err := store.DB().ExecContext(ctx,
		`UPDATE schema_migrations SET checksum = 'deadbeef' WHERE version = 1`); err != nil {
		t.Fatalf("tamper checksum: %v", err)
	}
	if _, err := store.Migrate(ctx); err == nil {
		t.Fatal("migrate accepted a tampered checksum")
	} else if !contains(err.Error(), "checksum") {
		t.Fatalf("error does not mention the checksum: %v", err)
	}

	// A database migrated by a newer binary (unknown version) is refused.
	store2 := newStore(t)
	if _, err := store2.DB().ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (99, 'future', 'x', 0)`); err != nil {
		t.Fatalf("insert future migration: %v", err)
	}
	if _, err := store2.Migrate(ctx); err == nil {
		t.Fatal("migrate accepted an unknown applied version")
	} else if !contains(err.Error(), "99") {
		t.Fatalf("error does not name the unknown version: %v", err)
	}
}

func TestForeignKeysCascade(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	session := mustSeedSession(t, store, "cascade-session")

	limit, err := store.RecordUsageLimit(ctx, storage.UsageLimit{
		SessionID: session.ID, LimitType: storage.LimitTypeFiveHour, Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("record limit: %v", err)
	}
	attempt, err := store.StartAttempt(ctx, session.ID, limit.ID, "claude -p --resume "+session.ID+" continue")
	if err != nil {
		t.Fatalf("start attempt: %v", err)
	}
	if _, created, err := store.EnqueueNotification(ctx, storage.Notification{
		SessionID: session.ID, AttemptID: attempt.ID, Event: storage.EventRecoveryStarted,
		DedupeKey: "cascade-1", Body: "hi",
	}); err != nil || !created {
		t.Fatalf("enqueue: created=%t err=%v", created, err)
	}

	// A child row with a bogus parent must be rejected outright.
	if _, err := store.RecordUsageLimit(ctx, storage.UsageLimit{SessionID: "ghost"}); err == nil {
		t.Fatal("usage limit for a nonexistent session was accepted")
	}

	if err := store.DeleteSession(ctx, session.ID); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	for name, count := range map[string]string{
		"usage_limits":      `SELECT COUNT(*) FROM usage_limits`,
		"recovery_attempts": `SELECT COUNT(*) FROM recovery_attempts`,
		"state_transitions": `SELECT COUNT(*) FROM state_transitions`,
		"notifications":     `SELECT COUNT(*) FROM notifications`,
	} {
		var n int
		if err := store.DB().QueryRowContext(ctx, count).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if n != 0 {
			t.Fatalf("%s still has %d rows after cascade delete", name, n)
		}
	}
}

// TestPersistenceAcrossReopen is the restart guarantee: everything written by one
// process is readable by a fresh connection.
func TestPersistenceAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "persist.db")

	first := openAt(t, path)
	if _, err := first.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	session := mustSeedSession(t, first, "persist-session")
	limit, err := first.RecordUsageLimit(ctx, storage.UsageLimit{
		SessionID:     session.ID,
		LimitType:     storage.LimitTypeFiveHour,
		RawMessage:    "You've hit your 5-hour limit · resets 12pm (Europe/Berlin)",
		ResetAt:       time.Date(2026, 3, 4, 11, 0, 0, 0, time.UTC),
		ResetTimezone: "Europe/Berlin",
		ResetRaw:      "12pm (Europe/Berlin)",
		Confidence:    0.95,
		PausedGoal:    true,
	})
	if err != nil {
		t.Fatalf("record limit: %v", err)
	}
	attempt, err := first.StartAttempt(ctx, session.ID, limit.ID, "resume-command")
	if err != nil {
		t.Fatalf("start attempt: %v", err)
	}
	if _, err := first.TransitionSession(ctx, session.ID, storage.StateUsageLimitDetected, "detected", attempt.ID); err != nil {
		t.Fatalf("transition: %v", err)
	}
	yes := true
	if _, err := first.FinishAttempt(ctx, attempt.ID, storage.AttemptResult{
		State: storage.StateResumed, Success: true, Output: "done",
		SessionIDVerified: &yes, TranscriptGrew: &yes,
	}); err != nil {
		t.Fatalf("finish attempt: %v", err)
	}
	if _, _, err := first.EnqueueNotification(ctx, storage.Notification{
		SessionID: session.ID, AttemptID: attempt.ID, Event: storage.EventRecoverySuccess,
		DedupeKey: "persist-notify", Body: "<b>resumed</b>",
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := first.SetSetting(ctx, "global_enabled", "true"); err != nil {
		t.Fatalf("set setting: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	second := openAt(t, path)
	defer second.Close()

	// No migration is needed when reopening: the schema is already current.
	if applied, err := second.Migrate(ctx); err != nil {
		t.Fatalf("reopen migrate: %v", err)
	} else if len(applied) != 0 {
		t.Fatalf("reopen applied migrations: %v", applied)
	}

	got, err := second.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("get session after reopen: %v", err)
	}
	if got.State != storage.StateUsageLimitDetected {
		t.Fatalf("state = %s, want USAGE_LIMIT_DETECTED", got.State)
	}
	if got.ProcessStartTime != 424242 || got.PID != 1234 || got.PPID != 1 {
		t.Fatalf("identity lost: %+v", got)
	}
	if !got.DiscoveredAt.Equal(session.DiscoveredAt) {
		t.Fatalf("discovered_at = %s, want %s", got.DiscoveredAt, session.DiscoveredAt)
	}
	if !got.LastSeenAt.Equal(session.LastSeenAt) {
		t.Fatalf("last_seen_at = %s, want %s", got.LastSeenAt, session.LastSeenAt)
	}

	limitAfter, err := second.LatestUsageLimit(ctx, session.ID)
	if err != nil {
		t.Fatalf("latest limit after reopen: %v", err)
	}
	if limitAfter.ID != limit.ID || limitAfter.ResetTimezone != "Europe/Berlin" ||
		!limitAfter.ResetAt.Equal(limit.ResetAt) || !limitAfter.PausedGoal {
		t.Fatalf("usage limit not preserved: %+v", limitAfter)
	}

	attempts, err := second.ListAttempts(ctx, session.ID, 0)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts after reopen = %d, %v", len(attempts), err)
	}
	if attempts[0].Success == nil || !*attempts[0].Success {
		t.Fatalf("attempt success lost: %+v", attempts[0])
	}
	if attempts[0].SessionIDVerified == nil || !*attempts[0].SessionIDVerified {
		t.Fatalf("verification flags lost: %+v", attempts[0])
	}
	if attempts[0].TranscriptGrew == nil || !*attempts[0].TranscriptGrew {
		t.Fatalf("transcript_grew lost: %+v", attempts[0])
	}

	transitions, err := second.ListTransitions(ctx, session.ID, 0)
	if err != nil || len(transitions) != 1 {
		t.Fatalf("transitions after reopen = %d, %v", len(transitions), err)
	}

	notifications, err := second.ListNotificationsForSession(ctx, session.ID, 0)
	if err != nil || len(notifications) != 1 {
		t.Fatalf("notifications after reopen = %d, %v", len(notifications), err)
	}
	if notifications[0].Status != storage.NotificationPending || notifications[0].Body != "<b>resumed</b>" {
		t.Fatalf("notification not preserved: %+v", notifications[0])
	}

	if value, ok, err := second.GetSetting(ctx, "global_enabled"); err != nil || !ok || value != "true" {
		t.Fatalf("setting after reopen = %q ok=%t err=%v", value, ok, err)
	}
}

// TestConcurrentWriters proves the pool, WAL mode and busy timeout actually
// support concurrent use without "database is locked" failures.
func TestConcurrentWriters(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	session := mustSeedSession(t, store, "concurrent-session")

	const writers = 8
	const perWriter = 10
	errs := make(chan error, writers*perWriter)
	done := make(chan struct{})

	for w := 0; w < writers; w++ {
		go func(w int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < perWriter; i++ {
				_, err := store.RecordUsageLimit(ctx, storage.UsageLimit{
					SessionID:  session.ID,
					LimitType:  storage.LimitTypeOther,
					RawMessage: fmt.Sprintf("limit from worker %d item %d", w, i),
					Confidence: 0.5,
				})
				if err != nil {
					errs <- fmt.Errorf("worker %d record limit: %w", w, err)
					return
				}
				if _, err := store.ListSessions(ctx, storage.SessionFilter{NewestFirst: true, Limit: 5}); err != nil {
					errs <- fmt.Errorf("worker %d list: %w", w, err)
					return
				}
			}
		}(w)
	}
	for i := 0; i < writers; i++ {
		<-done
	}
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent write failed: %v", err)
	}

	limits, err := store.ListUsageLimits(ctx, session.ID, 0)
	if err != nil {
		t.Fatalf("list limits: %v", err)
	}
	if len(limits) != writers*perWriter {
		t.Fatalf("recorded %d limits, want %d", len(limits), writers*perWriter)
	}
}

// TestConcurrentAttemptNumbering proves the unique index plus retry loop can
// never hand out the same attempt number twice.
func TestConcurrentAttemptNumbering(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	session := mustSeedSession(t, store, "attempt-race")

	const goroutines = 12
	results := make(chan int, goroutines)
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			attempt, err := store.StartAttempt(ctx, session.ID, 0, "resume")
			if err != nil {
				errs <- err
				return
			}
			results <- attempt.AttemptNumber
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatalf("start attempt race: %v", err)
	}

	seen := map[int]bool{}
	for number := range results {
		if number < 1 {
			t.Fatalf("attempt number %d is invalid", number)
		}
		if seen[number] {
			t.Fatalf("attempt number %d handed out twice", number)
		}
		seen[number] = true
	}
	if len(seen) != goroutines {
		t.Fatalf("got %d distinct attempt numbers, want %d", len(seen), goroutines)
	}
	attempts, err := store.ListAttempts(ctx, session.ID, 0)
	if err != nil {
		t.Fatalf("list attempts: %v", err)
	}
	if len(attempts) != goroutines {
		t.Fatalf("stored %d attempts, want %d", len(attempts), goroutines)
	}
	for i, attempt := range attempts {
		if attempt.AttemptNumber != i+1 {
			t.Fatalf("attempt %d has number %d, want %d", i, attempt.AttemptNumber, i+1)
		}
	}
}

// TestNotificationDedupeUnderConcurrency proves only one row exists per dedupe
// key no matter how many senders race.
func TestNotificationDedupeUnderConcurrency(t *testing.T) {
	store := newStore(t)
	ctx := context.Background()
	session := mustSeedSession(t, store, "notify-race")

	const goroutines = 10
	created := make(chan bool, goroutines)
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, wasCreated, err := store.EnqueueNotification(ctx, storage.Notification{
				SessionID: session.ID, Event: storage.EventRecoverySuccess,
				DedupeKey: "same-event", Body: "resumed",
			})
			if err != nil {
				errs <- err
				return
			}
			created <- wasCreated
		}()
	}
	wg.Wait()
	close(created)
	close(errs)
	for err := range errs {
		t.Fatalf("enqueue race: %v", err)
	}
	crations := 0
	for wasCreated := range created {
		if wasCreated {
			crations++
		}
	}
	if crations != 1 {
		t.Fatalf("%d goroutines reported created, want exactly 1", crations)
	}
	var stored int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM notifications`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 1 {
		t.Fatalf("stored %d notifications, want 1", stored)
	}
}

// contains is a tiny helper so tests avoid pulling in strings for one check.
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
