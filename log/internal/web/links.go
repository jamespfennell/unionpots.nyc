package web

import (
	"net/http"

	"unionpots.nyc/log/internal/db"
)

// Links: useful links, each a title and a URL, listed by title and edited
// on the same page.

func (s *Server) links(w http.ResponseWriter, r *http.Request) error {
	ls, err := s.Store.Links(r.Context())
	if err != nil {
		return err
	}
	data := struct {
		Page
		Links []db.Link
	}{s.page(r, "Links"), ls}
	return s.render(w, http.StatusOK, "links", data)
}

func (s *Server) addLink(w http.ResponseWriter, r *http.Request) error {
	if _, err := s.Store.AddLink(r.Context(), r.FormValue("title"), r.FormValue("url")); err != nil {
		return err
	}
	return redirect(w, r, "/links")
}

func (s *Server) updateLink(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := s.Store.UpdateLink(r.Context(), id, r.FormValue("title"), r.FormValue("url")); err != nil {
		return err
	}
	if isAutosave(r) {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	return redirect(w, r, "/links")
}

func (s *Server) deleteLink(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := s.Store.DeleteLink(r.Context(), id); err != nil {
		return err
	}
	return redirect(w, r, "/links")
}
