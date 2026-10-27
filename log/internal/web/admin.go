package web

import "net/http"

// admin is the Admin page (menu → Admin): backups, app ideas, logging out
// and the version, kept out of the menu.
func (s *Server) admin(w http.ResponseWriter, r *http.Request) error {
	return s.render(w, http.StatusOK, "admin", s.page(r, "Admin"))
}
