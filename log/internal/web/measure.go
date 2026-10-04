package web

import (
	"fmt"
	"math"
	"net/http"
	"strings"

	"unionpots.nyc/log/internal/db"
	"unionpots.nyc/log/internal/model"
)

// Dimension inputs are named h_<key>, w_<key>, d_<key> and round_<key>, where
// key is "step" (next-step card), a step's action (piece page) or "new" (new
// piece form). With round set, depth is the width.
func formDims(r *http.Request, key string) (model.Dims, error) {
	var d model.Dims
	for _, f := range []struct {
		name  string
		label string
		dst   *float64
	}{{"h", "height", &d.H}, {"w", "width", &d.W}, {"d", "depth", &d.D}} {
		v, err := model.ParseMeasure(r.FormValue(f.name + "_" + key))
		if err != nil {
			return d, &db.UserError{Msg: fmt.Sprintf("%s: %v", strings.ToUpper(f.label[:1])+f.label[1:], err)}
		}
		*f.dst = v
	}
	if r.FormValue("round_"+key) == "1" {
		d.D = d.W
	}
	return d, nil
}

func formWeight(r *http.Request, name string) (float64, error) {
	v, err := model.ParseMeasure(r.FormValue(name))
	if err != nil {
		return 0, &db.UserError{Msg: fmt.Sprintf("Clay weight: %v", err)}
	}
	return v, nil
}

// dimsRow is one row of dimension inputs in a template.
type dimsRow struct {
	Key   string // form field suffix
	Label string
	Dims  model.Dims
}

// measureStep is a step's recorded dimensions on the piece page.
type measureStep struct {
	Action model.Action
	Dims   model.Dims
}

// shrinkage summarises how much a piece shrank between being queued for
// bisque and finished, e.g. "height 12%, width 11%". Empty if unknown.
func shrinkage(m db.Measurements) string {
	before, after := m.Dims[model.QueuedBisque], m.Dims[model.Finished]
	var parts []string
	for _, c := range []struct {
		name string
		b, a float64
	}{{"height", before.H, after.H}, {"width", before.W, after.W}, {"depth", before.D, after.D}} {
		if pct := model.Shrinkage(c.b, c.a); c.b != 0 && c.a != 0 {
			parts = append(parts, fmt.Sprintf("%s %d%%", c.name, int(math.Round(pct))))
		}
	}
	return strings.Join(parts, ", ")
}

// measureSteps lists the steps shown in the piece page's measurements form:
// how the piece started (thrown or built), queued for bisque, finished.
func measureSteps(events []db.Event, m db.Measurements) []measureStep {
	start := model.Thrown
	if len(events) > 0 && events[0].Action == model.Built {
		start = model.Built
	}
	var steps []measureStep
	for _, a := range []model.Action{start, model.QueuedBisque, model.Finished} {
		steps = append(steps, measureStep{a, m.Dims[a]})
	}
	return steps
}

func (s *Server) updateMeasurements(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r, "id")
	if err != nil {
		return err
	}
	m := db.Measurements{Dims: map[model.Action]model.Dims{}}
	if m.ClayWeight, err = formWeight(r, "clay_weight"); err != nil {
		return err
	}
	for _, a := range model.MeasuredAt {
		if _, ok := r.Form["h_"+string(a)]; !ok {
			continue
		}
		d, err := formDims(r, string(a))
		if err != nil {
			return err
		}
		m.Dims[a] = d
	}
	// Keep dimensions for steps the form didn't show (e.g. built vs thrown).
	old, err := s.Store.GetMeasurements(r.Context(), id)
	if err != nil {
		return err
	}
	for a, d := range old.Dims {
		if _, shown := r.Form["h_"+string(a)]; !shown {
			m.Dims[a] = d
		}
	}
	if err := s.Store.SetMeasurements(r.Context(), id, m); err != nil {
		return err
	}
	if isAutosave(r) {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	return redirect(w, r, pieceURL(id))
}
