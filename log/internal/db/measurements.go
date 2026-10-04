package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"unionpots.nyc/log/internal/model"
)

// Measurements are stored as piece metadata rows: "dims.<action>" holds the
// dimensions taken at that step as JSON, "clay_weight" the weight of clay in
// pounds.
const (
	dimsKeyPrefix = "dims."
	clayWeightKey = "clay_weight"
)

// Measurements are a piece's recorded measurements.
type Measurements struct {
	ClayWeight float64                     // pounds; 0 = not recorded
	Dims       map[model.Action]model.Dims // keyed by the step they were taken at
}

func dimsKey(a model.Action) string { return dimsKeyPrefix + string(a) }

// GetMeasurements returns a piece's measurements (never nil maps).
func (s *Store) GetMeasurements(ctx context.Context, pieceID int64) (Measurements, error) {
	m := Measurements{Dims: map[model.Action]model.Dims{}}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT key, value FROM metadata
		WHERE piece_id = ? AND (key = ? OR key LIKE 'dims.%') AND value IS NOT NULL`,
		pieceID, clayWeightKey)
	if err != nil {
		return m, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return m, err
		}
		switch {
		case key == clayWeightKey:
			if err := json.Unmarshal([]byte(value), &m.ClayWeight); err != nil {
				return m, err
			}
		case strings.HasPrefix(key, dimsKeyPrefix):
			var d model.Dims
			if err := json.Unmarshal([]byte(value), &d); err != nil {
				return m, err
			}
			m.Dims[model.Action(strings.TrimPrefix(key, dimsKeyPrefix))] = d
		}
	}
	return m, rows.Err()
}

// SetMeasurements replaces all of a piece's measurements.
func (s *Store) SetMeasurements(ctx context.Context, pieceID int64, m Measurements) error {
	_, err := withTx(ctx, s.DB, func(tx *sql.Tx) (struct{}, error) {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pieces WHERE id = ?)", pieceID).Scan(&exists); err != nil {
			return struct{}{}, err
		}
		if !exists {
			return struct{}{}, ErrNotFound
		}
		if err := setMeta(ctx, tx, pieceID, clayWeightKey, m.ClayWeight, m.ClayWeight == 0); err != nil {
			return struct{}{}, err
		}
		for _, a := range model.MeasuredAt {
			d := m.Dims[a]
			if err := setMeta(ctx, tx, pieceID, dimsKey(a), d, d.Empty()); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	})
	return err
}

// setMeta writes (or, if clear, removes) one piece metadata value.
func setMeta(ctx context.Context, tx *sql.Tx, pieceID int64, key string, value any, clear bool) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM metadata WHERE piece_id = ? AND key = ?", pieceID, key); err != nil {
		return err
	}
	if clear {
		return nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		"INSERT INTO metadata (piece_id, key, value, updated_at) VALUES (?, ?, ?, ?)",
		pieceID, key, string(b), now())
	return err
}
