package web

import (
	"net/http"
	"strconv"
	"time"

	"unionpots.nyc/log/internal/db"
)

// Notes: general notes, each with a title and text, listed most recently
// edited first.

func noteURL(id int64) string { return "/notes/" + strconv.FormatInt(id, 10) }

func (s *Server) notes(w http.ResponseWriter, r *http.Request) error {
	ns, err := s.Store.Notes(r.Context())
	if err != nil {
		return err
	}
	data := struct {
		Page
		Notes []db.Note
		Now   time.Time
	}{s.page(r, "Notes"), ns, s.Now()}
	return s.render(w, http.StatusOK, "notes", data)
}

func (s *Server) addNote(w http.ResponseWriter, r *http.Request) error {
	id, err := s.Store.AddNote(r.Context(), r.FormValue("title"))
	if err != nil {
		return err
	}
	return redirect(w, r, noteURL(id))
}

func (s *Server) note(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	n, err := s.Store.GetNote(r.Context(), id)
	if err != nil {
		return err
	}
	data := struct {
		Page
		Note db.Note
	}{s.page(r, n.Title), n}
	return s.render(w, http.StatusOK, "note", data)
}

func (s *Server) updateNote(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := s.Store.UpdateNote(r.Context(), id, r.FormValue("title"), r.FormValue("text")); err != nil {
		return err
	}
	if isAutosave(r) {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	return redirect(w, r, noteURL(id))
}

func (s *Server) deleteNote(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := s.Store.DeleteNote(r.Context(), id); err != nil {
		return err
	}
	return redirect(w, r, "/notes")
}
