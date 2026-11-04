package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

// migrationsFS holds the embedded schema migrations so a single static binary
// carries its own schema.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// migration is one versioned, reversible schema change.
type migration struct {
	version int
	name    string
	up      string
	down    string
	// checksum is the SHA-256 of the whole file, so an edited-but-already-applied
	// migration is detected instead of silently diverging from the database.
	checksum string
}

// migrationMarker separates the Up and Down halves of a migration file.
const migrationMarker = "-- +migrate Down"

// loadMigrations reads, parses and validates every embedded migration.
func loadMigrations() ([]migration, error) {
	return loadMigrationsFrom(migrationsFS)
}

// loadMigrationsFrom is loadMigrations with an injectable filesystem, so the
// parser's rejection paths can be tested with deliberately broken migrations.
func loadMigrationsFrom(fsys fs.FS) ([]migration, error) {
	paths, err := fs.Glob(fsys, "migrations/*.sql")
	if err != nil {
		return nil, fmt.Errorf("glob migrations: %w", err)
	}
	if len(paths) == 0 {
		return nil, errors.New("no embedded migrations found")
	}
	sort.Strings(paths)

	migrations := make([]migration, 0, len(paths))
	seen := map[int]string{}
	for _, p := range paths {
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", p, err)
		}
		base := path.Base(p)
		prefix, rest, found := strings.Cut(base, "_")
		if !found {
			return nil, fmt.Errorf("migration %s: expected <version>_<name>.sql", base)
		}
		version, err := strconv.Atoi(prefix)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("migration %s: invalid version %q", base, prefix)
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations %s and %s share version %d", other, base, version)
		}
		seen[version] = base

		up, down, err := splitMigration(string(data))
		if err != nil {
			return nil, fmt.Errorf("migration %s: %w", base, err)
		}
		sum := sha256.Sum256(data)
		migrations = append(migrations, migration{
			version:  version,
			name:     strings.TrimSuffix(rest, ".sql"),
			up:       up,
			down:     down,
			checksum: hex.EncodeToString(sum[:]),
		})
	}
	for i, m := range migrations {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration versions must be contiguous from 1: %s has version %d at position %d",
				m.name, m.version, i+1)
		}
	}
	return migrations, nil
}

// splitMigration splits a file into its Up and Down halves.
func splitMigration(content string) (up string, down string, err error) {
	idx := strings.Index(content, migrationMarker)
	if idx < 0 {
		return "", "", fmt.Errorf("missing %q marker", migrationMarker)
	}
	up = strings.TrimSpace(content[:idx])
	down = strings.TrimSpace(content[idx+len(migrationMarker):])
	if up == "" {
		return "", "", errors.New("empty Up section")
	}
	if down == "" {
		return "", "", errors.New("empty Down section")
	}
	return up, down, nil
}

// migrationTableDDL creates the bookkeeping table. It is created by the runner
// rather than by a migration, because it must exist before the first migration.
const migrationTableDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT    NOT NULL,
    checksum   TEXT    NOT NULL,
    applied_at INTEGER NOT NULL,
    CHECK (version > 0)
)`

// MigrationStatus describes one migration and whether the database has it.
type MigrationStatus struct {
	Version   int
	Name      string
	Applied   bool
	AppliedAt int64
}

// Migrate applies every pending migration in order and returns the versions it
// applied. Running it twice is a no-op, which makes it safe at every startup.
func (s *Store) Migrate(ctx context.Context) ([]int, error) {
	if s == nil || s.db == nil {
		return nil, ErrClosed
	}
	migrations, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	if err := s.ensureMigrationTable(ctx); err != nil {
		return nil, err
	}

	current, applied, err := s.currentVersion(ctx)
	if err != nil {
		return nil, err
	}
	if err := verifyChecksums(migrations, applied); err != nil {
		return nil, err
	}

	var ran []int
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		if err := s.applyMigration(ctx, m); err != nil {
			return ran, fmt.Errorf("apply migration %d_%s: %w", m.version, m.name, err)
		}
		ran = append(ran, m.version)
	}
	return ran, nil
}

// Rollback reverses migrations until the schema is at targetVersion. Pass 0 to
// drop everything. It returns the versions it reversed, newest first.
func (s *Store) Rollback(ctx context.Context, targetVersion int) ([]int, error) {
	if s == nil || s.db == nil {
		return nil, ErrClosed
	}
	if targetVersion < 0 {
		return nil, fmt.Errorf("%w: target version %d", ErrInvalid, targetVersion)
	}
	migrations, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	if targetVersion > len(migrations) {
		return nil, fmt.Errorf("%w: target version %d exceeds latest %d", ErrInvalid, targetVersion, len(migrations))
	}
	if err := s.ensureMigrationTable(ctx); err != nil {
		return nil, err
	}
	current, _, err := s.currentVersion(ctx)
	if err != nil {
		return nil, err
	}
	if targetVersion > current {
		return nil, fmt.Errorf("%w: cannot roll forward to %d from %d", ErrInvalid, targetVersion, current)
	}

	byVersion := make(map[int]migration, len(migrations))
	for _, m := range migrations {
		byVersion[m.version] = m
	}

	var reverted []int
	for v := current; v > targetVersion; v-- {
		m, ok := byVersion[v]
		if !ok {
			return reverted, fmt.Errorf("no embedded migration for applied version %d", v)
		}
		if err := s.revertMigration(ctx, m); err != nil {
			return reverted, fmt.Errorf("revert migration %d_%s: %w", m.version, m.name, err)
		}
		reverted = append(reverted, v)
	}
	return reverted, nil
}

// Version reports the highest applied migration version (0 for an empty schema).
func (s *Store) Version(ctx context.Context) (int, error) {
	if s == nil || s.db == nil {
		return 0, ErrClosed
	}
	if err := s.ensureMigrationTable(ctx); err != nil {
		return 0, err
	}
	current, _, err := s.currentVersion(ctx)
	return current, err
}

// Status reports every embedded migration with its applied flag.
func (s *Store) Status(ctx context.Context) ([]MigrationStatus, error) {
	if s == nil || s.db == nil {
		return nil, ErrClosed
	}
	migrations, err := loadMigrations()
	if err != nil {
		return nil, err
	}
	if err := s.ensureMigrationTable(ctx); err != nil {
		return nil, err
	}
	_, applied, err := s.currentVersion(ctx)
	if err != nil {
		return nil, err
	}
	statuses := make([]MigrationStatus, 0, len(migrations))
	for _, m := range migrations {
		row, ok := applied[m.version]
		statuses = append(statuses, MigrationStatus{
			Version:   m.version,
			Name:      m.name,
			Applied:   ok,
			AppliedAt: row.appliedAt,
		})
	}
	return statuses, nil
}

// appliedMigration is one row of schema_migrations.
type appliedMigration struct {
	name      string
	checksum  string
	appliedAt int64
}

func (s *Store) ensureMigrationTable(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, migrationTableDDL); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	return nil
}

func (s *Store) currentVersion(ctx context.Context) (int, map[int]appliedMigration, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT version, name, checksum, applied_at FROM schema_migrations ORDER BY version`)
	if err != nil {
		return 0, nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := map[int]appliedMigration{}
	current := 0
	for rows.Next() {
		var version int
		var row appliedMigration
		if err := rows.Scan(&version, &row.name, &row.checksum, &row.appliedAt); err != nil {
			return 0, nil, fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[version] = row
		if version > current {
			current = version
		}
	}
	if err := rows.Err(); err != nil {
		return 0, nil, fmt.Errorf("iterate schema_migrations: %w", err)
	}
	return current, applied, nil
}

// verifyChecksums refuses to run when an already-applied migration file changed.
func verifyChecksums(migrations []migration, applied map[int]appliedMigration) error {
	byVersion := make(map[int]migration, len(migrations))
	for _, m := range migrations {
		byVersion[m.version] = m
	}
	for version, row := range applied {
		m, ok := byVersion[version]
		if !ok {
			return fmt.Errorf("database has migration version %d (%s) with no embedded file; refusing to run", version, row.name)
		}
		if m.checksum != row.checksum {
			return fmt.Errorf("migration %d_%s checksum changed (database %s, embedded %s); refusing to run",
				version, m.name, row.checksum, m.checksum)
		}
	}
	return nil
}

// applyMigration runs one Up section and records it, atomically.
func (s *Store) applyMigration(ctx context.Context, m migration) error {
	return s.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, m.up); err != nil {
			return fmt.Errorf("exec up: %w", err)
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name, checksum, applied_at) VALUES (?, ?, ?, ?)`,
			m.version, m.name, m.checksum, s.now().UTC().UnixMilli())
		if err != nil {
			return fmt.Errorf("record migration: %w", err)
		}
		return nil
	})
}

// revertMigration runs one Down section and deletes its record, atomically.
func (s *Store) revertMigration(ctx context.Context, m migration) error {
	return s.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, m.down); err != nil {
			return fmt.Errorf("exec down: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, m.version); err != nil {
			return fmt.Errorf("delete migration record: %w", err)
		}
		return nil
	})
}
