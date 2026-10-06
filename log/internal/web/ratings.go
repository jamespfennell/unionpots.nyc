package web

import (
	"net/http"
	"strconv"

	"unionpots.nyc/log/internal/db"
)

// ratingRow is one row of 1–5 pills on the piece page.
type ratingRow struct {
	Name  string // form field: rating_<name>
	Label string
	Value int
}

func ratingRows(r db.Ratings) []ratingRow {
	return []ratingRow{
		{"glaze", "Glaze", r.Glaze},
		{"shape", "Shape", r.Shape},
		{"overall", "Overall", r.Overall},
	}
}

func (s *Server) updateRatings(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	// An unselected row sends nothing, which means "not rated".
	get := func(name string) int {
		n, _ := strconv.Atoi(r.FormValue("rating_" + name))
		return n
	}
	ratings := db.Ratings{Glaze: get("glaze"), Shape: get("shape"), Overall: get("overall")}
	if err := s.Store.SetRatings(r.Context(), id, ratings); err != nil {
		return err
	}
	if isAutosave(r) {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	return redirect(w, r, pieceURL(id))
}

// rateFinished saves the ratings chosen while marking pieces finished, if
// any, on each of them. Ratings not chosen are left as they were.
func (s *Server) rateFinished(r *http.Request, ids []int64) error {
	get := func(name string) int {
		n, _ := strconv.Atoi(r.FormValue("rating_" + name))
		return n
	}
	chosen := db.Ratings{Glaze: get("glaze"), Shape: get("shape"), Overall: get("overall")}
	if chosen == (db.Ratings{}) {
		return nil
	}
	for _, id := range ids {
		cur, err := s.Store.GetRatings(r.Context(), id)
		if err != nil {
			return err
		}
		for _, f := range []struct {
			dst *int
			v   int
		}{{&cur.Glaze, chosen.Glaze}, {&cur.Shape, chosen.Shape}, {&cur.Overall, chosen.Overall}} {
			if f.v != 0 {
				*f.dst = f.v
			}
		}
		if err := s.Store.SetRatings(r.Context(), id, cur); err != nil {
			return err
		}
	}
	return nil
}
