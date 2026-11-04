package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// attemptColumns is the canonical column list for attempt queries.
const attemptColumns = `id, session_id, usage_limit_id, attempt_number, state, command,
	started_at, finished_at, success, output, error, session_id_verified, transcript_grew`

// maxAttemptAllocationRetries bounds the retry loop used when two goroutines
// allocate the same attempt number concurrently. The unique index on
// (session_id, attempt_number) is the arbiter; contention resolves in one retry.
const maxAttemptAllocationRetries = 8

// StartAttempt allocates the next attempt number for a session and records a
// running attempt.
//
// Numbering is done inside a transaction and guarded by the unique index
// UNIQUE(session_id, attempt_number), so concurrent callers can never produce
// two attempts with the same number even across processes.
func (s *Store) StartAttempt(ctx context.Context, sessionID string, usageLimitID int64, command string) (RecoveryAttempt, error) {
	if s == nil || s.db == nil {
		return RecoveryAttempt{}, ErrClosed
	}
	if strings.TrimSpace(sessionID) == "" {
		return RecoveryAttempt{}, fmt.Errorf("%w: attempt session id is required", ErrInvalid)
	}

	var lastErr error
	for i := 0; i < maxAttemptAllocationRetries; i++ {
		attempt, err := s.tryStartAttempt(ctx, sessionID, usageLimitID, command)
		if err == nil {
			return attempt, nil
		}
		lastErr = err
		if !isUniqueViolation(err) {
			return RecoveryAttempt{}, err
		}
	}
	return RecoveryAttempt{}, fmt.Errorf("allocate attempt for %s after %d retries: %w",
		sessionID, maxAttemptAllocationRetries, lastErr)
}

func (s *Store) tryStartAttempt(ctx context.Context, sessionID string, usageLimitID int64, command string) (RecoveryAttempt, error) {
	var attempt RecoveryAttempt
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		var next int
		err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(attempt_number), 0) + 1 FROM recovery_attempts WHERE session_id = ?`,
			sessionID).Scan(&next)
		if err != nil {
			return fmt.Errorf("allocate attempt number for %s: %w", sessionID, err)
		}
		if next < 1 {
			next = 1
		}

		now := s.now().UTC()
		attempt = RecoveryAttempt{
			SessionID:     sessionID,
			UsageLimitID:  usageLimitID,
			AttemptNumber: next,
			State:         StateResuming,
			Command:       command,
			StartedAt:     now,
		}
		var limitID any
		if usageLimitID > 0 {
			limitID = usageLimitID
		}
		result, err := tx.ExecContext(ctx, `
			INSERT INTO recovery_attempts
				(session_id, usage_limit_id, attempt_number, state, command, started_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			sessionID, limitID, next, string(attempt.State), command, timeToDB(now))
		if err != nil {
			return fmt.Errorf("insert attempt %d for %s: %w", next, sessionID, err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			return fmt.Errorf("insert attempt %d for %s: %w", next, sessionID, err)
		}
		attempt.ID = id
		return nil
	})
	if err != nil {
		return RecoveryAttempt{}, err
	}
	return attempt, nil
}

// isUniqueViolation reports whether err is a SQLite UNIQUE constraint failure.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique constraint failed") || strings.Contains(message, "constraint failed: unique")
}

// AttemptResult is the outcome of a finished attempt.
type AttemptResult struct {
	// State is the state the session moved to as a result of the attempt.
	State State
	// Success reports whether the resume worked.
	Success bool
	// Output is the captured CLI output (truncated by the caller).
	Output string
	// Error is the failure text, empty on success.
	Error string
	// SessionIDVerified reports whether the resume kept the original session ID.
	SessionIDVerified *bool
	// TranscriptGrew reports whether the transcript grew, the cross-platform
	// proof that the conversation continued.
	TranscriptGrew *bool
	// FinishedAt overrides the finish timestamp; zero means "now".
	FinishedAt time.Time
}

// FinishAttempt records the outcome of an attempt, atomically.
func (s *Store) FinishAttempt(ctx context.Context, attemptID int64, result AttemptResult) (RecoveryAttempt, error) {
	if s == nil || s.db == nil {
		return RecoveryAttempt{}, ErrClosed
	}
	if !result.State.Valid() {
		return RecoveryAttempt{}, fmt.Errorf("%w: unknown attempt result state %q", ErrInvalid, result.State)
	}
	finishedAt := result.FinishedAt
	if finishedAt.IsZero() {
		finishedAt = s.now().UTC()
	}

	var attempt RecoveryAttempt
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		update, err := tx.ExecContext(ctx, `
			UPDATE recovery_attempts SET
				state = ?, finished_at = ?, success = ?, output = ?, error = ?,
				session_id_verified = ?, transcript_grew = ?
			WHERE id = ?`,
			string(result.State), timeToDB(finishedAt), boolToInt(result.Success),
			result.Output, result.Error, nullableBool(result.SessionIDVerified),
			nullableBool(result.TranscriptGrew), attemptID)
		if err != nil {
			return fmt.Errorf("finish attempt %d: %w", attemptID, err)
		}
		affected, err := update.RowsAffected()
		if err != nil {
			return fmt.Errorf("finish attempt %d: %w", attemptID, err)
		}
		if affected == 0 {
			return fmt.Errorf("%w: attempt %d", ErrNotFound, attemptID)
		}
		attempt, err = getAttemptTx(ctx, tx, attemptID)
		return err
	})
	if err != nil {
		return RecoveryAttempt{}, err
	}
	return attempt, nil
}

// GetAttempt returns one attempt by ID.
func (s *Store) GetAttempt(ctx context.Context, id int64) (RecoveryAttempt, error) {
	if s == nil || s.db == nil {
		return RecoveryAttempt{}, ErrClosed
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+attemptColumns+` FROM recovery_attempts WHERE id = ?`, id)
	attempt, err := scanAttempt(row)
	if err != nil {
		return RecoveryAttempt{}, wrapNotFound(err, fmt.Sprintf("attempt %d", id))
	}
	return attempt, nil
}

// ListAttempts returns a session's attempts, oldest first. A limit of zero
// returns everything.
func (s *Store) ListAttempts(ctx context.Context, sessionID string, limit int) ([]RecoveryAttempt, error) {
	if s == nil || s.db == nil {
		return nil, ErrClosed
	}
	query := `SELECT ` + attemptColumns + ` FROM recovery_attempts WHERE session_id = ?
		ORDER BY attempt_number ASC`
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list attempts for %s: %w", sessionID, err)
	}
	defer rows.Close()

	var attempts []RecoveryAttempt
	for rows.Next() {
		attempt, err := scanAttempt(rows)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list attempts for %s: %w", sessionID, err)
	}
	return attempts, nil
}

// CountAttempts returns how many attempts a session has used, which is what the
// retry budget is checked against.
func (s *Store) CountAttempts(ctx context.Context, sessionID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, ErrClosed
	}
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM recovery_attempts WHERE session_id = ?`, sessionID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count attempts for %s: %w", sessionID, err)
	}
	return count, nil
}

// LatestAttempt returns the most recent attempt for a session.
func (s *Store) LatestAttempt(ctx context.Context, sessionID string) (RecoveryAttempt, error) {
	if s == nil || s.db == nil {
		return RecoveryAttempt{}, ErrClosed
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+attemptColumns+`
		FROM recovery_attempts WHERE session_id = ? ORDER BY attempt_number DESC LIMIT 1`, sessionID)
	attempt, err := scanAttempt(row)
	if err != nil {
		return RecoveryAttempt{}, wrapNotFound(err, fmt.Sprintf("attempt for session %s", sessionID))
	}
	return attempt, nil
}

// DeleteAttempt removes one attempt.
func (s *Store) DeleteAttempt(ctx context.Context, id int64) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM recovery_attempts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete attempt %d: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete attempt %d: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: attempt %d", ErrNotFound, id)
	}
	return nil
}

func getAttemptTx(ctx context.Context, tx *sql.Tx, id int64) (RecoveryAttempt, error) {
	row := tx.QueryRowContext(ctx, `SELECT `+attemptColumns+` FROM recovery_attempts WHERE id = ?`, id)
	return scanAttempt(row)
}

// scanAttempt reads one attempt row.
func scanAttempt(row scanner) (RecoveryAttempt, error) {
	var (
		attempt        RecoveryAttempt
		usageLimitID   sql.NullInt64
		state          string
		finishedAt     any
		success        sql.NullInt64
		verified       sql.NullInt64
		transcriptGrew sql.NullInt64
		startedAt      any
	)
	err := row.Scan(&attempt.ID, &attempt.SessionID, &usageLimitID, &attempt.AttemptNumber,
		&state, &attempt.Command, &startedAt, &finishedAt, &success, &attempt.Output,
		&attempt.Error, &verified, &transcriptGrew)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RecoveryAttempt{}, err
		}
		return RecoveryAttempt{}, fmt.Errorf("scan attempt: %w", err)
	}
	attempt.UsageLimitID = usageLimitID.Int64
	attempt.State = State(state)
	if attempt.StartedAt, err = timeFromDB(startedAt); err != nil {
		return RecoveryAttempt{}, err
	}
	if attempt.FinishedAt, err = timeFromDB(finishedAt); err != nil {
		return RecoveryAttempt{}, err
	}
	if success.Valid {
		value := success.Int64 != 0
		attempt.Success = &value
	}
	attempt.SessionIDVerified = scanNullableBool(verified)
	attempt.TranscriptGrew = scanNullableBool(transcriptGrew)
	return attempt, nil
}
