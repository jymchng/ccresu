package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// This file implements `ccresu sessions delete <id>`: forgetting a session.
//
// Deleting the row alone would not delete anything for long. ccresu *discovers*
// sessions from the Claude Code transcripts on disk, so the very next daemon pass
// would upsert the session straight back and the operator would be told the
// delete had failed. A tombstone in `deleted_sessions` is what makes the delete
// stick: discovery skips every id it names.
//
// Nothing under ~/.claude is ever touched. A deleted session is one ccresu has
// stopped tracking, not one whose conversation has been destroyed; that is why
// the tombstone keeps the paths and why `ccresu enable <id>` can bring it back
// without the transcript being read first.
//
// The child rows go with the session: `usage_limits`, `recovery_attempts`,
// `state_transitions`, `schedules` and `notifications` all reference
// `sessions (id) ON DELETE CASCADE`, so the database enforces that a forgotten
// session leaves nothing behind that would later reappear under a re-discovered
// id.

// DeletedSession is one tombstone: a session ccresu has been told to forget.
type DeletedSession struct {
	// ID is the Claude Code session UUID that was deleted.
	ID string `json:"id"`
	// ProjectPath and TranscriptPath are kept so the delete can be undone
	// without reading the transcript again.
	ProjectPath    string `json:"project_path"`
	TranscriptPath string `json:"transcript_path"`
	// State is the state the session was in when it was deleted, kept for the
	// audit trail and for the report `sessions delete` prints.
	State State `json:"state"`
	// DeletedAt is when the operator deleted it, in UTC.
	DeletedAt time.Time `json:"deleted_at"`
}

// ForgetCounts reports what one delete removed, so the operator can see that the
// history went with it rather than discovering it later in `ccresu history`.
type ForgetCounts struct {
	// Attempts, Transitions, UsageLimits and Notifications are the child rows
	// that cascaded with the session.
	Attempts      int `json:"attempts"`
	Transitions   int `json:"transitions"`
	UsageLimits   int `json:"usage_limits"`
	Notifications int `json:"notifications"`
	// Scheduled reports whether a pending reset wait was cancelled.
	Scheduled bool `json:"scheduled"`
}

// ForgetSession deletes one session and records a tombstone for it.
//
// The session and its history are removed in a single transaction with the
// tombstone, so there is no window in which discovery could see neither the row
// nor the tombstone and resurrect the session. A session that does not exist is
// ErrNotFound; a session that is already forgotten is reported as having been
// forgotten (see GetDeletedSession), because "delete it again" is not an error an
// operator should have to decode.
func (s *Store) ForgetSession(ctx context.Context, id string) (DeletedSession, ForgetCounts, error) {
	if s == nil || s.db == nil {
		return DeletedSession{}, ForgetCounts{}, ErrClosed
	}
	if strings.TrimSpace(id) == "" {
		return DeletedSession{}, ForgetCounts{}, fmt.Errorf("%w: session id is required", ErrInvalid)
	}

	deleted := DeletedSession{ID: id, DeletedAt: s.now().UTC()}
	var counts ForgetCounts
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `
			SELECT project_path, transcript_path, state FROM sessions WHERE id = ?`, id)
		var state string
		if err := row.Scan(&deleted.ProjectPath, &deleted.TranscriptPath, &state); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: session %s", ErrNotFound, id)
			}
			return fmt.Errorf("read session %s: %w", id, err)
		}
		deleted.State = State(state)

		// Count before the cascade, because afterwards there is nothing to count.
		counts.Scheduled = true
		for _, child := range []struct {
			table string
			into  *int
		}{
			{"recovery_attempts", &counts.Attempts},
			{"state_transitions", &counts.Transitions},
			{"usage_limits", &counts.UsageLimits},
			{"notifications", &counts.Notifications},
		} {
			var n int
			query := "SELECT COUNT(*) FROM " + child.table + " WHERE session_id = ?"
			if err := tx.QueryRowContext(ctx, query, id).Scan(&n); err != nil {
				return fmt.Errorf("count %s for %s: %w", child.table, id, err)
			}
			*child.into = n
		}
		var schedules int
		if err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM schedules WHERE session_id = ?", id).Scan(&schedules); err != nil {
			return fmt.Errorf("count schedules for %s: %w", id, err)
		}
		counts.Scheduled = schedules > 0

		if _, err := tx.ExecContext(ctx, `
			INSERT INTO deleted_sessions (id, project_path, transcript_path, state, deleted_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET
				project_path = excluded.project_path,
				transcript_path = excluded.transcript_path,
				state = excluded.state,
				deleted_at = excluded.deleted_at`,
			deleted.ID, deleted.ProjectPath, deleted.TranscriptPath, string(deleted.State),
			timeToDB(deleted.DeletedAt),
		); err != nil {
			return fmt.Errorf("record the deletion of %s: %w", id, err)
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id); err != nil {
			return fmt.Errorf("delete session %s: %w", id, err)
		}
		return nil
	})
	if err != nil {
		return DeletedSession{}, ForgetCounts{}, err
	}
	return deleted, counts, nil
}

// GetDeletedSession returns the tombstone for one id.
//
// It answers ErrNotFound when the session was never deleted, which is the normal
// case and therefore not an error worth a sentinel of its own.
func (s *Store) GetDeletedSession(ctx context.Context, id string) (DeletedSession, error) {
	if s == nil || s.db == nil {
		return DeletedSession{}, ErrClosed
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT id, project_path, transcript_path, state, deleted_at
		FROM deleted_sessions WHERE id = ?`, id)
	deleted, err := scanDeletedSession(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DeletedSession{}, fmt.Errorf("%w: deleted session %s", ErrNotFound, id)
		}
		return DeletedSession{}, fmt.Errorf("read deleted session %s: %w", id, err)
	}
	return deleted, nil
}

// DeletedSessionIDs returns every tombstoned id as a set.
//
// Discovery calls this once per pass and skips what it names. A set rather than a
// query per session keeps a transcript directory with hundreds of sessions to one
// round trip, and makes the daemon's behaviour independent of how many sessions
// happen to exist.
func (s *Store) DeletedSessionIDs(ctx context.Context) (map[string]struct{}, error) {
	if s == nil || s.db == nil {
		return nil, ErrClosed
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM deleted_sessions`)
	if err != nil {
		return nil, fmt.Errorf("list deleted sessions: %w", err)
	}
	defer rows.Close()

	deleted := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan deleted session: %w", err)
		}
		deleted[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list deleted sessions: %w", err)
	}
	return deleted, nil
}

// ListDeletedSessions returns every tombstone, newest first.
func (s *Store) ListDeletedSessions(ctx context.Context) ([]DeletedSession, error) {
	if s == nil || s.db == nil {
		return nil, ErrClosed
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_path, transcript_path, state, deleted_at
		FROM deleted_sessions ORDER BY deleted_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("list deleted sessions: %w", err)
	}
	defer rows.Close()

	var deleted []DeletedSession
	for rows.Next() {
		record, err := scanDeletedSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan deleted session: %w", err)
		}
		deleted = append(deleted, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list deleted sessions: %w", err)
	}
	return deleted, nil
}

// RestoreSession undoes a delete: the tombstone goes and the session row comes
// back in DISCOVERED, enabled state, so the next discovery pass refreshes it and
// recovery is allowed again.
//
// It returns the tombstone it acted on, so the caller can report which session it
// brought back. Restoring a session that was never deleted is ErrNotFound: there
// is nothing to undo, and silently "succeeding" would hide a typo in the id.
func (s *Store) RestoreSession(ctx context.Context, id string) (DeletedSession, error) {
	if s == nil || s.db == nil {
		return DeletedSession{}, ErrClosed
	}
	if strings.TrimSpace(id) == "" {
		return DeletedSession{}, fmt.Errorf("%w: session id is required", ErrInvalid)
	}

	var deleted DeletedSession
	now := s.now().UTC()
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `
			SELECT id, project_path, transcript_path, state, deleted_at
			FROM deleted_sessions WHERE id = ?`, id)
		record, err := scanDeletedSession(row)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: deleted session %s", ErrNotFound, id)
			}
			return fmt.Errorf("read deleted session %s: %w", id, err)
		}
		deleted = record

		if _, err := tx.ExecContext(ctx, `DELETE FROM deleted_sessions WHERE id = ?`, id); err != nil {
			return fmt.Errorf("clear the deletion of %s: %w", id, err)
		}

		// The row starts DISCOVERED and enabled: the operator asked for it back,
		// and discovery fills in everything else on its next pass. Sessions are
		// not recoverable until detection has seen their transcript, which is
		// exactly what a re-discovered session is.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sessions (
				id, project_path, transcript_path, provider, session_type,
				state, enabled, recoverable, reason, last_error,
				discovered_at, last_seen_at, updated_at
			) VALUES (?, ?, ?, 'claude', 'unknown', ?, 1, 0, ?, '', ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET
				enabled = 1,
				updated_at = excluded.updated_at`,
			id, deleted.ProjectPath, deleted.TranscriptPath, string(StateDiscovered),
			"restored after a delete; discovery will refresh it",
			timeToDB(now), timeToDB(now), timeToDB(now),
		); err != nil {
			return fmt.Errorf("restore session %s: %w", id, err)
		}
		return nil
	})
	if err != nil {
		return DeletedSession{}, err
	}
	return deleted, nil
}

// scanDeletedSession reads one tombstone from a row or a query.
func scanDeletedSession(row interface{ Scan(...any) error }) (DeletedSession, error) {
	var (
		deleted DeletedSession
		state   string
		at      int64
	)
	if err := row.Scan(&deleted.ID, &deleted.ProjectPath, &deleted.TranscriptPath, &state, &at); err != nil {
		return DeletedSession{}, err
	}
	deleted.State = State(state)
	when, err := timeFromDB(at)
	if err != nil {
		return DeletedSession{}, err
	}
	deleted.DeletedAt = when
	return deleted, nil
}
