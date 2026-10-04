package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"unionpots.nyc/log/internal/model"
)

// Glaze is a known glaze. Glazes are found by name in pieces' free glaze
// text; a piece "uses" a glaze when its text mentions it.
type Glaze struct {
	ID     int64
	Name   string
	Notes  string
	Pieces int
}

const glazeColumns = `g.id, g.name, g.notes,
	(SELECT COUNT(*) FROM metadata m, json_each(m.value) j
	 WHERE m.key = 'glazes' AND m.piece_id IS NOT NULL AND j.value = g.name COLLATE NOCASE)`

func scanGlaze(row interface{ Scan(...any) error }) (Glaze, error) {
	var g Glaze
	err := row.Scan(&g.ID, &g.Name, &g.Notes, &g.Pieces)
	return g, err
}

// Glazes lists the known glazes by name.
func (s *Store) Glazes(ctx context.Context) ([]Glaze, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+glazeColumns+` FROM glazes g ORDER BY g.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var gs []Glaze
	for rows.Next() {
		g, err := scanGlaze(rows)
		if err != nil {
			return nil, err
		}
		gs = append(gs, g)
	}
	return gs, rows.Err()
}

func (s *Store) GetGlaze(ctx context.Context, id int64) (Glaze, error) {
	g, err := scanGlaze(s.DB.QueryRowContext(ctx, `SELECT `+glazeColumns+` FROM glazes g WHERE g.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	return g, err
}

// GlazeNames are the known glaze names, used to recognise glazes in text.
func (s *Store) GlazeNames(ctx context.Context) ([]string, error) {
	return glazeNames(ctx, s.DB)
}

func glazeNames(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) ([]string, error) {
	rows, err := q.QueryContext(ctx, "SELECT name FROM glazes ORDER BY name COLLATE NOCASE")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// EnsureGlaze returns the glaze with this name (ignoring case), adding it if
// it isn't known yet. Existing pieces aren't re-scanned for it.
func (s *Store) EnsureGlaze(ctx context.Context, name string) (Glaze, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Glaze{}, userErr("a glaze needs a name")
	}
	var id int64
	err := s.DB.QueryRowContext(ctx, "SELECT id FROM glazes WHERE name = ? COLLATE NOCASE", name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		res, err := s.DB.ExecContext(ctx, "INSERT INTO glazes (name) VALUES (?)", name)
		if err != nil {
			return Glaze{}, err
		}
		if id, err = res.LastInsertId(); err != nil {
			return Glaze{}, err
		}
	} else if err != nil {
		return Glaze{}, err
	}
	return s.GetGlaze(ctx, id)
}

// GlazePieces returns the pieces whose glaze text mentions the glaze.
func (s *Store) GlazePieces(ctx context.Context, id int64) ([]Piece, error) {
	return s.queryPieces(ctx, `WHERE p.id IN (
		SELECT m.piece_id FROM metadata m, json_each(m.value) j, glazes g
		WHERE g.id = ? AND m.key = 'glazes' AND j.value = g.name COLLATE NOCASE)
		ORDER BY p.id DESC`, id)
}

// RenameGlaze renames a glaze, e.g. to fix a typo, rewriting the pieces that
// mention it so their text uses the new name. Renaming onto another known
// glaze merges the two.
func (s *Store) RenameGlaze(ctx context.Context, id int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return userErr("a glaze needs a name")
	}
	_, err := withTx(ctx, s.DB, func(tx *sql.Tx) (struct{}, error) {
		var old string
		err := tx.QueryRowContext(ctx, "SELECT name FROM glazes WHERE id = ?", id).Scan(&old)
		if errors.Is(err, sql.ErrNoRows) {
			return struct{}{}, ErrNotFound
		} else if err != nil {
			return struct{}{}, err
		}
		var other int64
		err = tx.QueryRowContext(ctx, "SELECT id FROM glazes WHERE name = ? COLLATE NOCASE AND id != ?", name, id).Scan(&other)
		switch {
		case err == nil: // merge into the existing glaze
			if _, err := tx.ExecContext(ctx, "DELETE FROM glazes WHERE id = ?", id); err != nil {
				return struct{}{}, err
			}
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.ExecContext(ctx, "UPDATE glazes SET name = ? WHERE id = ?", name, id); err != nil {
				return struct{}{}, err
			}
		default:
			return struct{}{}, err
		}
		// Rewrite and re-scan the pieces that mentioned the old name.
		rows, err := tx.QueryContext(ctx, `
			SELECT t.piece_id, json_extract(t.value, '$') FROM metadata t
			WHERE t.key = 'glaze_text' AND t.piece_id IN (
				SELECT m.piece_id FROM metadata m, json_each(m.value) j
				WHERE m.key = 'glazes' AND j.value = ? COLLATE NOCASE)`, old)
		if err != nil {
			return struct{}{}, err
		}
		type piece struct {
			id   int64
			text string
		}
		var affected []piece
		for rows.Next() {
			var p piece
			if err := rows.Scan(&p.id, &p.text); err != nil {
				rows.Close()
				return struct{}{}, err
			}
			affected = append(affected, p)
		}
		rows.Close()
		for _, p := range affected {
			if err := setGlazeText(ctx, tx, p.id, model.ReplaceGlaze(p.text, old, name)); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	})
	return err
}

// DeleteGlaze removes a glaze no piece mentions.
func (s *Store) DeleteGlaze(ctx context.Context, id int64) error {
	g, err := s.GetGlaze(ctx, id)
	if err != nil {
		return err
	}
	if g.Pieces > 0 {
		return userErr("%s is mentioned by %d piece(s), so it can't be deleted", g.Name, g.Pieces)
	}
	_, err = s.DB.ExecContext(ctx, "DELETE FROM glazes WHERE id = ?", id)
	return err
}
