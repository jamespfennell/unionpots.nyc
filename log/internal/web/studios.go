package web

import (
	"net/http"
	"strconv"

	"unionpots.nyc/log/internal/db"
)

// studioCookie remembers which studio In progress is showing, so it stays
// filtered (say, to the studio you're at) until you pick another.
const studioCookie = "log_studio"

// formStudio reads the chosen studio ("studio"). Pieces added to an
// existing project without a choice are made where the project is.
func (s *Server) formStudio(r *http.Request, projectID int64) (int64, error) {
	if _, ok := r.Form["studio"]; ok || projectID == 0 {
		return formInt(r, "studio", 0)
	}
	proj, err := s.Store.GetProject(r.Context(), projectID)
	if err != nil || len(proj.Pieces) == 0 {
		return 0, err
	}
	return proj.Pieces[0].StudioID, nil
}

// studios lists where pieces are made: add, rename, and delete unused ones.
func (s *Server) studios(w http.ResponseWriter, r *http.Request) error {
	st, err := s.Store.Studios(r.Context())
	if err != nil {
		return err
	}
	data := struct {
		Page
		Studios []db.Studio
	}{s.page(r, "Studios"), st}
	return s.render(w, http.StatusOK, "studios", data)
}

func (s *Server) addStudio(w http.ResponseWriter, r *http.Request) error {
	if _, err := s.Store.EnsureStudio(r.Context(), r.FormValue("title")); err != nil {
		return err
	}
	return redirect(w, r, "/studios")
}

func (s *Server) renameStudio(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := s.Store.RenameStudio(r.Context(), id, r.FormValue("title")); err != nil {
		return err
	}
	if isAutosave(r) {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	return redirect(w, r, "/studios")
}

func (s *Server) deleteStudio(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := s.Store.DeleteStudio(r.Context(), id); err != nil {
		return err
	}
	return redirect(w, r, "/studios")
}

// studioFilter is the studio In progress shows (0 = all): "?studio=<id>" or
// "?studio=all" chooses and remembers it; otherwise the remembered one. A
// studio that no longer exists, or a log with fewer than two, shows all.
func (s *Server) studioFilter(w http.ResponseWriter, r *http.Request, studios []db.Studio) int64 {
	choice := r.URL.Query().Get("studio")
	if choice != "" {
		http.SetCookie(w, &http.Cookie{Name: studioCookie, Value: choice, Path: "/", MaxAge: 365 * 24 * 3600,
			HttpOnly: true, Secure: isHTTPS(r), SameSite: http.SameSiteLaxMode})
	} else if c, err := r.Cookie(studioCookie); err == nil {
		choice = c.Value
	}
	if len(studios) < 2 {
		return 0
	}
	id, _ := strconv.ParseInt(choice, 10, 64)
	for _, st := range studios {
		if st.ID == id {
			return id
		}
	}
	return 0
}
