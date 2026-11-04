// Package storage persists session, usage-limit, recovery and notification state in SQLite.
//
// It uses the pure-Go modernc.org/sqlite driver so release binaries need no cgo
// toolchain and no system SQLite library. Every connection enables WAL mode,
// a busy timeout and foreign keys, because a watchdog that silently loses a
// foreign-key cascade or blocks on a lock would corrupt its own audit trail.
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	// Register the pure-Go "sqlite" driver.
	_ "modernc.org/sqlite"
)

// Common errors returned by the storage layer.
var (
	// ErrNotFound means the requested row does not exist.
	ErrNotFound = errors.New("not found")
	// ErrInvalid means a caller supplied a value the schema cannot store.
	ErrInvalid = errors.New("invalid storage argument")
	// ErrClosed means the store was used after Close.
	ErrClosed = errors.New("storage is closed")
)

// driverName is the database/sql driver registered by modernc.org/sqlite.
const driverName = "sqlite"

// Options configures Open.
type Options struct {
	// Path is the database file. Required unless Memory is set.
	Path string
	// BusyTimeout is how long a writer waits for a lock before failing.
	// Zero means DefaultBusyTimeout. A watchdog must not fail its own writes
	// because a reader happened to hold the database briefly.
	BusyTimeout time.Duration
	// MaxOpenConns caps the connection pool. Zero means DefaultMaxOpenConns.
	MaxOpenConns int
	// Clock supplies timestamps. Nil means the system clock.
	Clock func() time.Time
	// Memory opens a private in-memory database instead of Path. Each in-memory
	// database is per-process and is discarded on Close, so use it only in
	// tests that do not need reopen semantics.
	Memory bool
}

// Defaults applied by Open when Options leave a field zero.
const (
	// DefaultBusyTimeout is the default SQLite write-lock wait.
	DefaultBusyTimeout = 5 * time.Second
	// DefaultMaxOpenConns keeps one writer plus a few readers. WAL allows
	// concurrent readers, so a small pool is enough for a single-user watchdog.
	DefaultMaxOpenConns = 4
	// fileMode restricts the database file to the owner: it holds transcripts
	// excerpts and session identifiers.
	fileMode = 0o600
	// dirMode restricts the containing directory to the owner.
	dirMode = 0o700
)

// Store is a SQLite-backed repository bundle. It is safe for concurrent use.
type Store struct {
	db   *sql.DB
	path string
	now  func() time.Time
}

// Open opens (creating if needed) the database, applies file permissions and
// verifies that the required pragmas actually took effect.
//
// Open does not run migrations; call Migrate explicitly so that a failure to
// migrate is never confused with a failure to connect.
func Open(ctx context.Context, opts Options) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	now := opts.Clock
	if now == nil {
		now = time.Now
	}
	busy := opts.BusyTimeout
	if busy <= 0 {
		busy = DefaultBusyTimeout
	}
	maxConns := opts.MaxOpenConns
	if maxConns <= 0 {
		maxConns = DefaultMaxOpenConns
	}

	path := strings.TrimSpace(opts.Path)
	dsn := ""
	switch {
	case opts.Memory:
		path = ":memory:"
		dsn = "file:ccresu-memory-" + strconv.FormatInt(now().UnixNano(), 10) + "?mode=memory&cache=shared"
	default:
		if path == "" {
			return nil, fmt.Errorf("%w: storage path is empty", ErrInvalid)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("%w: resolve storage path %q: %v", ErrInvalid, path, err)
		}
		path = abs
		if err := prepareFile(path); err != nil {
			return nil, err
		}
		dsn = "file:" + path
	}

	// Pragmas are passed in the DSN so they are applied to every pooled
	// connection, not just the one that happened to run an Exec. foreign_keys in
	// particular is per-connection and silently off by default.
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	dsn += separator + strings.Join([]string{
		fmt.Sprintf("_pragma=busy_timeout(%d)", busy.Milliseconds()),
		"_pragma=journal_mode(WAL)",
		"_pragma=foreign_keys(1)",
		"_pragma=synchronous(NORMAL)",
		// Start write transactions as BEGIN IMMEDIATE. A deferred transaction
		// that reads and then writes must upgrade its lock, and SQLite reports
		// SQLITE_BUSY (517, "database is locked") for that upgrade *without*
		// invoking the busy handler. Under concurrent writers that turns into
		// spurious failures at random, which is exactly what a watchdog cannot
		// afford. Taking the write lock up front makes contention wait instead.
		"_txlock=immediate",
	}, "&")

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	db.SetConnMaxLifetime(0)

	store := &Store{db: db, path: path, now: now}
	if err := store.verifyPragmas(ctx, busy); err != nil {
		_ = db.Close()
		return nil, err
	}
	if !opts.Memory {
		// SQLite creates the file with the process umask; tighten it afterwards
		// so a permissive umask cannot leave the database world-readable.
		if err := os.Chmod(path, fileMode); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("restrict permissions on %s: %w", path, err)
		}
	}
	return store, nil
}

// prepareFile creates the database file with owner-only permissions and its
// parent directory with owner-only permissions.
func prepareFile(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("create storage directory %s: %w", dir, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, fileMode)
	if err != nil {
		return fmt.Errorf("create storage file %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close storage file %s: %w", path, err)
	}
	return os.Chmod(path, fileMode)
}

// verifyPragmas proves the DSN pragmas were applied on a real connection.
//
// A silent failure here would mean writes are not serialised (busy timeout), the
// journal is not crash-safe (WAL) or cascades do not fire (foreign keys).
func (s *Store) verifyPragmas(ctx context.Context, busy time.Duration) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Close()

	var journalMode string
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("read journal_mode: %w", err)
	}
	if !strings.EqualFold(journalMode, "wal") && s.path != ":memory:" {
		return fmt.Errorf("sqlite journal_mode is %q, want wal", journalMode)
	}

	var foreignKeys int
	if err := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read foreign_keys: %w", err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("sqlite foreign_keys is %d, want 1", foreignKeys)
	}

	var gotBusy int64
	if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&gotBusy); err != nil {
		return fmt.Errorf("read busy_timeout: %w", err)
	}
	if gotBusy != busy.Milliseconds() {
		return fmt.Errorf("sqlite busy_timeout is %dms, want %dms", gotBusy, busy.Milliseconds())
	}
	return nil
}

// Close closes the underlying pool.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	err := s.db.Close()
	s.db = nil
	return err
}

// Path reports the database file path.
func (s *Store) Path() string { return s.path }

// DB exposes the connection pool for callers that need a transaction spanning
// several repositories.
func (s *Store) DB() *sql.DB { return s.db }

// Now reports the store's clock, so repositories and callers agree on time.
func (s *Store) Now() time.Time { return s.now() }

// InTx runs fn inside a transaction, rolling back on error or panic.
//
// Repositories use it for multi-statement invariants (allocate an attempt
// number, transition state, enqueue a notification) that must be atomic.
func (s *Store) InTx(ctx context.Context, fn func(*sql.Tx) error) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	committed = true
	return nil
}

// timeToDB converts t to the stored representation: UTC Unix milliseconds.
// Zero times are stored as NULL.
func timeToDB(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().UnixMilli()
}

// timeFromDB converts a stored value back to a UTC time.
func timeFromDB(v any) (time.Time, error) {
	switch value := v.(type) {
	case nil:
		return time.Time{}, nil
	case int64:
		return time.UnixMilli(value).UTC(), nil
	case []byte:
		n, err := strconv.ParseInt(string(value), 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("%w: parse timestamp %q: %v", ErrInvalid, value, err)
		}
		return time.UnixMilli(n).UTC(), nil
	case string:
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("%w: parse timestamp %q: %v", ErrInvalid, value, err)
		}
		return time.UnixMilli(n).UTC(), nil
	case time.Time:
		return value.UTC(), nil
	default:
		return time.Time{}, fmt.Errorf("%w: unsupported timestamp type %T", ErrInvalid, v)
	}
}

// boolToInt stores a bool as 0/1, which SQLite CHECK constraints can enforce.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// intToBool reads SQLite's 0/1 representation.
func intToBool(n int64) bool { return n != 0 }

// nullableBool converts a *bool to a storable value.
func nullableBool(b *bool) any {
	if b == nil {
		return nil
	}
	return boolToInt(*b)
}

// scanNullableBool converts a stored value back to a *bool.
func scanNullableBool(v sql.NullInt64) *bool {
	if !v.Valid {
		return nil
	}
	b := v.Int64 != 0
	return &b
}

// wrapNotFound maps sql.ErrNoRows onto ErrNotFound with context.
func wrapNotFound(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrNotFound, what)
	}
	return err
}
