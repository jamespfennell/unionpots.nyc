package web

import (
	"net/http"
	"net/url"
	"sort"
	"strings"

	"unionpots.nyc/log/internal/db"
)

// Filters on the finished pieces page: clay, glaze, form and studio, by
// name, from the query string (so a filtered view can be bookmarked). They
// combine; a glaze matches any piece whose glazes include it, and a form
// matches loosely ("bowl" finds "bowls" and "pasta bowl").

type finishedFilters struct {
	Clay, Glaze, Form, Studio string
}

func (f finishedFilters) Any() bool { return f != finishedFilters{} }

func readFinishedFilters(r *http.Request) finishedFilters {
	q := r.URL.Query()
	return finishedFilters{
		Clay: strings.TrimSpace(q.Get("clay")), Glaze: strings.TrimSpace(q.Get("glaze")),
		Form: strings.TrimSpace(q.Get("form")), Studio: strings.TrimSpace(q.Get("studio")),
	}
}

// href is this page with the filters, in the given view.
func (f finishedFilters) href(byPiece bool) string {
	q := url.Values{}
	if byPiece {
		q.Set("view", "pieces")
	}
	for k, v := range map[string]string{"clay": f.Clay, "glaze": f.Glaze, "form": f.Form, "studio": f.Studio} {
		if v != "" {
			q.Set(k, v)
		}
	}
	if len(q) == 0 {
		return "/finished"
	}
	return "/finished?" + q.Encode()
}

// pieceFacets is what a finished piece can be filtered by.
type pieceFacets struct {
	Clays, Glazes []string
	Form, Studio  string
}

// formKey reduces a form to what loose matching compares: lower case and
// singular ("Bowls" → "bowl").
func formKey(form string) string {
	f := strings.ToLower(strings.TrimSpace(form))
	if len(f) > 3 && strings.HasSuffix(f, "s") && !strings.HasSuffix(f, "ss") {
		f = strings.TrimSuffix(f, "s")
	}
	return f
}

func (f finishedFilters) match(p pieceFacets) bool {
	has := func(list []string, want string) bool {
		for _, v := range list {
			if strings.EqualFold(v, want) {
				return true
			}
		}
		return false
	}
	switch {
	case f.Clay != "" && !has(p.Clays, f.Clay):
		return false
	case f.Glaze != "" && !has(p.Glazes, f.Glaze):
		return false
	case f.Studio != "" && !strings.EqualFold(p.Studio, f.Studio):
		return false
	case f.Form != "" && !strings.Contains(strings.ToLower(p.Form), formKey(f.Form)):
		return false
	}
	return true
}

// finishedOptions are the values each dropdown offers: those that occur on
// finished pieces.
type finishedOptions struct {
	Clays, Glazes, Forms, Studios []string
}

// filterFinished loads each piece's facets, keeps the matching pieces and
// collects the dropdowns' options (from all finished pieces).
func (s *Server) filterFinished(r *http.Request, pieces []db.Piece, f finishedFilters) ([]db.Piece, finishedOptions, error) {
	sets := [4]map[string]string{{}, {}, {}, {}} // lower → shown, for clays, glazes, forms, studios
	add := func(i int, v string) {
		if k := strings.ToLower(v); k != "" {
			if _, ok := sets[i][k]; !ok {
				sets[i][k] = v
			}
		}
	}
	var kept []db.Piece
	for _, p := range pieces {
		clays, err := s.Store.PieceClays(r.Context(), p.ID)
		if err != nil {
			return nil, finishedOptions{}, err
		}
		d, err := s.Store.GetDetails(r.Context(), p.ID)
		if err != nil {
			return nil, finishedOptions{}, err
		}
		pf := pieceFacets{Glazes: d.Glazes, Form: d.Form, Studio: p.StudioName}
		for _, c := range clays {
			pf.Clays = append(pf.Clays, c.Name)
			add(0, c.Name)
		}
		for _, g := range d.Glazes {
			add(1, g)
		}
		add(2, formKey(d.Form))
		add(3, p.StudioName)
		if f.match(pf) {
			kept = append(kept, p)
		}
	}
	var opts finishedOptions
	for i, dst := range []*[]string{&opts.Clays, &opts.Glazes, &opts.Forms, &opts.Studios} {
		for _, v := range sets[i] {
			*dst = append(*dst, v)
		}
		sort.Slice(*dst, func(a, b int) bool { return strings.ToLower((*dst)[a]) < strings.ToLower((*dst)[b]) })
	}
	return kept, opts, nil
}
