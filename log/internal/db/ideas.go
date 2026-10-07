package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// Idea is one item on the App ideas page. Hash identifies it to the code
// that implements it (see internal/ideas); it is never shown in the app.
type Idea struct {
	ID        int64
	Hash      string
	Text      string
	Simple    bool // small and clear: Claude may implement it without discussion
	CreatedAt string
	DoneAt    string // "" while open
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Ideas returns the open ideas (oldest first) and the done ones (most
// recently done first).
func (s *Store) Ideas(ctx context.Context) (open, done []Idea, err error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, hash, text, simple, created_at, COALESCE(done_at, '') FROM ideas
		ORDER BY done_at IS NOT NULL, done_at DESC, id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var i Idea
		if err := rows.Scan(&i.ID, &i.Hash, &i.Text, &i.Simple, &i.CreatedAt, &i.DoneAt); err != nil {
			return nil, nil, err
		}
		if i.DoneAt == "" {
			open = append(open, i)
		} else {
			done = append(done, i)
		}
	}
	return open, done, rows.Err()
}

// AddIdea adds an idea with a new random hash.
func (s *Store) AddIdea(ctx context.Context, text string, simple bool) (Idea, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Idea{}, userErr("write the idea first")
	}
	hash, err := randomHex(16)
	if err != nil {
		return Idea{}, err
	}
	ts := now()
	res, err := s.DB.ExecContext(ctx, "INSERT INTO ideas (hash, text, simple, created_at) VALUES (?, ?, ?, ?)", hash, text, simple, ts)
	if err != nil {
		return Idea{}, err
	}
	id, err := res.LastInsertId()
	return Idea{ID: id, Hash: hash, Text: text, Simple: simple, CreatedAt: ts}, err
}

// UpdateIdea changes an idea's text and whether it's simple. Emptying the
// text isn't allowed: delete it instead.
func (s *Store) UpdateIdea(ctx context.Context, id int64, text string, simple bool) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return userErr("an idea can't be empty (delete it instead)")
	}
	res, err := s.DB.ExecContext(ctx, "UPDATE ideas SET text = ?, simple = ? WHERE id = ?", text, simple, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteIdea(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM ideas WHERE id = ?", id)
	return err
}

// MarkIdeasDone marks the ideas with these hashes done (if open) and
// reports how many changed. Unknown hashes are ignored: they may belong to
// another instance, or to ideas since deleted.
func (s *Store) MarkIdeasDone(ctx context.Context, hashes []string) (int, error) {
	n := 0
	ts := now()
	for _, h := range hashes {
		res, err := s.DB.ExecContext(ctx, "UPDATE ideas SET done_at = ? WHERE hash = ? AND done_at IS NULL", ts, h)
		if err != nil {
			return n, err
		}
		c, _ := res.RowsAffected()
		n += int(c)
	}
	return n, nil
}
