package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Well-known setting keys. Keeping them here prevents typo drift between the
// daemon, the CLI and the API.
const (
	// SettingGlobalEnabled is "true" when the daemon may recover sessions at all.
	SettingGlobalEnabled = "global_enabled"
	// SettingTelegramEnabled mirrors telegram.enabled at runtime.
	SettingTelegramEnabled = "telegram_enabled"
	// SettingAPITokenFingerprint stores a SHA-256 of the API bearer token so the
	// token itself is never persisted in the database.
	SettingAPITokenFingerprint = "api_token_fingerprint"
)

// GetSetting returns a setting value and whether it exists.
func (s *Store) GetSetting(ctx context.Context, key string) (string, bool, error) {
	if s == nil || s.db == nil {
		return "", false, ErrClosed
	}
	if strings.TrimSpace(key) == "" {
		return "", false, fmt.Errorf("%w: setting key is required", ErrInvalid)
	}
	var value string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("get setting %s: %w", key, err)
	}
	return value, true, nil
}

// SetSetting inserts or updates a setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("%w: setting key is required", ErrInvalid)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, timeToDB(s.now().UTC()))
	if err != nil {
		return fmt.Errorf("set setting %s: %w", key, err)
	}
	return nil
}

// DeleteSetting removes a setting.
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	if s == nil || s.db == nil {
		return ErrClosed
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("delete setting %s: %w", key, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete setting %s: %w", key, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: setting %s", ErrNotFound, key)
	}
	return nil
}

// ListSettings returns every setting, ordered by key.
func (s *Store) ListSettings(ctx context.Context) ([]Setting, error) {
	if s == nil || s.db == nil {
		return nil, ErrClosed
	}
	rows, err := s.db.QueryContext(ctx, `SELECT key, value, updated_at FROM settings ORDER BY key ASC`)
	if err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	defer rows.Close()

	var settings []Setting
	for rows.Next() {
		var (
			setting   Setting
			updatedAt any
		)
		if err := rows.Scan(&setting.Key, &setting.Value, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan setting: %w", err)
		}
		if setting.UpdatedAt, err = timeFromDB(updatedAt); err != nil {
			return nil, err
		}
		settings = append(settings, setting)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list settings: %w", err)
	}
	return settings, nil
}
