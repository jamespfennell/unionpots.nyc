package web

import "net/http"

// ideas is a single free-text page for ideas about improving the log,
// saved as you type.
func (s *Server) ideas(w http.ResponseWriter, r *http.Request) error {
	text, err := s.Store.AppText(r.Context(), "ideas")
	if err != nil {
		return err
	}
	data := struct {
		Page
		Text string
	}{s.page(r, "App ideas"), text}
	return s.render(w, http.StatusOK, "ideas", data)
}

func (s *Server) saveIdeas(w http.ResponseWriter, r *http.Request) error {
	if err := s.Store.SetAppText(r.Context(), "ideas", r.FormValue("text")); err != nil {
		return err
	}
	if isAutosave(r) {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	return redirect(w, r, "/ideas")
}
