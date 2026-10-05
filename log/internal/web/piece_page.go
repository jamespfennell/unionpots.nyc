package web

import (
	"net/http"
	"strings"

	"unionpots.nyc/log/internal/db"
	"unionpots.nyc/log/internal/model"
)

// piecePage is the data behind both the piece page (status, next step,
// history, notes, read-only details) and its edit page (everything else).
type piecePage struct {
	Page
	Piece        db.Piece
	Events       []db.Event
	Project      db.Project
	ShowProject  bool
	Siblings     []db.Piece     // same project and state; an action can apply to them too
	Undo         *db.Event      // the latest event, if it can be undone (not the only one)
	NextOptions  []model.Action // ways on from here; a started piece can be thrown or built
	NextDims     []dimsRow      // dimension inputs for the next step, if it is measured
	NextGlazes   bool           // the next step is glazing, so the card asks for glazes
	Ratings      []ratingRow
	Facts        []fact // read-only details on the piece page
	Measurements db.Measurements
	StepDims     []dimsRow // the measurements form: one row per measured step
	Clays        []db.Clay // all clay bodies, for the pills
	ClayIDs      []int64   // the ones this piece is made from
	Details      db.PieceDetails
	GlazeNames   []string // known glazes, recognised in glaze text and suggested while typing
	ShowGlazes   bool     // the edit page offers the glaze text once the piece is (being) glazed
	Photos       photoSection
}

// fact is one read-only detail: a label and one or more values, optionally
// linked, plus an optional muted note. Text, if set, is shown instead of
// Items as one run of text with some parts linked (glaze text).
type fact struct {
	Label string
	Items []factItem
	Text  []factItem
	Note  string
}

type factItem struct {
	Text, Href string
	Hover      string // optional explanation shown on hover
}

func textFact(label, text string) fact { return fact{Label: label, Items: []factItem{{Text: text}}} }

func (s *Server) loadPiecePage(r *http.Request) (*piecePage, error) {
	ctx := r.Context()
	id, err := pathID(r, "id")
	if err != nil {
		return nil, err
	}
	p, err := s.Store.GetPiece(ctx, id)
	if err != nil {
		return nil, err
	}
	events, err := s.Store.PieceEvents(ctx, id)
	if err != nil {
		return nil, err
	}
	proj, err := s.Store.GetProject(ctx, p.ProjectID)
	if err != nil {
		return nil, err
	}
	m, err := s.Store.GetMeasurements(ctx, id)
	if err != nil {
		return nil, err
	}
	allClays, err := s.Store.Clays(ctx)
	if err != nil {
		return nil, err
	}
	pieceClays, err := s.Store.PieceClays(ctx, id)
	if err != nil {
		return nil, err
	}
	details, err := s.Store.GetDetails(ctx, id)
	if err != nil {
		return nil, err
	}
	glazes, err := s.Store.Glazes(ctx)
	if err != nil {
		return nil, err
	}
	var glazeNames []string
	for _, g := range glazes {
		glazeNames = append(glazeNames, g.Name)
	}
	ratings, err := s.Store.GetRatings(ctx, id)
	if err != nil {
		return nil, err
	}

	pp := &piecePage{
		Page:         s.page(r, p.Title()),
		Piece:        p,
		Events:       events,
		Project:      proj,
		ShowProject:  len(proj.Pieces) > 1,
		Siblings:     sameState(proj, p),
		NextOptions:  p.State.NextOptions(),
		NextGlazes:   p.State.Next() == model.Glazed,
		Measurements: m,
		Clays:        allClays,
		Details:      details,
		GlazeNames:   glazeNames,
		ShowGlazes:   details.GlazeText != "",
	}
	for _, c := range pieceClays {
		pp.ClayIDs = append(pp.ClayIDs, c.ID)
	}
	for _, e := range events {
		pp.ShowGlazes = pp.ShowGlazes || e.Action == model.Glazed
	}
	// The next-step card asks for dimensions when the next step is measured.
	// One set applies to every included piece; pieces that differ are
	// recorded one at a time.
	if p.State.Next().Measurable() {
		pp.NextDims = []dimsRow{{Key: "step"}}
	}
	for _, st := range measureSteps(events, m) {
		pp.StepDims = append(pp.StepDims, dimsRow{Key: string(st.Action), Label: sizeLabel(st.Action), Dims: st.Dims})
	}
	if p.State == model.StateFinished || ratings != (db.Ratings{}) {
		pp.Ratings = ratingRows(ratings)
	}
	if len(events) > 1 {
		pp.Undo = &events[len(events)-1]
	}
	pp.Facts = pieceFacts(events, m, pieceClays, details, glazes)
	if pp.Photos.Photos, err = s.Store.PiecePhotos(r.Context(), p.ID); err != nil {
		return pp, err
	}
	pp.Photos.PieceID, pp.Photos.Demo = p.ID, s.Demo
	return pp, nil
}

// pieceFacts lists what's been recorded about a piece, in the order it
// happens: form and clay at the start, sizes along the way, then glazes.
func pieceFacts(events []db.Event, m db.Measurements, clays []db.Clay, d db.PieceDetails, glazes []db.Glaze) []fact {
	var facts []fact
	if d.Form != "" {
		facts = append(facts, textFact("Form", d.Form))
	}
	if len(clays) > 0 {
		f := fact{Label: "Clay"}
		for _, c := range clays {
			f.Items = append(f.Items, factItem{Text: c.Name, Href: clayURL(c.ID)})
		}
		facts = append(facts, f)
	}
	if m.ClayWeight != 0 {
		facts = append(facts, textFact("Clay weight", model.FormatMeasure(m.ClayWeight)+" lb"))
	}
	for _, st := range measureSteps(events, m) {
		if !st.Dims.Empty() {
			facts = append(facts, fact{Label: sizeLabel(st.Action), Items: []factItem{{Text: formatDims(st.Dims), Hover: dimsOrder}}})
		}
	}
	if sh := shrinkage(m); sh != "" {
		facts = append(facts, textFact("Shrinkage", sh+" (bisque queue to finished)"))
	}
	if d.GlazeText != "" {
		facts = append(facts, fact{Label: "Glazes", Text: glazeSegments(d.GlazeText, glazes)})
	}
	return facts
}

// glazeSegments splits glaze text into plain runs and known glaze names,
// which link to their glaze pages.
func glazeSegments(text string, glazes []db.Glaze) []factItem {
	ids := map[string]int64{}
	var names []string
	for _, g := range glazes {
		ids[strings.ToLower(g.Name)] = g.ID
		names = append(names, g.Name)
	}
	var segs []factItem
	last := 0
	for _, m := range model.MatchGlazes(text, names) {
		if m.Start > last {
			segs = append(segs, factItem{Text: text[last:m.Start]})
		}
		segs = append(segs, factItem{Text: text[m.Start:m.End], Href: glazeURL(ids[strings.ToLower(m.Name)])})
		last = m.End
	}
	if last < len(text) {
		segs = append(segs, factItem{Text: text[last:]})
	}
	return segs
}

// sizeLabel names the size taken at a step.
func sizeLabel(a model.Action) string {
	switch a {
	case model.Thrown:
		return "Thrown size"
	case model.Built:
		return "Built size"
	case model.QueuedBisque:
		return "Bone dry size"
	case model.Finished:
		return "Finished size"
	}
	return a.Label() + " size"
}

// dimsOrder explains formatDims' order, shown on hover.
const dimsOrder = "height × width × depth"

// formatDims shows a size compactly: "5in × 6in × 4in" (height × width ×
// depth), with "–" for anything not measured.
func formatDims(d model.Dims) string {
	part := func(v float64) string {
		if v == 0 {
			return "–"
		}
		return model.FormatMeasure(v) + "in"
	}
	return part(d.H) + " × " + part(d.W) + " × " + part(d.D)
}

func (s *Server) piece(w http.ResponseWriter, r *http.Request) error {
	pp, err := s.loadPiecePage(r)
	if err != nil {
		return err
	}
	return s.render(w, http.StatusOK, "piece", pp)
}

func (s *Server) pieceEdit(w http.ResponseWriter, r *http.Request) error {
	pp, err := s.loadPiecePage(r)
	if err != nil {
		return err
	}
	pp.Title = "Edit " + pp.Piece.Title()
	pp.Photos.Edit = true
	return s.render(w, http.StatusOK, "piece_edit", pp)
}
