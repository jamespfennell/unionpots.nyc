package web

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"

	"unionpots.nyc/log/internal/db"
	"unionpots.nyc/log/internal/model"
)

// Public pages: one per finished piece, for anyone (no login), styled like
// unionpots.nyc rather than the log. They show what a visitor would want to
// know about a pot (including its history of steps) and nothing private: no
// ratings or notes. No photos either: the page is text only.
// Anything that isn't a finished piece is "not found", and in the demo
// there are no public pages at all.

// publicFact is one line of the public page: "glaze: Nutmeg".
type publicFact struct {
	Label, Text string
	Approximate bool // a history date that's a guess
}

type publicPage struct {
	ID      int64
	Name    string // the project's name, or "" for an unnamed one
	Facts   []publicFact
	History []publicFact // step and date, e.g. "thrown", "Apr 18, 2026"
}

// publicPiece loads a finished piece for its public page, or ErrNotFound.
func (s *Server) publicPiece(r *http.Request) (db.Piece, error) {
	if s.Demo {
		return db.Piece{}, db.ErrNotFound
	}
	id, err := pathID(r, "id")
	if err != nil {
		return db.Piece{}, err
	}
	p, err := s.Store.GetPiece(r.Context(), id)
	if err == nil && p.State != model.StateFinished {
		err = db.ErrNotFound
	}
	return p, err
}

func (s *Server) publicPage(w http.ResponseWriter, r *http.Request) {
	p, err := s.publicPiece(r)
	if err != nil {
		s.publicNotFound(w, err)
		return
	}
	ctx := r.Context()
	pp := publicPage{ID: p.ID, Name: p.ProjectName}
	details, err := s.Store.GetDetails(ctx, p.ID)
	if err != nil {
		s.publicError(w, err)
		return
	}
	clays, err := s.Store.PieceClays(ctx, p.ID)
	if err != nil {
		s.publicError(w, err)
		return
	}
	m, err := s.Store.GetMeasurements(ctx, p.ID)
	if err != nil {
		s.publicError(w, err)
		return
	}

	if p.ProjectName != "" {
		// A single pot has a name; a project's name is the project's, with the pot's
		// place in it written like a print's edition number: "big bowls, 2/2".
		name := strings.ToLower(p.ProjectName)
		if p.ProjectSize > 1 {
			pp.Facts = append(pp.Facts, publicFact{Label: "project", Text: fmt.Sprintf("%s, %d/%d", name, p.ProjectIndex, p.ProjectSize)})
		} else {
			pp.Facts = append(pp.Facts, publicFact{Label: "name", Text: name})
		}
	}
	if details.Form != "" {
		pp.Facts = append(pp.Facts, publicFact{Label: "form", Text: details.Form})
	}
	if len(clays) > 0 {
		var names []string
		for _, c := range clays {
			names = append(names, strings.ToLower(c.Name)+" clay")
		}
		text := strings.Join(names, " and ")
		if m.ClayWeight != 0 {
			text = model.FormatMeasure(m.ClayWeight) + " lb of " + text
		}
		pp.Facts = append(pp.Facts, publicFact{Label: "clay", Text: text})
	}
	if details.GlazeText != "" {
		pp.Facts = append(pp.Facts, publicFact{Label: "glaze", Text: strings.ToLower(details.GlazeText)})
	}
	// Only the finished size: sizes before firing are 10–12% bigger.
	if d, ok := m.Dims[model.Finished]; ok && !d.Empty() {
		pp.Facts = append(pp.Facts, publicFact{Label: "size", Text: publicDims(d)})
	}
	events, err := s.Store.PieceEvents(ctx, p.ID)
	if err != nil {
		s.publicError(w, err)
		return
	}
	// The year shows on the first step and whenever it changes.
	for _, h := range history(events) {
		text := h.Date
		if h.Year != "" {
			if h.Approximate {
				text += " " + h.Year
			} else {
				text += ", " + h.Year
			}
		}
		pp.History = append(pp.History, publicFact{Label: strings.ToLower(h.Label), Text: text, Approximate: h.Approximate})
	}

	var buf bytes.Buffer
	if err := s.pages["public"].ExecuteTemplate(&buf, "public", pp); err != nil {
		s.publicError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	buf.WriteTo(w)
}

// publicDims is "5.25 × 9.5 × 9.5 in", leaving out unmeasured sides.
func publicDims(d model.Dims) string {
	var parts []string
	for _, v := range []float64{d.H, d.W, d.D} {
		if v != 0 {
			parts = append(parts, model.FormatMeasure(v))
		}
	}
	return strings.Join(parts, " × ") + " in"
}

// publicNotFound answers with the same "page not found" page as the
// website, whether the piece doesn't exist or just isn't finished.
func (s *Server) publicNotFound(w http.ResponseWriter, err error) {
	if err != db.ErrNotFound {
		s.publicError(w, err)
		return
	}
	var buf bytes.Buffer
	if err := s.pages["public"].ExecuteTemplate(&buf, "publicNotFound", nil); err != nil {
		s.publicError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	buf.WriteTo(w)
}

func (s *Server) publicError(w http.ResponseWriter, err error) {
	s.Log.Error("public page", "err", err)
	http.Error(w, "Something went wrong.", http.StatusInternalServerError)
}
