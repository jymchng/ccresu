package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// usageLimitColumns is the canonical column list for usage-limit queries.
const usageLimitColumns = `id, session_id, limit_type, raw_message, reset_at,
	reset_timezone, reset_raw, confidence, paused_goal, detected_at`

// RecordUsageLimit inserts a detected usage-limit event and returns it with its
// assigned ID.
func (s *Store) RecordUsageLimit(ctx context.Context, limit UsageLimit) (UsageLimit, error) {
	if s == nil || s.db == nil {
		return UsageLimit{}, ErrClosed
	}
	if strings.TrimSpace(limit.SessionID) == "" {
		return UsageLimit{}, fmt.Errorf("%w: usage limit session id is required", ErrInvalid)
	}
	if limit.LimitType == "" {
		limit.LimitType = LimitTypeOther
	}
	if !limit.LimitType.Valid() {
		return UsageLimit{}, fmt.Errorf("%w: unknown limit type %q", ErrInvalid, limit.LimitType)
	}
	if limit.Confidence < 0 || limit.Confidence > 1 {
		return UsageLimit{}, fmt.Errorf("%w: confidence %v is outside [0,1]", ErrInvalid, limit.Confidence)
	}
	if limit.DetectedAt.IsZero() {
		limit.DetectedAt = s.now().UTC()
	}

	result, err := s.db.ExecContext(ctx, `
		INSERT INTO usage_limits
			(session_id, limit_type, raw_message, reset_at, reset_timezone, reset_raw, confidence, paused_goal, detected_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		limit.SessionID, string(limit.LimitType), limit.RawMessage,
		timeToDB(limit.ResetAt), limit.ResetTimezone, limit.ResetRaw,
		limit.Confidence, boolToInt(limit.PausedGoal), timeToDB(limit.DetectedAt))
	if err != nil {
		return UsageLimit{}, fmt.Errorf("record usage limit for %s: %w", limit.SessionID, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return UsageLimit{}, fmt.Errorf("record usage limit for %s: %w", limit.SessionID, err)
	}
	limit.ID = id
	return limit, nil
}

// GetUsageLimit returns one usage-limit event by ID.
func (s *Store) GetUsageLimit(ctx context.Context, id int64) (UsageLimit, error) {
	if s == nil || s.db == nil {
		return UsageLimit{}, ErrClosed
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+usageLimitColumns+` FROM usage_limits WHERE id = ?`, id)
	limit, err := scanUsageLimit(row)
	if err != nil {
		return UsageLimit{}, wrapNotFound(err, fmt.Sprintf("usage limit %d", id))
	}
	return limit, nil
}

// LatestUsageLimit returns the newest usage-limit event for a session.
func (s *Store) LatestUsageLimit(ctx context.Context, sessionID string) (UsageLimit, error) {
	if s == nil || s.db == nil {
		return UsageLimit{}, ErrClosed
	}
	limits, err := s.ListUsageLimits(ctx, sessionID, 1)
	if err != nil {
		return UsageLimit{}, err
	}
	if len(limits) == 0 {
		return UsageLimit{}, fmt.Errorf("%w: no usage limits for session %s", ErrNotFound, sessionID)
	}
	return limits[0], nil
}

// ListUsageLimits returns a session's usage-limit events, newest first.
func (s *Store) ListUsageLimits(ctx context.Context, sessionID string, limit int) ([]UsageLimit, error) {
	if s == nil || s.db == nil {
		return nil, ErrClosed
	}
	query := `SELECT ` + usageLimitColumns + ` FROM usage_limits WHERE session_id = ?
		ORDER BY detected_at DESC, id DESC`
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list usage limits for %s: %w", sessionID, err)
	}
	defer rows.Close()

	var limits []UsageLimit
	for rows.Next() {
		limit, err := scanUsageLimit(rows)
		if err != nil {
			return nil, err
		}
		limits = append(limits, limit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list usage limits for %s: %w", sessionID, err)
	}
	return limits, nil
}

// SetUsageLimitResetAt attaches a resolved reset instant to an event, for the
// case where the reset time was learned later (polling or a follow-up message).
// A zero resetAt clears the stored value.
func (s *Store) SetUsageLimitResetAt(ctx context.Context, id int64, resetAt time.Time, timezone, raw string) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	result, err := s.db.ExecContext(ctx,
		`UPDATE usage_limits SET reset_at = ?, reset_timezone = ?, reset_raw = ? WHERE id = ?`,
		timeToDB(resetAt), timezone, raw, id)
	if err != nil {
		return fmt.Errorf("set usage limit %d reset: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("set usage limit %d reset: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: usage limit %d", ErrNotFound, id)
	}
	return nil
}

// scanUsageLimit reads one usage-limit row.
func scanUsageLimit(row scanner) (UsageLimit, error) {
	var (
		limit      UsageLimit
		limitType  string
		resetAt    any
		pausedGoal int64
		detectedAt any
	)
	err := row.Scan(&limit.ID, &limit.SessionID, &limitType, &limit.RawMessage, &resetAt,
		&limit.ResetTimezone, &limit.ResetRaw, &limit.Confidence, &pausedGoal, &detectedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UsageLimit{}, err
		}
		return UsageLimit{}, fmt.Errorf("scan usage limit: %w", err)
	}
	limit.LimitType = LimitType(limitType)
	limit.PausedGoal = intToBool(pausedGoal)
	if limit.ResetAt, err = timeFromDB(resetAt); err != nil {
		return UsageLimit{}, err
	}
	if limit.DetectedAt, err = timeFromDB(detectedAt); err != nil {
		return UsageLimit{}, err
	}
	return limit, nil
}
