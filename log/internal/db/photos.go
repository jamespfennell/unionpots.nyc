package db

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

// Photo is a photo attached to a piece. The files themselves live in the
// photos package; this is the record of which piece they belong to.
type Photo struct {
	ID            int64
	PieceID       int64
	SHA256        string
	Ext           string
	Width, Height int
	CreatedAt     string
	BackedUp      bool
}

// Aspect is width ÷ height (of the upright photo), for laying out
// thumbnails at their own shape.
func (p Photo) Aspect() string {
	if p.Width <= 0 || p.Height <= 0 {
		return "1"
	}
	return strconv.FormatFloat(float64(p.Width)/float64(p.Height), 'f', 4, 64)
}

const photoColumns = `id, COALESCE(piece_id, 0), sha256, original_ext, width, height, created_at, backed_up_at IS NOT NULL`

func scanPhoto(row interface{ Scan(...any) error }) (Photo, error) {
	var p Photo
	err := row.Scan(&p.ID, &p.PieceID, &p.SHA256, &p.Ext, &p.Width, &p.Height, &p.CreatedAt, &p.BackedUp)
	return p, err
}

func (s *Store) queryPhotos(ctx context.Context, where string, args ...any) ([]Photo, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+photoColumns+" FROM photos "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ps []Photo
	for rows.Next() {
		p, err := scanPhoto(rows)
		if err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	return ps, rows.Err()
}

// AddPhoto records a photo for a piece. Adding the same photo to the same
// piece again is a no-op; a photo can belong to only one piece.
func (s *Store) AddPhoto(ctx context.Context, pieceID int64, sha, ext string, width, height int) (Photo, error) {
	existing, err := scanPhoto(s.DB.QueryRowContext(ctx, "SELECT "+photoColumns+" FROM photos WHERE sha256 = ?", sha))
	switch {
	case err == nil && existing.PieceID == pieceID:
		return existing, nil
	case err == nil && existing.PieceID != 0:
		return Photo{}, userErr("that photo is already on #%d", existing.PieceID)
	case err == nil: // left over from a deleted piece: reuse it
		_, err := s.DB.ExecContext(ctx, "UPDATE photos SET piece_id = ?, created_at = ? WHERE id = ?", pieceID, now(), existing.ID)
		if err != nil {
			return Photo{}, err
		}
		return s.GetPhoto(ctx, existing.ID)
	case !errors.Is(err, sql.ErrNoRows):
		return Photo{}, err
	}
	res, err := s.DB.ExecContext(ctx, `
		INSERT INTO photos (sha256, piece_id, original_ext, width, height, position, created_at)
		VALUES (?, ?, ?, ?, ?, (SELECT COALESCE(MAX(position), 0) + 1 FROM photos WHERE piece_id = ?), ?)`,
		sha, pieceID, ext, width, height, pieceID, now())
	if err != nil {
		if isForeignKeyError(err) {
			return Photo{}, ErrNotFound
		}
		return Photo{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Photo{}, err
	}
	return s.GetPhoto(ctx, id)
}

func (s *Store) GetPhoto(ctx context.Context, id int64) (Photo, error) {
	p, err := scanPhoto(s.DB.QueryRowContext(ctx, "SELECT "+photoColumns+" FROM photos WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// PiecePhotos returns a piece's photos in the order they were added.
func (s *Store) PiecePhotos(ctx context.Context, pieceID int64) ([]Photo, error) {
	return s.queryPhotos(ctx, "WHERE piece_id = ? ORDER BY position, id", pieceID)
}

// DeletePhoto removes a piece's photo record and returns it (so its files
// can be removed too).
func (s *Store) DeletePhoto(ctx context.Context, pieceID, photoID int64) (Photo, error) {
	p, err := s.GetPhoto(ctx, photoID)
	if err != nil {
		return p, err
	}
	if p.PieceID != pieceID {
		return p, ErrNotFound
	}
	_, err = s.DB.ExecContext(ctx, "DELETE FROM photos WHERE id = ?", photoID)
	return p, err
}

// PhotosNotBackedUp lists photos whose files haven't been copied to backup
// storage yet.
func (s *Store) PhotosNotBackedUp(ctx context.Context) ([]Photo, error) {
	return s.queryPhotos(ctx, "WHERE backed_up_at IS NULL ORDER BY id")
}

func (s *Store) MarkPhotoBackedUp(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE photos SET backed_up_at = ? WHERE id = ?", now(), id)
	return err
}

func isForeignKeyError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY")
}
