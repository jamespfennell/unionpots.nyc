package db

import (
	"context"
	"database/sql"
	"errors"
)

// AppText returns a named piece of free text kept by the app (e.g. "ideas"),
// or "" if it hasn't been written yet.
func (s *Store) AppText(ctx context.Context, name string) (string, error) {
	var text string
	err := s.DB.QueryRowContext(ctx, "SELECT text FROM app_text WHERE name = ?", name).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return text, err
}

// SetAppText saves a named piece of free text.
func (s *Store) SetAppText(ctx context.Context, name, text string) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO app_text (name, text, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET text = excluded.text, updated_at = excluded.updated_at`,
		name, text, now())
	return err
}
