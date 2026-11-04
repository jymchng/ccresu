package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// scheduleColumns is the canonical column list for schedule queries.
const scheduleColumns = `session_id, usage_limit_id, reset_at, reset_timezone,
	reset_raw, unknown_reset, attempts, next_poll_at, created_at, updated_at`

// SaveSchedule inserts or replaces the pending schedule for a session.
//
// It is an upsert because a session can hit its limit more than once: the new
// wait replaces the stale one rather than accumulating rows that would each
// wake the same session.
func (s *Store) SaveSchedule(ctx context.Context, sched Schedule) (Schedule, error) {
	if s == nil || s.db == nil {
		return Schedule{}, ErrClosed
	}
	if strings.TrimSpace(sched.SessionID) == "" {
		return Schedule{}, fmt.Errorf("%w: schedule session id is required", ErrInvalid)
	}
	if !sched.Unknown && sched.ResetAt.IsZero() {
		return Schedule{}, fmt.Errorf("%w: schedule %s has neither a reset time nor the unknown flag",
			ErrInvalid, sched.SessionID)
	}
	if sched.Unknown && !sched.ResetAt.IsZero() {
		return Schedule{}, fmt.Errorf("%w: schedule %s is marked unknown but carries a reset time",
			ErrInvalid, sched.SessionID)
	}
	if sched.Attempts < 0 {
		return Schedule{}, fmt.Errorf("%w: schedule %s has negative attempt count", ErrInvalid, sched.SessionID)
	}

	now := s.now().UTC()
	if sched.CreatedAt.IsZero() {
		sched.CreatedAt = now
	}
	sched.UpdatedAt = now

	var usageLimitID any
	if sched.UsageLimitID != 0 {
		usageLimitID = sched.UsageLimitID
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO schedules (`+scheduleColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (session_id) DO UPDATE SET
			usage_limit_id = excluded.usage_limit_id,
			reset_at       = excluded.reset_at,
			reset_timezone = excluded.reset_timezone,
			reset_raw      = excluded.reset_raw,
			unknown_reset  = excluded.unknown_reset,
			attempts       = excluded.attempts,
			next_poll_at   = excluded.next_poll_at,
			updated_at     = excluded.updated_at`,
		sched.SessionID, usageLimitID, timeToDB(sched.ResetAt), sched.ResetTimezone,
		sched.ResetRaw, boolToInt(sched.Unknown), sched.Attempts,
		timeToDB(sched.NextPollAt), timeToDB(sched.CreatedAt), timeToDB(sched.UpdatedAt))
	if err != nil {
		return Schedule{}, fmt.Errorf("save schedule for %s: %w", sched.SessionID, err)
	}
	return sched, nil
}

// GetSchedule returns the pending schedule for one session, or ErrNotFound.
func (s *Store) GetSchedule(ctx context.Context, sessionID string) (Schedule, error) {
	if s == nil || s.db == nil {
		return Schedule{}, ErrClosed
	}
	if strings.TrimSpace(sessionID) == "" {
		return Schedule{}, fmt.Errorf("%w: session id is required", ErrInvalid)
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+scheduleColumns+` FROM schedules WHERE session_id = ?`, sessionID)
	sched, err := scanSchedule(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Schedule{}, fmt.Errorf("%w: schedule for session %s", ErrNotFound, sessionID)
		}
		return Schedule{}, err
	}
	return sched, nil
}

// LoadSchedules returns every pending schedule, earliest deadline first.
//
// Rows with no deadline at all (neither a reset time nor a poll time) are
// excluded: they can never become due, so honouring them would stall the
// scheduler forever.
func (s *Store) LoadSchedules(ctx context.Context) ([]Schedule, error) {
	if s == nil || s.db == nil {
		return nil, ErrClosed
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+scheduleColumns+` FROM schedules
		WHERE COALESCE(reset_at, next_poll_at) IS NOT NULL
		ORDER BY COALESCE(reset_at, next_poll_at) ASC, session_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("load schedules: %w", err)
	}
	defer rows.Close()

	var schedules []Schedule
	for rows.Next() {
		sched, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		schedules = append(schedules, sched)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load schedules: %w", err)
	}
	return schedules, nil
}

// DeleteSchedule removes the pending schedule for a session. Removing a
// schedule that does not exist is not an error: the wait is over either way.
func (s *Store) DeleteSchedule(ctx context.Context, sessionID string) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("%w: session id is required", ErrInvalid)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM schedules WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("delete schedule for %s: %w", sessionID, err)
	}
	return nil
}

// scanSchedule reads one schedule row.
func scanSchedule(row scanner) (Schedule, error) {
	var (
		sched        Schedule
		usageLimitID sql.NullInt64
		resetAt      any
		nextPollAt   any
		createdAt    any
		updatedAt    any
		unknown      int64
	)
	err := row.Scan(&sched.SessionID, &usageLimitID, &resetAt, &sched.ResetTimezone,
		&sched.ResetRaw, &unknown, &sched.Attempts, &nextPollAt, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Schedule{}, err
		}
		return Schedule{}, fmt.Errorf("scan schedule: %w", err)
	}
	if usageLimitID.Valid {
		sched.UsageLimitID = usageLimitID.Int64
	}
	sched.Unknown = intToBool(unknown)
	for _, field := range []struct {
		name  string
		value any
		dest  *time.Time
	}{
		{"reset_at", resetAt, &sched.ResetAt},
		{"next_poll_at", nextPollAt, &sched.NextPollAt},
		{"created_at", createdAt, &sched.CreatedAt},
		{"updated_at", updatedAt, &sched.UpdatedAt},
	} {
		parsed, err := timeFromDB(field.value)
		if err != nil {
			return Schedule{}, fmt.Errorf("schedule %s: %s: %w", sched.SessionID, field.name, err)
		}
		*field.dest = parsed
	}
	return sched, nil
}
