package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// attentionColumns is the canonical column list for attention-episode queries.
const attentionColumns = `session_id, started_at, reason`

// AttentionEpisode is one stretch during which a session was waiting for a human
// (PRD 4, §6.3).
//
// It exists only to give the `needs_input` notification an identity. The
// session's own state says the same thing, but the hook that reports a block may
// run before any pass has written that state, and the hook may not write it —
// so the anchor both producers can agree on is kept here.
type AttentionEpisode struct {
	// SessionID is the session that is waiting.
	SessionID string
	// StartedAt is when the episode began, as the first observer saw it. It is
	// persisted so a restart mid-episode cannot announce the same block twice.
	StartedAt time.Time
	// Reason is the reason reported when the episode opened, verbatim. Empty
	// means none was reported, which is worth keeping rather than inferring.
	Reason string
}

// BeginAttentionEpisode records that a session is waiting for a human and returns
// the instant the episode began.
//
// It is idempotent on purpose: the hook and the recovery pass both call it for
// the same condition, and the first caller's instant must win. The second call
// returns the stored instant rather than overwriting it, which is what makes the
// two producers compute the same dedupe key.
//
// at is the caller's idea of "now"; the zero value means the store's clock.
func (s *Store) BeginAttentionEpisode(ctx context.Context, sessionID, reason string, at time.Time) (time.Time, error) {
	if s == nil || s.db == nil {
		return time.Time{}, ErrClosed
	}
	if sessionID == "" {
		return time.Time{}, fmt.Errorf("%w: attention episode session id is required", ErrInvalid)
	}
	if at.IsZero() {
		at = s.now().UTC()
	}
	at = at.UTC().Truncate(time.Second)

	err := s.InTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO attention_episodes (session_id, started_at, reason)
			VALUES (?, ?, ?)
			ON CONFLICT (session_id) DO NOTHING`,
			sessionID, timeToDB(at), reason)
		if err != nil {
			return fmt.Errorf("begin attention episode for %s: %w", sessionID, err)
		}
		return nil
	})
	if err != nil {
		return time.Time{}, err
	}

	episode, err := s.GetAttentionEpisode(ctx, sessionID)
	if err != nil {
		return time.Time{}, err
	}
	return episode.StartedAt, nil
}

// GetAttentionEpisode returns the session's current episode.
//
// A session that is not waiting reports ErrNotFound, which is a fact rather than
// a failure: it means no episode is open.
func (s *Store) GetAttentionEpisode(ctx context.Context, sessionID string) (AttentionEpisode, error) {
	if s == nil || s.db == nil {
		return AttentionEpisode{}, ErrClosed
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+attentionColumns+` FROM attention_episodes WHERE session_id = ?`, sessionID)
	var (
		episode   AttentionEpisode
		startedAt any
	)
	if err := row.Scan(&episode.SessionID, &startedAt, &episode.Reason); err != nil {
		return AttentionEpisode{}, wrapNotFound(err, fmt.Sprintf("attention episode for %s", sessionID))
	}
	parsed, err := timeFromDB(startedAt)
	if err != nil {
		return AttentionEpisode{}, err
	}
	episode.StartedAt = parsed
	return episode, nil
}

// EndAttentionEpisode closes a session's episode, so the next block of the same
// session is announced as a new one.
func (s *Store) EndAttentionEpisode(ctx context.Context, sessionID string) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM attention_episodes WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("end attention episode for %s: %w", sessionID, err)
	}
	return nil
}

// PruneAttentionEpisodes closes every episode whose session is no longer waiting.
//
// It is one statement per pass rather than a hook on the transition writer,
// because "waiting for a human" is a property of the session's current state and
// every path that changes that state — a recovery, an operator's `disable`, a
// discovery pass — already writes it. Reading the end of an episode from the same
// state the episode is about keeps the two from disagreeing.
//
// It returns how many episodes it closed.
func (s *Store) PruneAttentionEpisodes(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, ErrClosed
	}
	result, err := s.db.ExecContext(ctx, `
		DELETE FROM attention_episodes
		WHERE session_id NOT IN (
			SELECT id FROM sessions WHERE state = ?
		)`, string(StateWaitingForInput))
	if err != nil {
		return 0, fmt.Errorf("prune attention episodes: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune attention episodes: %w", err)
	}
	return affected, nil
}

// ErrNoAttentionEpisode reports that no episode is open for a session. It is a
// convenience for callers that ask rather than begin, and it wraps ErrNotFound so
// one errors.Is covers both.
var ErrNoAttentionEpisode = fmt.Errorf("%w: no attention episode is open", ErrNotFound)

// AttentionEpisodeStart reports when the session's current episode began, and
// whether one is open at all.
func (s *Store) AttentionEpisodeStart(ctx context.Context, sessionID string) (time.Time, bool, error) {
	episode, err := s.GetAttentionEpisode(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, err
	}
	return episode.StartedAt, true, nil
}
