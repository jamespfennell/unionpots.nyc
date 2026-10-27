package web

import (
	"net/http"

	"unionpots.nyc/log/internal/db"
	"unionpots.nyc/log/internal/model"
)

// Firing calculators: what bisque firing costs, two separate ways: a stack
// built in the studio and measured as a whole (less than the sum, since pieces
// nest), or pieces waiting to be fired, each at the size it was queued at.
// Nothing is saved.

// firingRates are the studio's per-cubic-inch bisque firing rates, in
// dollars, as the script reads them.
const firingRates = "0.04 0.06"

// firingPiece is one piece on the calculator; Volume is 0 if it wasn't
// measured when it was queued.
type firingPiece struct {
	db.Piece
	Volume float64
}

func (s *Server) firing(w http.ResponseWriter, r *http.Request) error {
	studios, err := s.Store.Studios(r.Context())
	if err != nil {
		return err
	}
	studio := s.studioFilter(w, r, studios)
	pieces, err := s.Store.PiecesInState(r.Context(), model.PendingBisque)
	if err != nil {
		return err
	}
	var fps []firingPiece
	for _, p := range pieces {
		if studio != 0 && p.StudioID != studio {
			continue
		}
		m, err := s.Store.GetMeasurements(r.Context(), p.ID)
		if err != nil {
			return err
		}
		d := m.Dims[model.QueuedBisque]
		fps = append(fps, firingPiece{p, d.H * d.W * d.D})
	}
	data := struct {
		Page
		Pieces  []firingPiece
		Studios []db.Studio
		Studio  int64 // the studio shown; 0 = all
		Stack   []dimsRow
	}{s.page(r, "Firing calculators"), fps, studios, studio, []dimsRow{{Key: "stack"}}}
	if len(studios) < 2 {
		data.Studios = nil // nothing to filter by
	}
	return s.render(w, http.StatusOK, "firing", data)
}
