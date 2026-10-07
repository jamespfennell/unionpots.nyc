package web

import (
	"bytes"
	"fmt"
	"net/http"

	"unionpots.nyc/log/internal/db"
)

// App ideas: a list of ideas for improving the log. Each has a hidden hash;
// code that implements an idea lists the hash in internal/ideas/done.txt and
// the app marks it done on startup. A public read-only feed lets Claude read
// the open ideas without logging in (see log/CLAUDE.md); ideas aren't
// private (implemented ones end up in public commits anyway).

func (s *Server) ideas(w http.ResponseWriter, r *http.Request) error {
	open, done, err := s.Store.Ideas(r.Context())
	if err != nil {
		return err
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	data := struct {
		Page
		Open, Done []db.Idea
		FeedURL    string
	}{s.page(r, "App ideas"), open, done, fmt.Sprintf("%s://%s/ideas/feed", scheme, r.Host)}
	return s.render(w, http.StatusOK, "ideas", data)
}

func (s *Server) addIdea(w http.ResponseWriter, r *http.Request) error {
	if _, err := s.Store.AddIdea(r.Context(), r.FormValue("text"), r.FormValue("simple") == "1"); err != nil {
		return err
	}
	return redirect(w, r, "/ideas")
}

func (s *Server) updateIdea(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := s.Store.UpdateIdea(r.Context(), id, r.FormValue("text"), r.FormValue("simple") == "1"); err != nil {
		return err
	}
	if isAutosave(r) {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	return redirect(w, r, "/ideas")
}

func (s *Server) deleteIdea(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	if err := s.Store.DeleteIdea(r.Context(), id); err != nil {
		return err
	}
	return redirect(w, r, "/ideas")
}

// notInDemo makes a page "not found" in the demo, where there are no App
// ideas (they'd be visitors' ideas, and could be mistaken for real ones).
func (s *Server) notInDemo(h handlerFunc) handlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		if s.Demo {
			return db.ErrNotFound
		}
		return h(w, r)
	}
}

// ideasFeed is the open ideas as plain text, for Claude: public, no login.
func (s *Server) ideasFeed(w http.ResponseWriter, r *http.Request) {
	if s.Demo {
		http.NotFound(w, r)
		return
	}
	open, _, err := s.Store.Ideas(r.Context())
	if err != nil {
		s.publicError(w, err)
		return
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "# log.unionpots.nyc app ideas: %d open\n", len(open))
	fmt.Fprintf(&b, "# How to address them: log/CLAUDE.md. A done idea's hash goes in log/internal/ideas/done.txt.\n")
	for _, simple := range []bool{true, false} {
		var group []db.Idea
		for _, i := range open {
			if i.Simple == simple {
				group = append(group, i)
			}
		}
		if simple {
			fmt.Fprintf(&b, "\n# Simple ideas (%d): implement each as its own commit on main and push\n", len(group))
		} else {
			fmt.Fprintf(&b, "\n# Other ideas (%d): address one at a time, interactively\n", len(group))
		}
		for _, i := range group {
			fmt.Fprintf(&b, "\n## %s  (added %s)\n%s\n", i.Hash, i.CreatedAt[:min(10, len(i.CreatedAt))], i.Text)
		}
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	b.WriteTo(w)
}
