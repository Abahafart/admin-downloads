package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// GetSetting returns the value stored under key, or ErrNotFound.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("setting %q: %w", key, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("store: get setting %q: %w", key, err)
	}
	return v, nil
}

// SetSetting creates or replaces a setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, TimeToDB(time.Now()))
	if err != nil {
		return fmt.Errorf("store: set setting %q: %w", key, err)
	}
	return nil
}

// DeleteSetting removes a setting; deleting a missing key is not an error.
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
		return fmt.Errorf("store: delete setting %q: %w", key, err)
	}
	return nil
}
