package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

// TestEmbeddedMigrationsAreWellFormed checks the real, shipped migrations: they
// must be contiguous from 1, reversible and carry a checksum.
func TestEmbeddedMigrationsAreWellFormed(t *testing.T) {
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(migrations) < 2 {
		t.Fatalf("expected at least 2 migrations, got %d", len(migrations))
	}
	for i, m := range migrations {
		if m.version != i+1 {
			t.Fatalf("migration %d has version %d", i, m.version)
		}
		if m.name == "" || m.up == "" || m.down == "" || m.checksum == "" {
			t.Fatalf("migration %d is incomplete: %+v", m.version, m)
		}
		if len(m.checksum) != 64 {
			t.Fatalf("migration %d checksum %q is not sha256 hex", m.version, m.checksum)
		}
	}
}

func TestLoadMigrationsRejectsBrokenFiles(t *testing.T) {
	valid := "-- +migrate Up\nCREATE TABLE a (id INTEGER);\n-- +migrate Down\nDROP TABLE a;\n"

	cases := map[string]fstest.MapFS{
		"no migrations": {
			"migrations/.keep": &fstest.MapFile{Data: []byte("")},
		},
		"missing version prefix": {
			"migrations/init.sql": &fstest.MapFile{Data: []byte(valid)},
		},
		"non-numeric version": {
			"migrations/abc_init.sql": &fstest.MapFile{Data: []byte(valid)},
		},
		"zero version": {
			"migrations/0_init.sql": &fstest.MapFile{Data: []byte(valid)},
		},
		"duplicate version": {
			"migrations/0001_a.sql": &fstest.MapFile{Data: []byte(valid)},
			"migrations/0001_b.sql": &fstest.MapFile{Data: []byte(valid)},
		},
		"non-contiguous versions": {
			"migrations/0001_a.sql": &fstest.MapFile{Data: []byte(valid)},
			"migrations/0003_c.sql": &fstest.MapFile{Data: []byte(valid)},
		},
		"missing down marker": {
			"migrations/0001_a.sql": &fstest.MapFile{Data: []byte("-- +migrate Up\nCREATE TABLE a (id INTEGER);\n")},
		},
		"empty up section": {
			"migrations/0001_a.sql": &fstest.MapFile{Data: []byte("-- +migrate Down\nDROP TABLE a;\n")},
		},
		"empty down section": {
			"migrations/0001_a.sql": &fstest.MapFile{Data: []byte("-- +migrate Up\nCREATE TABLE a (id INTEGER);\n-- +migrate Down\n")},
		},
	}
	for name, fsys := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := loadMigrationsFrom(fsys); err == nil {
				t.Fatal("expected an error")
			}
		})
	}

	// A single well-formed migration loads.
	good := fstest.MapFS{"migrations/0001_init.sql": &fstest.MapFile{Data: []byte(valid)}}
	migrations, err := loadMigrationsFrom(good)
	if err != nil {
		t.Fatalf("valid migration rejected: %v", err)
	}
	if len(migrations) != 1 || migrations[0].name != "init" || migrations[0].version != 1 {
		t.Fatalf("unexpected parse result: %+v", migrations)
	}
}

func TestSplitMigration(t *testing.T) {
	up, down, err := splitMigration("-- +migrate Up\nSELECT 1;\n-- +migrate Down\nSELECT 2;\n")
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if up == "" || down == "" {
		t.Fatalf("empty halves: %q %q", up, down)
	}
	for _, bad := range []string{"", "SELECT 1;", "-- +migrate Down\nSELECT 2;"} {
		if _, _, err := splitMigration(bad); err == nil {
			t.Fatalf("splitMigration(%q) accepted invalid input", bad)
		}
	}
}

func TestIsUniqueViolation(t *testing.T) {
	if isUniqueViolation(nil) {
		t.Fatal("nil is not a unique violation")
	}
	if !isUniqueViolation(errors.New("constraint failed: UNIQUE constraint failed: recovery_attempts.session_id")) {
		t.Fatal("sqlite unique message not recognised")
	}
	if isUniqueViolation(errors.New("database is locked")) {
		t.Fatal("unrelated error misclassified as unique violation")
	}
}

func TestTimeRoundTrip(t *testing.T) {
	if _, err := timeFromDB(nil); err != nil {
		t.Fatalf("nil timestamp: %v", err)
	}
	instant := time.Date(2026, 5, 6, 7, 8, 9, 10_000_000, time.FixedZone("CEST", 2*3600))
	stored := timeToDB(instant)
	rounded, err := timeFromDB(stored)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if !rounded.Equal(instant.Truncate(time.Millisecond)) {
		t.Fatalf("round trip = %s, want %s", rounded, instant)
	}
	if rounded.Location() != time.UTC {
		t.Fatalf("round trip location = %s, want UTC", rounded.Location())
	}
	if timeToDB(time.Time{}) != nil {
		t.Fatal("zero time must store as NULL")
	}
	for _, value := range []any{int64(1700000000000), []byte("1700000000000"), "1700000000000", instant} {
		if _, err := timeFromDB(value); err != nil {
			t.Fatalf("timeFromDB(%T): %v", value, err)
		}
	}
	for _, bad := range []any{"not-a-timestamp", []byte("nope"), 3.5, struct{}{}} {
		if _, err := timeFromDB(bad); err == nil {
			t.Fatalf("timeFromDB(%T) accepted invalid input", bad)
		}
	}
	if !intToBool(1) || intToBool(0) {
		t.Fatal("intToBool is wrong")
	}
	if boolToInt(true) != 1 || boolToInt(false) != 0 {
		t.Fatal("boolToInt is wrong")
	}
	if nullableBool(nil) != nil {
		t.Fatal("nullableBool(nil) must be NULL")
	}
	if scanNullableBool(sql.NullInt64{}) != nil {
		t.Fatal("scanNullableBool(invalid) must be nil")
	}
	if v := scanNullableBool(sql.NullInt64{Int64: 0, Valid: true}); v == nil || *v {
		t.Fatal("scanNullableBool(0) must be a false pointer")
	}
}

func TestInTxRollbackSemantics(t *testing.T) {
	store := openInternal(t)
	ctx := context.Background()

	sentinel := errors.New("boom")
	err := store.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings (key, value, updated_at) VALUES ('tx', 'value', 0)`); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("InTx error = %v, want the callback error", err)
	}
	if _, ok, err := store.GetSetting(ctx, "tx"); err != nil || ok {
		t.Fatalf("rollback did not discard the write: ok=%t err=%v", ok, err)
	}

	// A panic must roll back too, so a crashed handler cannot half-commit.
	func() {
		defer func() { _ = recover() }()
		_ = store.InTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO settings (key, value, updated_at) VALUES ('panic', 'value', 0)`); err != nil {
				return err
			}
			panic("handler exploded")
		})
	}()
	if _, ok, err := store.GetSetting(ctx, "panic"); err != nil || ok {
		t.Fatalf("panic did not roll back: ok=%t err=%v", ok, err)
	}

	if err := store.InTx(ctx, func(tx *sql.Tx) error { return nil }); err != nil {
		t.Fatalf("empty transaction: %v", err)
	}
}

// TestClosedStoreUniformErrors proves every repository reports ErrClosed instead
// of panicking or touching a dead pool.
func TestClosedStoreUniformErrors(t *testing.T) {
	store := openInternal(t)
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	ctx := context.Background()

	checks := map[string]error{
		"UpsertSession":             func() error { _, err := store.UpsertSession(ctx, Session{ID: "x"}); return err }(),
		"GetSession":                func() error { _, err := store.GetSession(ctx, "x"); return err }(),
		"ListSessions":              func() error { _, err := store.ListSessions(ctx, SessionFilter{}); return err }(),
		"CountSessions":             func() error { _, err := store.CountSessions(ctx, SessionFilter{}); return err }(),
		"SetSessionEnabled":         store.SetSessionEnabled(ctx, "x", true),
		"TransitionSession":         func() error { _, err := store.TransitionSession(ctx, "x", StateRunning, "", 0); return err }(),
		"SetSessionIdentity":        store.SetSessionIdentity(ctx, "x", 1, 2, 3),
		"SetSessionTranscriptStats": store.SetSessionTranscriptMetrics(ctx, "x", "/t", 1, 1),
		"SetSessionError":           store.SetSessionError(ctx, "x", "e"),
		"DeleteSession":             store.DeleteSession(ctx, "x"),
		"RecordUsageLimit":          func() error { _, err := store.RecordUsageLimit(ctx, UsageLimit{SessionID: "x"}); return err }(),
		"GetUsageLimit":             func() error { _, err := store.GetUsageLimit(ctx, 1); return err }(),
		"LatestUsageLimit":          func() error { _, err := store.LatestUsageLimit(ctx, "x"); return err }(),
		"ListUsageLimits":           func() error { _, err := store.ListUsageLimits(ctx, "x", 0); return err }(),
		"SetUsageLimitResetAt":      store.SetUsageLimitResetAt(ctx, 1, time.Now(), "", ""),
		"StartAttempt":              func() error { _, err := store.StartAttempt(ctx, "x", 0, "c"); return err }(),
		"FinishAttempt":             func() error { _, err := store.FinishAttempt(ctx, 1, AttemptResult{State: StateResumed}); return err }(),
		"GetAttempt":                func() error { _, err := store.GetAttempt(ctx, 1); return err }(),
		"ListAttempts":              func() error { _, err := store.ListAttempts(ctx, "x", 0); return err }(),
		"CountAttempts":             func() error { _, err := store.CountAttempts(ctx, "x"); return err }(),
		"LatestAttempt":             func() error { _, err := store.LatestAttempt(ctx, "x"); return err }(),
		"DeleteAttempt":             store.DeleteAttempt(ctx, 1),
		"RecordTransition":          store.RecordTransition(ctx, Transition{SessionID: "x", ToState: StateRunning}),
		"ListTransitions":           func() error { _, err := store.ListTransitions(ctx, "x", 0); return err }(),
		"ListTransitionsForAttempt": func() error { _, err := store.ListTransitionsForAttempt(ctx, 1); return err }(),
		"EnqueueNotification": func() error {
			_, _, err := store.EnqueueNotification(ctx, Notification{Event: EventWaiting, DedupeKey: "k"})
			return err
		}(),
		"GetNotification":          func() error { _, err := store.GetNotification(ctx, 1); return err }(),
		"GetNotificationByKey":     func() error { _, err := store.GetNotificationByKey(ctx, "k"); return err }(),
		"ListPendingNotifications": func() error { _, err := store.ListPendingNotifications(ctx, 0); return err }(),
		"ListNotificationsFor":     func() error { _, err := store.ListNotificationsForSession(ctx, "x", 0); return err }(),
		"MarkNotificationSent":     store.MarkNotificationSent(ctx, 1, time.Now()),
		"MarkNotificationFailed":   store.MarkNotificationFailed(ctx, 1, "e", NotificationFailed),
		"GetSetting":               func() error { _, _, err := store.GetSetting(ctx, "x"); return err }(),
		"SetSetting":               store.SetSetting(ctx, "x", "y"),
		"DeleteSetting":            store.DeleteSetting(ctx, "x"),
		"ListSettings":             func() error { _, err := store.ListSettings(ctx); return err }(),
		"SaveSchedule": func() error {
			_, err := store.SaveSchedule(ctx, Schedule{SessionID: "x", ResetAt: time.Now()})
			return err
		}(),
		"GetSchedule":    func() error { _, err := store.GetSchedule(ctx, "x"); return err }(),
		"LoadSchedules":  func() error { _, err := store.LoadSchedules(ctx); return err }(),
		"DeleteSchedule": store.DeleteSchedule(ctx, "x"),
		"InTx":           store.InTx(ctx, func(*sql.Tx) error { return nil }),
	}
	for name, err := range checks {
		if !errors.Is(err, ErrClosed) {
			t.Errorf("%s on closed store = %v, want ErrClosed", name, err)
		}
	}
}

// TestCorruptRowsSurfaceErrors proves a hand-edited or corrupted database is
// reported, not silently converted into a zero timestamp.
func TestCorruptRowsSurfaceErrors(t *testing.T) {
	store := openInternal(t)
	ctx := context.Background()
	if _, err := store.UpsertSession(ctx, Session{ID: "corrupt", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx,
		`UPDATE sessions SET discovered_at = 'garbage' WHERE id = 'corrupt'`); err != nil {
		t.Fatalf("corrupt row: %v", err)
	}
	if _, err := store.GetSession(ctx, "corrupt"); err == nil {
		t.Fatal("corrupt discovered_at was accepted")
	}

	// Same for a usage limit's detected_at.
	if _, err := store.UpsertSession(ctx, Session{ID: "corrupt-limit", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordUsageLimit(ctx, UsageLimit{SessionID: "corrupt-limit"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx,
		`UPDATE usage_limits SET detected_at = 'garbage' WHERE session_id = 'corrupt-limit'`); err != nil {
		t.Fatalf("corrupt limit: %v", err)
	}
	if _, err := store.ListUsageLimits(ctx, "corrupt-limit", 0); err == nil {
		t.Fatal("corrupt detected_at was accepted")
	}

	// And for a notification's sent_at.
	if _, _, err := store.EnqueueNotification(ctx, Notification{
		Event: EventWaiting, DedupeKey: "corrupt-notification",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx,
		`UPDATE notifications SET sent_at = 'garbage' WHERE dedupe_key = 'corrupt-notification'`); err != nil {
		t.Fatalf("corrupt notification: %v", err)
	}
	if _, err := store.ListPendingNotifications(ctx, 0); err == nil {
		t.Fatal("corrupt sent_at was accepted")
	}

	// And for an attempt's started_at.
	if _, err := store.StartAttempt(ctx, "corrupt-limit", 0, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx,
		`UPDATE recovery_attempts SET started_at = 'garbage'`); err != nil {
		t.Fatalf("corrupt attempt: %v", err)
	}
	if _, err := store.ListAttempts(ctx, "corrupt-limit", 0); err == nil {
		t.Fatal("corrupt started_at was accepted")
	}

	// And for a schedule's reset_at, through both the single-row and list reads.
	if _, err := store.SaveSchedule(ctx, Schedule{
		SessionID: "corrupt", ResetAt: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx,
		`UPDATE schedules SET reset_at = 'garbage' WHERE session_id = 'corrupt'`); err != nil {
		t.Fatalf("corrupt schedule: %v", err)
	}
	if _, err := store.GetSchedule(ctx, "corrupt"); err == nil {
		t.Fatal("corrupt reset_at was accepted")
	}
	if _, err := store.LoadSchedules(ctx); err == nil {
		t.Fatal("corrupt reset_at was accepted by the list read")
	}
}

// TestMigrateFailsWhenSchemaIsBroken proves a failing Up section is reported and
// leaves no partial migration record behind.
func TestMigrateFailsWhenSchemaIsBroken(t *testing.T) {
	// Deliberately unmigrated: the conflicting table must exist before Migrate.
	store, err := Open(context.Background(), Options{Path: filepath.Join(t.TempDir(), "broken.db")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	// Occupy the name the migration wants to create, so migration 1 cannot apply.
	if _, err := store.DB().ExecContext(ctx, `CREATE TABLE sessions (id TEXT)`); err != nil {
		t.Fatalf("pre-create table: %v", err)
	}
	if _, err := store.Migrate(ctx); err == nil {
		t.Fatal("migrate succeeded despite a conflicting table")
	}
	var count int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != 0 {
		t.Fatalf("%d migrations were recorded despite the failure", count)
	}
}

// TestDefaultOptionsAreApplied proves the zero Options still produces a usable
// store with the documented defaults.
func TestDefaultOptionsAreApplied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "defaults.db")
	store, err := Open(context.Background(), Options{Path: path})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	var busy int64
	if err := store.DB().QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busy != DefaultBusyTimeout.Milliseconds() {
		t.Fatalf("busy_timeout = %d, want %d", busy, DefaultBusyTimeout.Milliseconds())
	}
	if DefaultMaxOpenConns < 2 {
		t.Fatalf("DefaultMaxOpenConns = %d, too small", DefaultMaxOpenConns)
	}
	if store.Now().IsZero() {
		t.Fatal("Now() returned the zero time")
	}
}

// openInternal opens a migrated store for white-box tests.
func openInternal(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), Options{Path: filepath.Join(t.TempDir(), "internal.db")})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return store
}
