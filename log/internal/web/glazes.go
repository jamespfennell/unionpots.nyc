package web

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"unionpots.nyc/log/internal/db"
)

func glazeURL(id int64) string { return "/glazes/" + strconv.FormatInt(id, 10) }

func (s *Server) glazes(w http.ResponseWriter, r *http.Request) error {
	gs, err := s.Store.Glazes(r.Context())
	if err != nil {
		return err
	}
	data := struct {
		Page
		Glazes []db.Glaze
	}{s.page(r, "Glazes"), gs}
	return s.render(w, http.StatusOK, "glazes", data)
}

// addGlaze adds a known glaze. The inline "(add a glaze)" on the glaze text
// box asks for JSON and stays on its page; the glazes page gets a redirect.
func (s *Server) addGlaze(w http.ResponseWriter, r *http.Request) error {
	g, err := s.Store.EnsureGlaze(r.Context(), r.FormValue("title"))
	if err != nil {
		return err
	}
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		w.Header().Set("Content-Type", "application/json")
		return json.NewEncoder(w).Encode(map[string]any{"id": g.ID, "name": g.Name})
	}
	return redirect(w, r, glazeURL(g.ID))
}

func (s *Server) glaze(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	g, err := s.Store.GetGlaze(r.Context(), id)
	if err != nil {
		return err
	}
	pieces, err := s.Store.GlazePieces(r.Context(), id)
	if err != nil {
		return err
	}
	data := struct {
		Page
		Glaze  db.Glaze
		Pieces []db.Piece
	}{s.page(r, g.Name), g, pieces}
	return s.render(w, http.StatusOK, "glaze", data)
}

func (s *Server) renameGlaze(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := s.Store.RenameGlaze(r.Context(), id, r.FormValue("title")); err != nil {
		return err
	}
	// A rename can merge into another glaze, deleting this one.
	if _, err := s.Store.GetGlaze(r.Context(), id); err != nil {
		return redirect(w, r, "/glazes")
	}
	return redirect(w, r, glazeURL(id))
}

func (s *Server) deleteGlaze(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := s.Store.DeleteGlaze(r.Context(), id); err != nil {
		return err
	}
	return redirect(w, r, "/glazes")
}
