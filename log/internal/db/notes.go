package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Note is one of the general notes (menu → Notes).
type Note struct {
	ID        int64
	Title     string
	Text      string
	CreatedAt string
	UpdatedAt string
}

// Updated is when the note was last edited.
func (n Note) Updated() time.Time {
	t, _ := time.Parse(time.RFC3339Nano, n.UpdatedAt)
	return t
}

// Notes returns all notes, most recently edited first.
func (s *Store) Notes(ctx context.Context) ([]Note, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, title, text, created_at, updated_at FROM notes
		ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ns []Note
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.ID, &n.Title, &n.Text, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		ns = append(ns, n)
	}
	return ns, rows.Err()
}

func (s *Store) GetNote(ctx context.Context, id int64) (Note, error) {
	var n Note
	err := s.DB.QueryRowContext(ctx, "SELECT id, title, text, created_at, updated_at FROM notes WHERE id = ?", id).
		Scan(&n.ID, &n.Title, &n.Text, &n.CreatedAt, &n.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return n, ErrNotFound
	}
	return n, err
}

// AddNote adds an empty note with this title.
func (s *Store) AddNote(ctx context.Context, title string) (int64, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return 0, userErr("a note needs a title")
	}
	ts := now()
	res, err := s.DB.ExecContext(ctx, "INSERT INTO notes (title, created_at, updated_at) VALUES (?, ?, ?)", title, ts, ts)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateNote changes a note's title and text. Its edit time only moves if
// something changed, so a save with no changes doesn't reorder the notes.
func (s *Store) UpdateNote(ctx context.Context, id int64, title, text string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return userErr("a note needs a title")
	}
	res, err := s.DB.ExecContext(ctx, `
		UPDATE notes SET
		  updated_at = CASE WHEN title = ? AND text = ? THEN updated_at ELSE ? END,
		  title = ?, text = ?
		WHERE id = ?`, title, text, now(), title, text, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteNote(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM notes WHERE id = ?", id)
	return err
}
