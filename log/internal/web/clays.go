package web

import (
	"net/http"
	"strconv"

	"unionpots.nyc/log/internal/db"
)

// formClays reads the selected clay pills (name "clay", repeated).
func (s *Server) formClays(r *http.Request) ([]int64, error) {
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	var ids []int64
	for _, v := range r.Form["clay"] {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, &db.UserError{Msg: "unknown clay body"}
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func clayURL(id int64) string { return "/clays/" + strconv.FormatInt(id, 10) }

func (s *Server) clays(w http.ResponseWriter, r *http.Request) error {
	cs, err := s.Store.Clays(r.Context())
	if err != nil {
		return err
	}
	data := struct {
		Page
		Clays []db.Clay
	}{s.page(r, "Clay bodies"), cs}
	return s.render(w, http.StatusOK, "clays", data)
}

func (s *Server) addClay(w http.ResponseWriter, r *http.Request) error {
	id, err := s.Store.EnsureClay(r.Context(), r.FormValue("title"))
	if err != nil {
		return err
	}
	return redirect(w, r, clayURL(id))
}

func (s *Server) clay(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	c, err := s.Store.GetClay(r.Context(), id)
	if err != nil {
		return err
	}
	pieces, err := s.Store.ClayPieces(r.Context(), id)
	if err != nil {
		return err
	}
	data := struct {
		Page
		Clay   db.Clay
		Pieces []db.Piece
	}{s.page(r, c.Name), c, pieces}
	return s.render(w, http.StatusOK, "clay", data)
}

func (s *Server) updateClay(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	c := db.Clay{ID: id, Name: r.FormValue("title"), Code: r.FormValue("code"), Price: r.FormValue("price"), Notes: r.FormValue("notes")}
	if err := s.Store.UpdateClay(r.Context(), c); err != nil {
		return err
	}
	if isAutosave(r) {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	return redirect(w, r, clayURL(id))
}

func (s *Server) deleteClay(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := s.Store.DeleteClay(r.Context(), id); err != nil {
		return err
	}
	return redirect(w, r, "/clays")
}

// hasID reports whether ids contains id; used to pre-select clay pills.
func hasID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}
