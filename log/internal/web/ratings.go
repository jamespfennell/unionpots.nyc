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
