package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// notificationColumns is the canonical column list for notification queries.
const notificationColumns = `id, session_id, attempt_id, event, dedupe_key, body,
	status, attempts, last_error, created_at, updated_at, sent_at`

// maxNotificationBody bounds the stored message text. Telegram rejects messages
// over 4096 characters, so storing more than a few kilobytes is pointless.
const maxNotificationBody = 4096

// EnqueueNotification records a notification to be delivered and reports whether
// it was newly created.
//
// created is false when the dedupe key already exists: the same logical event
// can be discovered twice (a restart re-reads the transcript), and a duplicate
// alert is exactly what deduplication exists to prevent. The existing row is
// returned unchanged so the caller can inspect its delivery status.
func (s *Store) EnqueueNotification(ctx context.Context, notification Notification) (Notification, bool, error) {
	if s == nil || s.db == nil {
		return Notification{}, false, ErrClosed
	}
	if strings.TrimSpace(notification.DedupeKey) == "" {
		return Notification{}, false, fmt.Errorf("%w: notification dedupe key is required", ErrInvalid)
	}
	if !notification.Event.Valid() {
		return Notification{}, false, fmt.Errorf("%w: unknown notification event %q", ErrInvalid, notification.Event)
	}
	if notification.Status == "" {
		notification.Status = NotificationPending
	}
	if !notification.Status.Valid() {
		return Notification{}, false, fmt.Errorf("%w: unknown notification status %q", ErrInvalid, notification.Status)
	}
	if len(notification.Body) > maxNotificationBody {
		notification.Body = notification.Body[:maxNotificationBody]
	}

	now := s.now().UTC()
	if notification.CreatedAt.IsZero() {
		notification.CreatedAt = now
	}
	notification.UpdatedAt = now

	created := false
	err := s.InTx(ctx, func(tx *sql.Tx) error {
		var sessionID, attemptID any
		if notification.SessionID != "" {
			sessionID = notification.SessionID
		}
		if notification.AttemptID > 0 {
			attemptID = notification.AttemptID
		}
		result, err := tx.ExecContext(ctx, `
			INSERT INTO notifications
				(session_id, attempt_id, event, dedupe_key, body, status, attempts, last_error, created_at, updated_at, sent_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (dedupe_key) DO NOTHING`,
			sessionID, attemptID, string(notification.Event), notification.DedupeKey,
			notification.Body, string(notification.Status), notification.Attempts,
			notification.LastError, timeToDB(notification.CreatedAt),
			timeToDB(notification.UpdatedAt), timeToDB(notification.SentAt))
		if err != nil {
			return fmt.Errorf("enqueue notification %s: %w", notification.DedupeKey, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("enqueue notification %s: %w", notification.DedupeKey, err)
		}
		if affected == 1 {
			created = true
			id, err := result.LastInsertId()
			if err != nil {
				return fmt.Errorf("enqueue notification %s: %w", notification.DedupeKey, err)
			}
			notification.ID = id
			return nil
		}

		existing, err := getNotificationTx(ctx, tx, notification.DedupeKey)
		if err != nil {
			return err
		}
		notification = existing
		return nil
	})
	if err != nil {
		return Notification{}, false, err
	}
	return notification, created, nil
}

// GetNotification returns one notification by ID.
func (s *Store) GetNotification(ctx context.Context, id int64) (Notification, error) {
	if s == nil || s.db == nil {
		return Notification{}, ErrClosed
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+notificationColumns+` FROM notifications WHERE id = ?`, id)
	notification, err := scanNotification(row)
	if err != nil {
		return Notification{}, wrapNotFound(err, fmt.Sprintf("notification %d", id))
	}
	return notification, nil
}

// GetNotificationByKey returns the notification with the given dedupe key.
func (s *Store) GetNotificationByKey(ctx context.Context, key string) (Notification, error) {
	if s == nil || s.db == nil {
		return Notification{}, ErrClosed
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT `+notificationColumns+` FROM notifications WHERE dedupe_key = ?`, key)
	notification, err := scanNotification(row)
	if err != nil {
		return Notification{}, wrapNotFound(err, fmt.Sprintf("notification %q", key))
	}
	return notification, nil
}

// ListPendingNotifications returns queued notifications, oldest first, so the
// sender works through the backlog in order. A limit of zero returns all.
func (s *Store) ListPendingNotifications(ctx context.Context, limit int) ([]Notification, error) {
	if s == nil || s.db == nil {
		return nil, ErrClosed
	}
	return s.listNotifications(ctx, `status = ?`, []any{string(NotificationPending)}, limit)
}

// ListNotificationsForSession returns a session's notifications, newest first.
func (s *Store) ListNotificationsForSession(ctx context.Context, sessionID string, limit int) ([]Notification, error) {
	if s == nil || s.db == nil {
		return nil, ErrClosed
	}
	query := `SELECT ` + notificationColumns + ` FROM notifications WHERE session_id = ?
		ORDER BY created_at DESC, id DESC`
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list notifications for %s: %w", sessionID, err)
	}
	defer rows.Close()

	var notifications []Notification
	for rows.Next() {
		notification, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		notifications = append(notifications, notification)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list notifications for %s: %w", sessionID, err)
	}
	return notifications, nil
}

func (s *Store) listNotifications(ctx context.Context, where string, args []any, limit int) ([]Notification, error) {
	if s == nil || s.db == nil {
		return nil, ErrClosed
	}
	query := `SELECT ` + notificationColumns + ` FROM notifications WHERE ` + where +
		` ORDER BY created_at ASC, id ASC`
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close()

	var notifications []Notification
	for rows.Next() {
		notification, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		notifications = append(notifications, notification)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	return notifications, nil
}

// MarkNotificationSent records successful delivery. It is a no-op when the
// notification was already marked sent, so a retry after a crash cannot double
// count.
func (s *Store) MarkNotificationSent(ctx context.Context, id int64, sentAt time.Time) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	if sentAt.IsZero() {
		sentAt = s.now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE notifications
		SET status = ?, sent_at = ?, updated_at = ?, attempts = attempts + 1, last_error = ''
		WHERE id = ? AND status = ?`,
		string(NotificationSent), timeToDB(sentAt), timeToDB(s.now().UTC()), id,
		string(NotificationPending))
	if err != nil {
		return fmt.Errorf("mark notification %d sent: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark notification %d sent: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: notification %d is not pending", ErrNotFound, id)
	}
	return nil
}

// MarkNotificationFailed records a failed delivery attempt. When the attempt
// budget is exhausted the notification is marked failed; otherwise it stays
// pending so the next sender pass retries it.
func (s *Store) MarkNotificationFailed(ctx context.Context, id int64, message string, status NotificationStatus) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	if status != NotificationPending && status != NotificationFailed && status != NotificationDropped {
		return fmt.Errorf("%w: unsupported failure status %q", ErrInvalid, status)
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE notifications
		SET status = ?, attempts = attempts + 1, last_error = ?, updated_at = ?
		WHERE id = ?`,
		string(status), message, timeToDB(s.now().UTC()), id)
	if err != nil {
		return fmt.Errorf("mark notification %d failed: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark notification %d failed: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: notification %d", ErrNotFound, id)
	}
	return nil
}

// DropNotification marks a notification as deliberately not delivered.
func (s *Store) DropNotification(ctx context.Context, id int64, reason string) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	return s.MarkNotificationFailed(ctx, id, reason, NotificationDropped)
}

func getNotificationTx(ctx context.Context, tx *sql.Tx, key string) (Notification, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT `+notificationColumns+` FROM notifications WHERE dedupe_key = ?`, key)
	return scanNotification(row)
}

// scanNotification reads one notification row.
func scanNotification(row scanner) (Notification, error) {
	var (
		notification Notification
		sessionID    sql.NullString
		attemptID    sql.NullInt64
		event        string
		status       string
		createdAt    any
		updatedAt    any
		sentAt       any
	)
	err := row.Scan(&notification.ID, &sessionID, &attemptID, &event,
		&notification.DedupeKey, &notification.Body, &status, &notification.Attempts,
		&notification.LastError, &createdAt, &updatedAt, &sentAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Notification{}, err
		}
		return Notification{}, fmt.Errorf("scan notification: %w", err)
	}
	notification.SessionID = sessionID.String
	notification.AttemptID = attemptID.Int64
	notification.Event = NotificationEvent(event)
	notification.Status = NotificationStatus(status)
	if notification.CreatedAt, err = timeFromDB(createdAt); err != nil {
		return Notification{}, err
	}
	if notification.UpdatedAt, err = timeFromDB(updatedAt); err != nil {
		return Notification{}, err
	}
	if notification.SentAt, err = timeFromDB(sentAt); err != nil {
		return Notification{}, err
	}
	return notification, nil
}
