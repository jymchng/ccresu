package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// sessionColumns is the canonical column list for session queries. Keeping it in
// one place means a new column is added (or forgotten) in exactly one spot.
const sessionColumns = `id, project_path, transcript_path, provider, session_type,
	pid, ppid, process_start_time, state, enabled, recoverable, reason, last_error,
	transcript_size, transcript_lines, retry_at, discovered_at, last_seen_at, updated_at`

// SessionFilter narrows a session query. Zero values mean "no constraint".
type SessionFilter struct {
	// ID filters on the exact session ID.
	ID string
	// Enabled filters on the enabled flag when non-nil.
	Enabled *bool
	// States filters on a set of states when non-empty.
	States []State
	// Recoverable filters on the recoverable flag when non-nil.
	Recoverable *bool
	// Limit caps the number of rows; zero means no limit.
	Limit int
	// NewestFirst orders by last_seen_at descending instead of ascending.
	NewestFirst bool
}

// UpsertSession records a discovered session and reports whether it was new.
//
// On conflict the discovery-owned columns are refreshed (paths, identity,
// transcript metrics, recoverability) but the recovery-owned columns — state and
// enabled — are left untouched. A rediscovery must never resurrect a disabled
// session or reset a recovery in progress.
func (s *Store) UpsertSession(ctx context.Context, session Session) (bool, error) {
	if s == nil || s.db == nil {
		return false, ErrClosed
	}
	if strings.TrimSpace(session.ID) == "" {
		return false, fmt.Errorf("%w: session id is required", ErrInvalid)
	}
	if session.State == "" {
		session.State = StateDiscovered
	}
	if !session.State.Valid() {
		return false, fmt.Errorf("%w: unknown session state %q", ErrInvalid, session.State)
	}
	if session.Type == "" {
		session.Type = SessionTypeUnknown
	}
	if !session.Type.Valid() {
		return false, fmt.Errorf("%w: unknown session type %q", ErrInvalid, session.Type)
	}
	if session.Provider == "" {
		session.Provider = "claude"
	}

	now := s.now().UTC()
	if session.DiscoveredAt.IsZero() {
		session.DiscoveredAt = now
	}
	if session.LastSeenAt.IsZero() {
		session.LastSeenAt = now
	}
	session.UpdatedAt = now

	created := false
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			INSERT INTO sessions (`+sessionColumns+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO NOTHING`,
			session.ID, session.ProjectPath, session.TranscriptPath, session.Provider,
			string(session.Type), session.PID, session.PPID, int64(session.ProcessStartTime),
			string(session.State), boolToInt(session.Enabled), boolToInt(session.Recoverable),
			session.Reason, session.LastError, session.TranscriptSize, session.TranscriptLines,
			timePtrToDB(session.RetryAt), timeToDB(session.DiscoveredAt),
			timeToDB(session.LastSeenAt), timeToDB(session.UpdatedAt),
		)
		if err != nil {
			return fmt.Errorf("insert session %s: %w", session.ID, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("insert session %s: %w", session.ID, err)
		}
		if affected == 1 {
			created = true
			return nil
		}

		_, err = tx.ExecContext(ctx, `
			UPDATE sessions SET
				project_path = ?, transcript_path = ?, provider = ?, session_type = ?,
				pid = ?, ppid = ?, process_start_time = ?,
				recoverable = ?, reason = ?,
				transcript_size = ?, transcript_lines = ?,
				last_seen_at = ?, updated_at = ?
			WHERE id = ?`,
			session.ProjectPath, session.TranscriptPath, session.Provider, string(session.Type),
			session.PID, session.PPID, int64(session.ProcessStartTime),
			boolToInt(session.Recoverable), session.Reason,
			session.TranscriptSize, session.TranscriptLines,
			timeToDB(session.LastSeenAt), timeToDB(session.UpdatedAt), session.ID)
		if err != nil {
			return fmt.Errorf("update session %s: %w", session.ID, err)
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return created, nil
}

// GetSession returns one session, or ErrNotFound.
func (s *Store) GetSession(ctx context.Context, id string) (Session, error) {
	if s == nil || s.db == nil {
		return Session{}, ErrClosed
	}
	if strings.TrimSpace(id) == "" {
		return Session{}, fmt.Errorf("%w: session id is required", ErrInvalid)
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id = ?`, id)
	session, err := scanSession(row)
	if err != nil {
		return Session{}, wrapNotFound(err, fmt.Sprintf("session %s", id))
	}
	return session, nil
}

// ListSessions returns sessions matching the filter, oldest activity first.
func (s *Store) ListSessions(ctx context.Context, filter SessionFilter) ([]Session, error) {
	if s == nil || s.db == nil {
		return nil, ErrClosed
	}
	var (
		where []string
		args  []any
	)
	if filter.ID != "" {
		where = append(where, "id = ?")
		args = append(args, filter.ID)
	}
	if filter.Enabled != nil {
		where = append(where, "enabled = ?")
		args = append(args, boolToInt(*filter.Enabled))
	}
	if filter.Recoverable != nil {
		where = append(where, "recoverable = ?")
		args = append(args, boolToInt(*filter.Recoverable))
	}
	if len(filter.States) > 0 {
		placeholders := make([]string, 0, len(filter.States))
		for _, state := range filter.States {
			if !state.Valid() {
				return nil, fmt.Errorf("%w: unknown session state %q", ErrInvalid, state)
			}
			placeholders = append(placeholders, "?")
			args = append(args, string(state))
		}
		where = append(where, "state IN ("+strings.Join(placeholders, ", ")+")")
	}

	query := `SELECT ` + sessionColumns + ` FROM sessions`
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	if filter.NewestFirst {
		query += " ORDER BY last_seen_at DESC, id ASC"
	} else {
		query += " ORDER BY last_seen_at ASC, id ASC"
	}
	if filter.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", filter.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return sessions, nil
}

// CountSessions counts sessions matching the filter.
func (s *Store) CountSessions(ctx context.Context, filter SessionFilter) (int, error) {
	sessions, err := s.ListSessions(ctx, filter)
	if err != nil {
		return 0, err
	}
	return len(sessions), nil
}

// SetSessionEnabled enables or disables recovery for a session and records the
// transition, so `ccresu enable/disable` is auditable.
func (s *Store) SetSessionEnabled(ctx context.Context, id string, enabled bool) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	return s.InTx(ctx, func(tx *sql.Tx) error {
		current, err := sessionStateTx(ctx, tx, id)
		if err != nil {
			return err
		}
		target := current
		if enabled && current == StateDisabled {
			// Re-enabling a disabled session returns it to DISCOVERED so the
			// scheduler re-evaluates it instead of trusting stale history.
			target = StateDiscovered
		}
		if !enabled {
			target = StateDisabled
		}
		now := timeToDB(s.now().UTC())
		// A pending retry belongs to RETRY_WAIT, so disabling a waiting session
		// has to drop the instant with the state. Nothing here can move a
		// session *into* RETRY_WAIT, so any target other than RETRY_WAIT leaves
		// no wait behind; keeping the column would let a disabled session carry
		// a retry instant that nothing will ever act on.
		clearRetry := target != StateRetryWait
		retryAt := "retry_at"
		if clearRetry {
			retryAt = "NULL"
		}
		result, err := tx.ExecContext(ctx,
			`UPDATE sessions SET enabled = ?, state = ?, retry_at = `+retryAt+`, updated_at = ? WHERE id = ?`,
			boolToInt(enabled), string(target), now, id)
		if err != nil {
			return fmt.Errorf("set session %s enabled=%t: %w", id, enabled, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("set session %s enabled: %w", id, err)
		}
		if affected == 0 {
			return fmt.Errorf("%w: session %s", ErrNotFound, id)
		}
		if target != current {
			if err := recordTransitionTx(ctx, tx, Transition{
				SessionID:  id,
				FromState:  current,
				ToState:    target,
				Reason:     enabledReason(enabled),
				OccurredAt: s.now().UTC(),
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

func enabledReason(enabled bool) string {
	if enabled {
		return "session enabled by operator"
	}
	return "session disabled by operator"
}

// SetSessionRetryAt records the instant a retrying session will be tried again:
// the backoff ccresu computed for it, written down rather than re-derived.
//
// Only RETRY_WAIT uses it: every other state either has a Schedule row or is not
// waiting at all. A zero instant clears the column, which is what TransitionSession
// does whenever a session leaves RETRY_WAIT.
func (s *Store) SetSessionRetryAt(ctx context.Context, id string, at time.Time) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("%w: session id is required", ErrInvalid)
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET retry_at = ?, updated_at = ? WHERE id = ?`,
		timeToDB(at), timeToDB(s.now().UTC()), id)
	if err != nil {
		return fmt.Errorf("set session %s retry instant: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set session %s retry instant: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: session %s", ErrNotFound, id)
	}
	return nil
}

// TransitionSession moves a session to a new state and appends the audit row.
//
// It returns the recorded transition. An attemptID of 0 means "not tied to a
// specific recovery attempt".
func (s *Store) TransitionSession(ctx context.Context, id string, to State, reason string, attemptID int64) (Transition, error) {
	if s == nil || s.db == nil {
		return Transition{}, ErrClosed
	}
	if !to.Valid() {
		return Transition{}, fmt.Errorf("%w: unknown session state %q", ErrInvalid, to)
	}
	var transition Transition
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		current, err := sessionStateTx(ctx, tx, id)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		result, err := tx.ExecContext(ctx,
			`UPDATE sessions SET state = ?, updated_at = ? WHERE id = ?`,
			string(to), timeToDB(now), id)
		if err != nil {
			return fmt.Errorf("transition session %s to %s: %w", id, to, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("transition session %s: %w", id, err)
		}
		if affected == 0 {
			return fmt.Errorf("%w: session %s", ErrNotFound, id)
		}
		// The retry wait belongs to RETRY_WAIT and to nothing else: leaving that
		// state means the wait is over, so the column is cleared here rather
		// than left to be stale. Entering RETRY_WAIT leaves it alone, because
		// the caller writes the instant it computed immediately afterwards.
		if to != StateRetryWait {
			if _, err := tx.ExecContext(ctx,
				`UPDATE sessions SET retry_at = NULL WHERE id = ?`, id); err != nil {
				return fmt.Errorf("clear session %s retry instant: %w", id, err)
			}
		}
		transition = Transition{
			SessionID:  id,
			AttemptID:  attemptID,
			FromState:  current,
			ToState:    to,
			Reason:     reason,
			OccurredAt: now,
		}
		return recordTransitionTx(ctx, tx, transition)
	})
	if err != nil {
		return Transition{}, err
	}
	return transition, nil
}

// SetSessionIdentity updates the process identity of a session.
func (s *Store) SetSessionIdentity(ctx context.Context, id string, pid, ppid int, startTime uint64) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET pid = ?, ppid = ?, process_start_time = ?, updated_at = ?
		WHERE id = ?`,
		pid, ppid, int64(startTime), timeToDB(s.now().UTC()), id)
	if err != nil {
		return fmt.Errorf("set session %s identity: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set session %s identity: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: session %s", ErrNotFound, id)
	}
	return nil
}

// SetSessionTranscriptMetrics stores the transcript snapshot used to prove
// growth after a resume.
func (s *Store) SetSessionTranscriptMetrics(ctx context.Context, id, path string, size, lines int64) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE sessions SET transcript_path = ?, transcript_size = ?, transcript_lines = ?, updated_at = ?
		WHERE id = ?`,
		path, size, lines, timeToDB(s.now().UTC()), id)
	if err != nil {
		return fmt.Errorf("set session %s transcript metrics: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set session %s transcript metrics: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: session %s", ErrNotFound, id)
	}
	return nil
}

// SetSessionError records the most recent error text for a session.
func (s *Store) SetSessionError(ctx context.Context, id, message string) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET last_error = ?, updated_at = ? WHERE id = ?`,
		message, timeToDB(s.now().UTC()), id)
	if err != nil {
		return fmt.Errorf("set session %s error: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set session %s error: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: session %s", ErrNotFound, id)
	}
	return nil
}

// DeleteSession removes a session and (by cascade) its dependent rows.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete session %s: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete session %s: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: session %s", ErrNotFound, id)
	}
	return nil
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// scanSession reads one session row.
func scanSession(row scanner) (Session, error) {
	var (
		session                             Session
		sessionType, state                  string
		enabled, recoverable                int64
		processStartTime                    int64
		retryAt                             any
		discoveredAt, lastSeenAt, updatedAt any
	)
	err := row.Scan(
		&session.ID, &session.ProjectPath, &session.TranscriptPath, &session.Provider,
		&sessionType, &session.PID, &session.PPID, &processStartTime,
		&state, &enabled, &recoverable, &session.Reason, &session.LastError,
		&session.TranscriptSize, &session.TranscriptLines, &retryAt,
		&discoveredAt, &lastSeenAt, &updatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Session{}, err
		}
		return Session{}, fmt.Errorf("scan session: %w", err)
	}

	session.Type = SessionType(sessionType)
	session.State = State(state)
	session.Enabled = intToBool(enabled)
	session.Recoverable = intToBool(recoverable)
	session.ProcessStartTime = uint64(processStartTime)
	if session.RetryAt, err = timePtrFromDB(retryAt); err != nil {
		return Session{}, err
	}
	if session.DiscoveredAt, err = timeFromDB(discoveredAt); err != nil {
		return Session{}, err
	}
	if session.LastSeenAt, err = timeFromDB(lastSeenAt); err != nil {
		return Session{}, err
	}
	if session.UpdatedAt, err = timeFromDB(updatedAt); err != nil {
		return Session{}, err
	}
	return session, nil
}

// sessionStateTx reads a session's current state inside a transaction.
func sessionStateTx(ctx context.Context, tx *sql.Tx, id string) (State, error) {
	var state string
	err := tx.QueryRowContext(ctx, `SELECT state FROM sessions WHERE id = ?`, id).Scan(&state)
	if err != nil {
		return "", wrapNotFound(err, fmt.Sprintf("session %s", id))
	}
	return State(state), nil
}
