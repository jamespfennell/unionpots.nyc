package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Clay is a clay body.
type Clay struct {
	ID     int64
	Name   string
	Notes  string
	Pieces int // number of pieces made from it
}

const clayColumns = `c.id, c.name, c.notes,
	(SELECT COUNT(*) FROM piece_clays pc WHERE pc.clay_id = c.id)`

func scanClay(row interface{ Scan(...any) error }) (Clay, error) {
	var c Clay
	err := row.Scan(&c.ID, &c.Name, &c.Notes, &c.Pieces)
	return c, err
}

func (s *Store) queryClays(ctx context.Context, query string, args ...any) ([]Clay, error) {
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cs []Clay
	for rows.Next() {
		c, err := scanClay(rows)
		if err != nil {
			return nil, err
		}
		cs = append(cs, c)
	}
	return cs, rows.Err()
}

// Clays lists all clay bodies, most recently used first (by the newest piece
// made from each), then by name.
func (s *Store) Clays(ctx context.Context) ([]Clay, error) {
	return s.queryClays(ctx, `SELECT `+clayColumns+` FROM clays c
		ORDER BY (SELECT MAX(pc.piece_id) FROM piece_clays pc WHERE pc.clay_id = c.id) DESC NULLS LAST,
		         c.name COLLATE NOCASE`)
}

// LastUsedClays returns the clay IDs of the most recently created piece that
// has any: the default for a new piece.
func (s *Store) LastUsedClays(ctx context.Context) ([]int64, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT clay_id FROM piece_clays
		WHERE piece_id = (SELECT p.id FROM pieces p
		                  WHERE EXISTS (SELECT 1 FROM piece_clays pc WHERE pc.piece_id = p.id)
		                  ORDER BY p.created_at DESC, p.id DESC LIMIT 1)
		ORDER BY clay_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) GetClay(ctx context.Context, id int64) (Clay, error) {
	c, err := scanClay(s.DB.QueryRowContext(ctx, `SELECT `+clayColumns+` FROM clays c WHERE c.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// EnsureClay returns the clay with this name (ignoring case), creating it if
// there isn't one.
func (s *Store) EnsureClay(ctx context.Context, name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, userErr("a clay body needs a name")
	}
	var id int64
	err := s.DB.QueryRowContext(ctx, "SELECT id FROM clays WHERE name = ? COLLATE NOCASE", name).Scan(&id)
	if err == nil {
		return id, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := s.DB.ExecContext(ctx, "INSERT INTO clays (name) VALUES (?)", name)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) UpdateClay(ctx context.Context, c Clay) error {
	c.Name = strings.TrimSpace(c.Name)
	if c.Name == "" {
		return userErr("a clay body needs a name")
	}
	res, err := s.DB.ExecContext(ctx,
		"UPDATE clays SET name = ?, notes = ? WHERE id = ?",
		c.Name, c.Notes, c.ID)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return userErr("there is already a clay body called %q", c.Name)
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteClay deletes a clay body that no piece uses.
func (s *Store) DeleteClay(ctx context.Context, id int64) error {
	c, err := s.GetClay(ctx, id)
	if err != nil {
		return err
	}
	if c.Pieces > 0 {
		return userErr("%s is used by %d piece(s), so it can't be deleted", c.Name, c.Pieces)
	}
	_, err = s.DB.ExecContext(ctx, "DELETE FROM clays WHERE id = ?", id)
	return err
}

// PieceClays returns the clays a piece is made from.
func (s *Store) PieceClays(ctx context.Context, pieceID int64) ([]Clay, error) {
	return s.queryClays(ctx, `SELECT `+clayColumns+` FROM clays c
		JOIN piece_clays pc ON pc.clay_id = c.id AND pc.piece_id = ?
		ORDER BY c.name COLLATE NOCASE`, pieceID)
}

// ClayPieces returns the pieces made from a clay, newest first.
func (s *Store) ClayPieces(ctx context.Context, clayID int64) ([]Piece, error) {
	return s.queryPieces(ctx, `WHERE p.id IN (SELECT piece_id FROM piece_clays WHERE clay_id = ?)
		ORDER BY p.id DESC`, clayID)
}

// SetPieceClays replaces the clays a piece is made from.
func (s *Store) SetPieceClays(ctx context.Context, pieceID int64, clayIDs []int64) error {
	_, err := withTx(ctx, s.DB, func(tx *sql.Tx) (struct{}, error) {
		return struct{}{}, setPieceClays(ctx, tx, pieceID, clayIDs)
	})
	return err
}

func setPieceClays(ctx context.Context, tx *sql.Tx, pieceID int64, clayIDs []int64) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM piece_clays WHERE piece_id = ?", pieceID); err != nil {
		return err
	}
	for _, c := range clayIDs {
		if _, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO piece_clays (piece_id, clay_id) VALUES (?, ?)", pieceID, c); err != nil {
			if strings.Contains(err.Error(), "FOREIGN KEY") {
				return userErr("unknown clay body")
			}
			return err
		}
	}
	return nil
}
