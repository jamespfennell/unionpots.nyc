package db

import (
	"context"
	"database/sql"
)

// SetEventDate moves one of a piece's steps to a new date. The date must
// keep the steps in order (not before the previous step, not after the next)
// and not be after today. A date that's set is known, so it's no longer
// approximate. If alsoSiblings, the other pieces of the project that have
// the same step on the same (old) date get the new date too, where it fits
// their own steps; their ids are returned.
func (s *Store) SetEventDate(ctx context.Context, pieceID, eventID int64, date, today string, alsoSiblings bool) ([]int64, error) {
	return withTx(ctx, s.DB, func(tx *sql.Tx) ([]int64, error) {
		var e Event
		var projectID int64
		err := tx.QueryRowContext(ctx, `
			SELECT e.id, e.piece_id, e.action, e.occurred_on, p.project_id
			FROM events e JOIN pieces p ON p.id = e.piece_id
			WHERE e.id = ? AND e.piece_id = ?`, eventID, pieceID).Scan(&e.ID, &e.PieceID, &e.Action, &e.Date, &projectID)
		if err == sql.ErrNoRows {
			return nil, ErrNotFound
		} else if err != nil {
			return nil, err
		}
		lo, hi, err := dateBounds(ctx, tx, pieceID, eventID, today)
		if err != nil {
			return nil, err
		}
		if date < lo || date > hi {
			return nil, userErr("%s has to be between %s and %s, to keep the steps in order", e.Action.Label(), lo, hi)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE events SET occurred_on = ?, approximate = 0 WHERE id = ?", date, eventID); err != nil {
			return nil, err
		}
		if !alsoSiblings || date == e.Date {
			return nil, nil
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT e.id, e.piece_id FROM events e JOIN pieces p ON p.id = e.piece_id
			WHERE p.project_id = ? AND e.piece_id <> ? AND e.action = ? AND e.occurred_on = ?
			ORDER BY e.piece_id`, projectID, pieceID, e.Action, e.Date)
		if err != nil {
			return nil, err
		}
		type sib struct{ event, piece int64 }
		var sibs []sib
		for rows.Next() {
			var x sib
			if err := rows.Scan(&x.event, &x.piece); err != nil {
				rows.Close()
				return nil, err
			}
			sibs = append(sibs, x)
		}
		rows.Close()
		var changed []int64
		for _, x := range sibs {
			lo, hi, err := dateBounds(ctx, tx, x.piece, x.event, today)
			if err != nil {
				return nil, err
			}
			if date < lo || date > hi {
				continue // doesn't fit this piece's own steps
			}
			if _, err := tx.ExecContext(ctx, "UPDATE events SET occurred_on = ?, approximate = 0 WHERE id = ?", date, x.event); err != nil {
				return nil, err
			}
			changed = append(changed, x.piece)
		}
		return changed, nil
	})
}

// dateBounds is the range a step's date can take: from the previous step's
// date to the next step's, or to today for the latest step.
func dateBounds(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, pieceID, eventID int64, today string) (lo, hi string, err error) {
	rows, err := q.QueryContext(ctx, "SELECT id, occurred_on FROM events WHERE piece_id = ? ORDER BY occurred_on, id", pieceID)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	var ids []int64
	var dates []string
	for rows.Next() {
		var id int64
		var d string
		if err := rows.Scan(&id, &d); err != nil {
			return "", "", err
		}
		ids, dates = append(ids, id), append(dates, d)
	}
	lo, hi = "0000-01-01", today
	for i, id := range ids {
		if id != eventID {
			continue
		}
		if i > 0 {
			lo = dates[i-1]
		}
		if i+1 < len(ids) {
			hi = dates[i+1]
		}
	}
	return lo, hi, rows.Err()
}

// StepSiblings are, for each of a piece's steps (by event id), the other
// pieces of its project that have the same step on the same date: the ones
// a date change can also apply to.
func (s *Store) StepSiblings(ctx context.Context, pieceID int64) (map[int64][]int64, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT e.id, o.piece_id
		FROM events e
		JOIN pieces p ON p.id = e.piece_id
		JOIN pieces q ON q.project_id = p.project_id AND q.id <> p.id
		JOIN events o ON o.piece_id = q.id AND o.action = e.action AND o.occurred_on = e.occurred_on
		WHERE e.piece_id = ?
		ORDER BY e.id, o.piece_id`, pieceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sibs := map[int64][]int64{}
	for rows.Next() {
		var eid, other int64
		if err := rows.Scan(&eid, &other); err != nil {
			return nil, err
		}
		if l := sibs[eid]; len(l) == 0 || l[len(l)-1] != other {
			sibs[eid] = append(l, other)
		}
	}
	return sibs, rows.Err()
}
