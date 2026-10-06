package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"unionpots.nyc/log/internal/model"
)

var ErrNotFound = errors.New("not found")

// UserError is an error whose message can be shown to the user as-is.
type UserError struct{ Msg string }

func (e *UserError) Error() string { return e.Msg }

func userErr(format string, args ...any) error {
	return &UserError{fmt.Sprintf(format, args...)}
}

type Store struct {
	DB *sql.DB
}

type Piece struct {
	ID        int64
	ProjectID int64
	Notes     string
	State     model.State
	StateDate string // date of the latest event, when it entered its state
	// StateDateApprox: that date is approximate (filled in by the notebook
	// import).
	StateDateApprox bool
	StudioID        int64 // where it was made; 0 if not recorded
	StudioName      string

	ProjectName  string // "" if the project is unnamed
	ProjectSize  int    // number of pieces in the project
	ProjectIndex int    // 1-based position in the project, by piece ID
	Public       bool
	CreatedAt    string
	UpdatedAt    string
}

// Label names the piece by its project: "Set of 4 bowls (2/4)", or just
// "Sculpture" for a piece on its own. All names are project names.
func (p Piece) Label() string {
	if p.ProjectSize > 1 {
		return strings.TrimSpace(fmt.Sprintf("%s (%d/%d)", p.ProjectName, p.ProjectIndex, p.ProjectSize))
	}
	return p.ProjectName
}

// Title is "#121" or "#121 · Set of 4 bowls (2/4)".
func (p Piece) Title() string {
	if l := p.Label(); l != "" {
		return fmt.Sprintf("#%d · %s", p.ID, l)
	}
	return fmt.Sprintf("#%d", p.ID)
}

type Event struct {
	ID          int64
	PieceID     int64
	Action      model.Action
	Date        string
	Approximate bool // the date is a guess (see migration 006)
}

type Project struct {
	ID     int64
	Name   string
	Notes  string
	Pieces []Piece
}

// DisplayName is the project's name, or e.g. "Project of #120–#123".
func (p Project) DisplayName() string {
	if p.Name != "" {
		return p.Name
	}
	switch len(p.Pieces) {
	case 0:
		return "Empty project"
	case 1:
		return fmt.Sprintf("Project of #%d", p.Pieces[0].ID)
	}
	lo, hi := p.Pieces[0].ID, p.Pieces[0].ID
	for _, pc := range p.Pieces {
		lo, hi = min(lo, pc.ID), max(hi, pc.ID)
	}
	if hi-lo+1 == int64(len(p.Pieces)) {
		return fmt.Sprintf("Project of #%d–#%d", lo, hi)
	}
	ids := make([]string, len(p.Pieces))
	for i, pc := range p.Pieces {
		ids[i] = fmt.Sprintf("#%d", pc.ID)
	}
	return "Project of " + strings.Join(ids, ", ")
}

// NewPieces describes pieces to create together.
type NewPieces struct {
	Count     int
	StudioID  int64        // where they're made; 0 if not recorded
	ProjectID int64        // 0 = create a new project
	Name      string       // name of the new project; ignored when adding to an existing one
	Action    model.Action // how the pieces start, e.g. thrown
	Date      string

	// Optional details at creation, applied to every piece.
	Form       string
	ClayIDs    []int64
	ClayWeight float64
	Dims       model.Dims
}

// NextPieceID is the number the next piece will get: the stored counter,
// which only moves forward, so numbers are never reused.
func (s *Store) NextPieceID(ctx context.Context) (int64, error) {
	return s.nextPieceID(ctx, s.DB)
}

func (s *Store) nextPieceID(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (int64, error) {
	var next int64
	if err := q.QueryRowContext(ctx, "SELECT next_piece_id FROM id_sequence").Scan(&next); err != nil {
		return 0, err
	}
	return max(next, 1), nil
}

// CreatePieces creates pieces, each with an initial event, and returns
// their IDs.
func (s *Store) CreatePieces(ctx context.Context, np NewPieces) ([]int64, error) {
	if np.Count < 1 || np.Count > 50 {
		return nil, userErr("number of pieces must be between 1 and 50")
	}
	if _, err := model.ParseAction(string(np.Action)); err != nil {
		return nil, &UserError{err.Error()}
	}
	ts := now()
	return withTx(ctx, s.DB, func(tx *sql.Tx) ([]int64, error) {
		next, err := s.nextPieceID(ctx, tx)
		if err != nil {
			return nil, err
		}
		ids := make([]int64, np.Count)
		for i := range ids {
			ids[i] = next + int64(i)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE id_sequence SET next_piece_id = ?", next+int64(np.Count)); err != nil {
			return nil, err
		}

		projectID := np.ProjectID
		if projectID == 0 {
			res, err := tx.ExecContext(ctx,
				"INSERT INTO projects (name, created_at, updated_at) VALUES (?, ?, ?)",
				nullIfEmpty(np.Name), ts, ts)
			if err != nil {
				return nil, err
			}
			if projectID, err = res.LastInsertId(); err != nil {
				return nil, err
			}
		} else {
			var exists bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM projects WHERE id = ?)", projectID).Scan(&exists); err != nil {
				return nil, err
			}
			if !exists {
				return nil, ErrNotFound
			}
		}

		for _, id := range ids {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO pieces (id, project_id, state, created_at, updated_at)
				 VALUES (?, ?, ?, ?, ?)`,
				id, projectID, np.Action.Result(), ts, ts); err != nil {
				return nil, err
			}
			if err := insertEvent(ctx, tx, id, np.Action, np.Date, ts); err != nil {
				return nil, err
			}
			if np.StudioID != 0 {
				if _, err := tx.ExecContext(ctx, "UPDATE pieces SET studio_id = ? WHERE id = ?", np.StudioID, id); err != nil {
					if isForeignKeyError(err) {
						return nil, userErr("unknown studio")
					}
					return nil, err
				}
			}
			if err := setPieceClays(ctx, tx, id, np.ClayIDs); err != nil {
				return nil, err
			}
			if err := setForm(ctx, tx, id, np.Form); err != nil {
				return nil, err
			}
			if np.ClayWeight != 0 {
				if err := setMeta(ctx, tx, id, clayWeightKey, np.ClayWeight, false); err != nil {
					return nil, err
				}
			}
			if np.Action.Measurable() && !np.Dims.Empty() {
				if err := setMeta(ctx, tx, id, dimsKey(np.Action), np.Dims, false); err != nil {
					return nil, err
				}
			}
		}
		return ids, nil
	})
}

func insertEvent(ctx context.Context, tx *sql.Tx, pieceID int64, action model.Action, date, ts string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO events (piece_id, action, occurred_on, created_at)
		 VALUES (?, ?, ?, ?)`,
		pieceID, action, date, ts)
	return err
}

// refreshPiece recomputes the piece's state from its latest event and bumps
// updated_at.
func refreshPiece(ctx context.Context, tx *sql.Tx, pieceID int64) error {
	var latest model.Action
	err := tx.QueryRowContext(ctx, `
		SELECT action FROM events WHERE piece_id = ?
		ORDER BY occurred_on DESC, id DESC LIMIT 1`, pieceID).Scan(&latest)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE pieces SET state = ?, updated_at = ? WHERE id = ?",
		latest.Result(), now(), pieceID)
	return err
}

const pieceColumns = `p.id, p.project_id, p.notes, p.state,
	p.public, p.created_at, p.updated_at,
	COALESCE((SELECT occurred_on FROM events e WHERE e.piece_id = p.id
	          ORDER BY occurred_on DESC, id DESC LIMIT 1), ''),
	COALESCE((SELECT name FROM projects pr WHERE pr.id = p.project_id), ''),
	(SELECT COUNT(*) FROM pieces q WHERE q.project_id = p.project_id),
	(SELECT COUNT(*) FROM pieces q WHERE q.project_id = p.project_id AND q.id <= p.id),
	COALESCE((SELECT approximate FROM events e WHERE e.piece_id = p.id
	          ORDER BY occurred_on DESC, id DESC LIMIT 1), 0),
	COALESCE(p.studio_id, 0),
	COALESCE((SELECT name FROM studios st WHERE st.id = p.studio_id), '')`

func scanPiece(row interface{ Scan(...any) error }) (Piece, error) {
	var p Piece
	err := row.Scan(&p.ID, &p.ProjectID, &p.Notes, &p.State,
		&p.Public, &p.CreatedAt, &p.UpdatedAt, &p.StateDate,
		&p.ProjectName, &p.ProjectSize, &p.ProjectIndex, &p.StateDateApprox, &p.StudioID, &p.StudioName)
	return p, err
}

func (s *Store) queryPieces(ctx context.Context, where string, args ...any) ([]Piece, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+pieceColumns+" FROM pieces p "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ps []Piece
	for rows.Next() {
		p, err := scanPiece(rows)
		if err != nil {
			return nil, err
		}
		ps = append(ps, p)
	}
	return ps, rows.Err()
}

func (s *Store) GetPiece(ctx context.Context, id int64) (Piece, error) {
	p, err := scanPiece(s.DB.QueryRowContext(ctx, "SELECT "+pieceColumns+" FROM pieces p WHERE p.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// PiecesInState lists pieces currently in state, by piece ID.
func (s *Store) PiecesInState(ctx context.Context, state model.State) ([]Piece, error) {
	return s.queryPieces(ctx, "WHERE p.state = ? ORDER BY p.id", state)
}

func (s *Store) PieceEvents(ctx context.Context, pieceID int64) ([]Event, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, piece_id, action, occurred_on, approximate
		FROM events WHERE piece_id = ? ORDER BY occurred_on ASC, id ASC`, pieceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var es []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.PieceID, &e.Action, &e.Date, &e.Approximate); err != nil {
			return nil, err
		}
		es = append(es, e)
	}
	return es, rows.Err()
}

// StepDetails is what can be recorded along with a step, for every piece in
// it: dimensions (for measured steps) and glaze text (when glazing).
type StepDetails struct {
	Dims      model.Dims
	GlazeText string
}

// AddEvents records action happening to each of the pieces on date, along
// with the step's details.
func (s *Store) AddEvents(ctx context.Context, pieceIDs []int64, action model.Action, date string, details StepDetails) error {
	if len(pieceIDs) == 0 {
		return userErr("no pieces selected")
	}
	if _, err := model.ParseAction(string(action)); err != nil {
		return &UserError{err.Error()}
	}
	ts := now()
	_, err := withTx(ctx, s.DB, func(tx *sql.Tx) (struct{}, error) {
		for _, id := range pieceIDs {
			if err := insertEvent(ctx, tx, id, action, date, ts); err != nil {
				if strings.Contains(err.Error(), "FOREIGN KEY") {
					return struct{}{}, userErr("piece #%d does not exist", id)
				}
				return struct{}{}, err
			}
			if err := refreshPiece(ctx, tx, id); err != nil {
				return struct{}{}, err
			}
			if d := details.Dims; action.Measurable() && !d.Empty() {
				if err := setMeta(ctx, tx, id, dimsKey(action), d, false); err != nil {
					return struct{}{}, err
				}
			}
			if t := strings.TrimSpace(details.GlazeText); action == model.Glazed && t != "" {
				if err := setGlazeText(ctx, tx, id, t); err != nil {
					return struct{}{}, err
				}
			}
		}
		return struct{}{}, nil
	})
	return err
}

// DeleteEvent removes an event. A piece's only event cannot be deleted.
func (s *Store) DeleteEvent(ctx context.Context, pieceID, eventID int64) error {
	_, err := withTx(ctx, s.DB, func(tx *sql.Tx) (struct{}, error) {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM events WHERE piece_id = ?", pieceID).Scan(&n); err != nil {
			return struct{}{}, err
		}
		if n <= 1 {
			return struct{}{}, userErr("a piece must have at least one event")
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM events WHERE id = ? AND piece_id = ?", eventID, pieceID)
		if err != nil {
			return struct{}{}, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return struct{}{}, ErrNotFound
		}
		return struct{}{}, refreshPiece(ctx, tx, pieceID)
	})
	return err
}

func (s *Store) UpdatePieceNotes(ctx context.Context, id int64, notes string) error {
	res, err := s.DB.ExecContext(ctx,
		"UPDATE pieces SET notes = ?, updated_at = ? WHERE id = ?",
		notes, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePiece deletes a piece. Its ID is not reused. An emptied project is
// deleted too.
func (s *Store) DeletePiece(ctx context.Context, id int64) error {
	_, err := withTx(ctx, s.DB, func(tx *sql.Tx) (struct{}, error) {
		var projectID int64
		err := tx.QueryRowContext(ctx, "SELECT project_id FROM pieces WHERE id = ?", id).Scan(&projectID)
		if errors.Is(err, sql.ErrNoRows) {
			return struct{}{}, ErrNotFound
		} else if err != nil {
			return struct{}{}, err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM pieces WHERE id = ?", id); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, deleteIfEmpty(ctx, tx, projectID)
	})
	return err
}

// MovePiece moves a piece into the project containing piece toPieceID, or
// into a new project of its own if toPieceID is 0.
func (s *Store) MovePiece(ctx context.Context, id, toPieceID int64) error {
	_, err := withTx(ctx, s.DB, func(tx *sql.Tx) (struct{}, error) {
		var from int64
		err := tx.QueryRowContext(ctx, "SELECT project_id FROM pieces WHERE id = ?", id).Scan(&from)
		if errors.Is(err, sql.ErrNoRows) {
			return struct{}{}, ErrNotFound
		} else if err != nil {
			return struct{}{}, err
		}
		var to int64
		ts := now()
		if toPieceID == 0 {
			res, err := tx.ExecContext(ctx, "INSERT INTO projects (created_at, updated_at) VALUES (?, ?)", ts, ts)
			if err != nil {
				return struct{}{}, err
			}
			if to, err = res.LastInsertId(); err != nil {
				return struct{}{}, err
			}
		} else {
			err := tx.QueryRowContext(ctx, "SELECT project_id FROM pieces WHERE id = ?", toPieceID).Scan(&to)
			if errors.Is(err, sql.ErrNoRows) {
				return struct{}{}, userErr("piece #%d does not exist", toPieceID)
			} else if err != nil {
				return struct{}{}, err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE pieces SET project_id = ?, updated_at = ? WHERE id = ?", to, ts, id); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, deleteIfEmpty(ctx, tx, from)
	})
	return err
}

func deleteIfEmpty(ctx context.Context, tx *sql.Tx, projectID int64) error {
	_, err := tx.ExecContext(ctx,
		"DELETE FROM projects WHERE id = ? AND NOT EXISTS (SELECT 1 FROM pieces WHERE project_id = ?)",
		projectID, projectID)
	return err
}

func (s *Store) GetProject(ctx context.Context, id int64) (Project, error) {
	var p Project
	err := s.DB.QueryRowContext(ctx,
		"SELECT id, COALESCE(name, ''), notes FROM projects WHERE id = ?", id).Scan(&p.ID, &p.Name, &p.Notes)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	} else if err != nil {
		return p, err
	}
	p.Pieces, err = s.queryPieces(ctx, "WHERE p.project_id = ? ORDER BY p.id", id)
	return p, err
}

func (s *Store) RenameProject(ctx context.Context, id int64, name string) error {
	res, err := s.DB.ExecContext(ctx,
		"UPDATE projects SET name = ?, updated_at = ? WHERE id = ?",
		nullIfEmpty(name), now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateProject(ctx context.Context, id int64, name, notes string) error {
	res, err := s.DB.ExecContext(ctx,
		"UPDATE projects SET name = ?, notes = ?, updated_at = ? WHERE id = ?",
		nullIfEmpty(name), notes, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func withTx[T any](ctx context.Context, sqlDB *sql.DB, f func(*sql.Tx) (T, error)) (T, error) {
	var zero T
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	v, err := f(tx)
	if err != nil {
		tx.Rollback()
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return v, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
