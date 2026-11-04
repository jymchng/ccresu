package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// recordTransitionTx appends one audit row. The caller must hold a transaction.
func recordTransitionTx(ctx context.Context, tx *sql.Tx, transition Transition) error {
	if !transition.ToState.Valid() {
		return fmt.Errorf("%w: unknown transition target state %q", ErrInvalid, transition.ToState)
	}
	if transition.FromState != "" && !transition.FromState.Valid() {
		return fmt.Errorf("%w: unknown transition source state %q", ErrInvalid, transition.FromState)
	}
	occurredAt := transition.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}
	var attemptID any
	if transition.AttemptID > 0 {
		attemptID = transition.AttemptID
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO state_transitions (session_id, attempt_id, from_state, to_state, reason, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		transition.SessionID, attemptID, string(transition.FromState), string(transition.ToState),
		transition.Reason, timeToDB(occurredAt))
	if err != nil {
		return fmt.Errorf("record transition %s->%s for %s: %w",
			transition.FromState, transition.ToState, transition.SessionID, err)
	}
	return nil
}

// RecordTransition appends a state-history row without changing the session's
// current state. Use it for events observed outside a transition (for example
// a note that a reset time was reached while the session was already waiting).
func (s *Store) RecordTransition(ctx context.Context, transition Transition) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	if transition.SessionID == "" {
		return fmt.Errorf("%w: transition session id is required", ErrInvalid)
	}
	if transition.OccurredAt.IsZero() {
		transition.OccurredAt = s.now().UTC()
	}
	return s.InTx(ctx, func(tx *sql.Tx) error {
		return recordTransitionTx(ctx, tx, transition)
	})
}

// ListTransitions returns a session's state history, oldest first. A limit of
// zero returns everything.
func (s *Store) ListTransitions(ctx context.Context, sessionID string, limit int) ([]Transition, error) {
	if s == nil || s.db == nil {
		return nil, ErrClosed
	}
	query := `SELECT id, session_id, attempt_id, from_state, to_state, reason, occurred_at
		FROM state_transitions WHERE session_id = ? ORDER BY occurred_at ASC, id ASC`
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list transitions for %s: %w", sessionID, err)
	}
	defer rows.Close()

	var transitions []Transition
	for rows.Next() {
		var (
			transition Transition
			attemptID  sql.NullInt64
			fromState  string
			toState    string
			occurredAt any
		)
		if err := rows.Scan(&transition.ID, &transition.SessionID, &attemptID,
			&fromState, &toState, &transition.Reason, &occurredAt); err != nil {
			return nil, fmt.Errorf("scan transition: %w", err)
		}
		transition.AttemptID = attemptID.Int64
		transition.FromState = State(fromState)
		transition.ToState = State(toState)
		if transition.OccurredAt, err = timeFromDB(occurredAt); err != nil {
			return nil, err
		}
		transitions = append(transitions, transition)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate transitions: %w", err)
	}
	return transitions, nil
}

// ListTransitionsSince returns the transitions appended after sinceID, oldest
// first, across every session.
//
// It exists so a streaming consumer can follow the state machine without
// polling each session in turn: the transition id is monotonic, so a caller
// remembers the highest id it has seen and asks for everything above it. A limit
// of zero returns everything, exactly like the per-session query.
func (s *Store) ListTransitionsSince(ctx context.Context, sinceID int64, limit int) ([]Transition, error) {
	if s == nil || s.db == nil {
		return nil, ErrClosed
	}
	query := `SELECT id, session_id, attempt_id, from_state, to_state, reason, occurred_at
		FROM state_transitions WHERE id > ? ORDER BY id ASC`
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, query, sinceID)
	if err != nil {
		return nil, fmt.Errorf("list transitions since %d: %w", sinceID, err)
	}
	defer rows.Close()

	var transitions []Transition
	for rows.Next() {
		var (
			transition Transition
			attemptID  sql.NullInt64
			fromState  string
			toState    string
			occurredAt any
		)
		if err := rows.Scan(&transition.ID, &transition.SessionID, &attemptID,
			&fromState, &toState, &transition.Reason, &occurredAt); err != nil {
			return nil, fmt.Errorf("scan transition: %w", err)
		}
		transition.AttemptID = attemptID.Int64
		transition.FromState = State(fromState)
		transition.ToState = State(toState)
		if transition.OccurredAt, err = timeFromDB(occurredAt); err != nil {
			return nil, err
		}
		transitions = append(transitions, transition)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate transitions: %w", err)
	}
	return transitions, nil
}

// ListTransitionsForAttempt returns the history of one recovery attempt.
func (s *Store) ListTransitionsForAttempt(ctx context.Context, attemptID int64) ([]Transition, error) {
	if s == nil || s.db == nil {
		return nil, ErrClosed
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, session_id, attempt_id, from_state, to_state, reason, occurred_at
		FROM state_transitions WHERE attempt_id = ? ORDER BY occurred_at ASC, id ASC`, attemptID)
	if err != nil {
		return nil, fmt.Errorf("list transitions for attempt %d: %w", attemptID, err)
	}
	defer rows.Close()

	var transitions []Transition
	for rows.Next() {
		var (
			transition Transition
			attempt    sql.NullInt64
			fromState  string
			toState    string
			occurredAt any
		)
		if err := rows.Scan(&transition.ID, &transition.SessionID, &attempt,
			&fromState, &toState, &transition.Reason, &occurredAt); err != nil {
			return nil, fmt.Errorf("scan transition: %w", err)
		}
		transition.AttemptID = attempt.Int64
		transition.FromState = State(fromState)
		transition.ToState = State(toState)
		if transition.OccurredAt, err = timeFromDB(occurredAt); err != nil {
			return nil, err
		}
		transitions = append(transitions, transition)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate transitions: %w", err)
	}
	return transitions, nil
}
