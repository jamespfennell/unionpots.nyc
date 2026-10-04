package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"unionpots.nyc/log/internal/model"
)

// Free-text piece details, stored as piece metadata.
const (
	formKey      = "form"       // JSON string, e.g. "bowl"
	glazeTextKey = "glaze_text" // JSON string as written: "Pink with dabs of Red"
	glazesKey    = "glazes"     // JSON list: known glazes found in glaze_text, for search
)

// PieceDetails are a piece's free-text details.
type PieceDetails struct {
	Form      string
	GlazeText string   // as written
	Glazes    []string // known glaze names found in GlazeText
}

func (s *Store) GetDetails(ctx context.Context, pieceID int64) (PieceDetails, error) {
	var d PieceDetails
	rows, err := s.DB.QueryContext(ctx,
		"SELECT key, value FROM metadata WHERE piece_id = ? AND key IN (?, ?, ?) AND value IS NOT NULL",
		pieceID, formKey, glazeTextKey, glazesKey)
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
		case glazeTextKey:
			dst = &d.GlazeText
		case glazesKey:
			dst = &d.Glazes
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

// SetGlazeText records how a piece was glazed, and the known glazes in it.
func (s *Store) SetGlazeText(ctx context.Context, pieceID int64, text string) error {
	_, err := withTx(ctx, s.DB, func(tx *sql.Tx) (struct{}, error) {
		return struct{}{}, setGlazeText(ctx, tx, pieceID, text)
	})
	return err
}

func setGlazeText(ctx context.Context, tx *sql.Tx, pieceID int64, text string) error {
	text = strings.TrimSpace(text)
	names, err := glazeNames(ctx, tx)
	if err != nil {
		return err
	}
	found := model.GlazesIn(text, names)
	if err := setMeta(ctx, tx, pieceID, glazeTextKey, text, text == ""); err != nil {
		return err
	}
	return setMeta(ctx, tx, pieceID, glazesKey, found, len(found) == 0)
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
