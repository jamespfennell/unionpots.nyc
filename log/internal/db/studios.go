package db

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Studio is a place where pieces are made.
type Studio struct {
	ID     int64
	Name   string
	Pieces int // number of pieces made there
}

// Studios lists the studios by name, with their piece counts.
func (s *Store) Studios(ctx context.Context) ([]Studio, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT st.id, st.name, (SELECT COUNT(*) FROM pieces p WHERE p.studio_id = st.id)
		FROM studios st ORDER BY st.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Studio
	for rows.Next() {
		var st Studio
		if err := rows.Scan(&st.ID, &st.Name, &st.Pieces); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// EnsureStudio returns the studio with this name (ignoring case), adding it
// if it's new.
func (s *Store) EnsureStudio(ctx context.Context, name string) (int64, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0, userErr("a studio needs a name")
	}
	var id int64
	err := s.DB.QueryRowContext(ctx, "SELECT id FROM studios WHERE name = ?", name).Scan(&id)
	if err == nil {
		return id, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := s.DB.ExecContext(ctx, "INSERT INTO studios (name, created_at) VALUES (?, ?)", name, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RenameStudio changes a studio's name.
func (s *Store) RenameStudio(ctx context.Context, id int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return userErr("a studio needs a name")
	}
	res, err := s.DB.ExecContext(ctx, "UPDATE studios SET name = ? WHERE id = ?", name, id)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return userErr("there is already a studio called %q", name)
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteStudio removes a studio no piece was made at.
func (s *Store) DeleteStudio(ctx context.Context, id int64) error {
	var used bool
	if err := s.DB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pieces WHERE studio_id = ?)", id).Scan(&used); err != nil {
		return err
	}
	if used {
		return userErr("pieces were made at this studio, so it can't be deleted")
	}
	_, err := s.DB.ExecContext(ctx, "DELETE FROM studios WHERE id = ?", id)
	return err
}

// LastUsedStudio is the studio of the most recently created piece (0 if
// none), the default for new pieces.
func (s *Store) LastUsedStudio(ctx context.Context) (int64, error) {
	var id sql.NullInt64
	err := s.DB.QueryRowContext(ctx, "SELECT studio_id FROM pieces WHERE studio_id IS NOT NULL ORDER BY id DESC LIMIT 1").Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id.Int64, err
}

// SetPieceStudio sets where a piece was made (0 for unknown).
func (s *Store) SetPieceStudio(ctx context.Context, pieceID, studioID int64) error {
	res, err := s.DB.ExecContext(ctx, "UPDATE pieces SET studio_id = ? WHERE id = ?", nullID(studioID), pieceID)
	if err != nil {
		if isForeignKeyError(err) {
			return userErr("unknown studio")
		}
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}
