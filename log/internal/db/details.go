package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// Free-text piece details, stored as piece metadata. These are deliberately
// not records of their own (unlike clay bodies).
const (
	formKey       = "form"        // JSON string, e.g. "bowl"
	glazesKey     = "glazes"      // JSON list of glaze names, in order applied
	glazeNotesKey = "glaze_notes" // JSON string: how they were applied
)

// Glazing is what a piece was glazed with.
type Glazing struct {
	Glazes []string
	Notes  string
}

func (g Glazing) Empty() bool { return len(g.Glazes) == 0 && g.Notes == "" }

// CleanGlazes trims names, drops blanks and repeats (ignoring case), and
// keeps the order given.
func CleanGlazes(names []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[strings.ToLower(n)] {
			continue
		}
		seen[strings.ToLower(n)] = true
		out = append(out, n)
	}
	return out
}

// PieceDetails are a piece's free-text details.
type PieceDetails struct {
	Form    string
	Glazing Glazing
}

func (s *Store) GetDetails(ctx context.Context, pieceID int64) (PieceDetails, error) {
	var d PieceDetails
	rows, err := s.DB.QueryContext(ctx,
		"SELECT key, value FROM metadata WHERE piece_id = ? AND key IN (?, ?, ?) AND value IS NOT NULL",
		pieceID, formKey, glazesKey, glazeNotesKey)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return d, err
		}
		var dst any
		switch key {
		case formKey:
			dst = &d.Form
		case glazesKey:
			dst = &d.Glazing.Glazes
		case glazeNotesKey:
			dst = &d.Glazing.Notes
		}
		if err := json.Unmarshal([]byte(value), dst); err != nil {
			return d, err
		}
	}
	return d, rows.Err()
}

func (s *Store) SetForm(ctx context.Context, pieceID int64, form string) error {
	_, err := withTx(ctx, s.DB, func(tx *sql.Tx) (struct{}, error) {
		return struct{}{}, setForm(ctx, tx, pieceID, form)
	})
	return err
}

func setForm(ctx context.Context, tx *sql.Tx, pieceID int64, form string) error {
	form = strings.TrimSpace(form)
	return setMeta(ctx, tx, pieceID, formKey, form, form == "")
}

func (s *Store) SetGlazing(ctx context.Context, pieceID int64, g Glazing) error {
	_, err := withTx(ctx, s.DB, func(tx *sql.Tx) (struct{}, error) {
		return struct{}{}, setGlazing(ctx, tx, pieceID, g)
	})
	return err
}

func setGlazing(ctx context.Context, tx *sql.Tx, pieceID int64, g Glazing) error {
	g.Glazes = CleanGlazes(g.Glazes)
	g.Notes = strings.TrimSpace(g.Notes)
	if err := setMeta(ctx, tx, pieceID, glazesKey, g.Glazes, len(g.Glazes) == 0); err != nil {
		return err
	}
	return setMeta(ctx, tx, pieceID, glazeNotesKey, g.Notes, g.Notes == "")
}

// GlazeNames lists every glaze name used so far, for suggestions.
func (s *Store) GlazeNames(ctx context.Context) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT DISTINCT g.value FROM metadata m, json_each(m.value) g
		WHERE m.key = ? AND m.piece_id IS NOT NULL
		ORDER BY g.value COLLATE NOCASE`, glazesKey)
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

// Ratings are 1–5 scores given once a piece is finished; 0 = not rated.
type Ratings struct {
	Glaze, Shape, Overall int
}

var ratingKeys = []string{"rating.glaze", "rating.shape", "rating.overall"}

func (r *Ratings) fields() []*int { return []*int{&r.Glaze, &r.Shape, &r.Overall} }

func (s *Store) GetRatings(ctx context.Context, pieceID int64) (Ratings, error) {
	var r Ratings
	for i, key := range ratingKeys {
		var value string
		err := s.DB.QueryRowContext(ctx,
			"SELECT value FROM metadata WHERE piece_id = ? AND key = ? AND value IS NOT NULL", pieceID, key).Scan(&value)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		} else if err != nil {
			return r, err
		}
		if err := json.Unmarshal([]byte(value), r.fields()[i]); err != nil {
			return r, err
		}
	}
	return r, nil
}

func (s *Store) SetRatings(ctx context.Context, pieceID int64, r Ratings) error {
	for _, v := range r.fields() {
		if *v < 0 || *v > 5 {
			return userErr("ratings go from 1 to 5")
		}
	}
	_, err := withTx(ctx, s.DB, func(tx *sql.Tx) (struct{}, error) {
		for i, key := range ratingKeys {
			v := *r.fields()[i]
			if err := setMeta(ctx, tx, pieceID, key, v, v == 0); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	})
	return err
}
