// Package demo fills a log with sample pieces for the public demo, and
// resets it to them every day. Dates are relative to today, so the demo
// always looks current.
package demo

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"time"

	"unionpots.nyc/log/internal/db"
	"unionpots.nyc/log/internal/model"
	"unionpots.nyc/log/internal/photos"
)

// ResetEvery is how often the demo goes back to the sample data.
const ResetEvery = 24 * time.Hour

// Reset deletes everything in the log and loads the sample pieces.
func Reset(ctx context.Context, s *db.Store, files *photos.Store, now time.Time) error {
	if err := s.ResetAll(ctx); err != nil {
		return err
	}
	return seed(ctx, s, files, now)
}

type step struct {
	action  model.Action
	daysAgo int
	details db.StepDetails
}

type project struct {
	name    string
	count   int
	form    string
	clay    string
	weight  float64
	steps   []step // the first one creates the pieces
	notes   string
	ratings db.Ratings
	photos  []pot // notes, ratings and photos go on the first piece
}

func seed(ctx context.Context, s *db.Store, files *photos.Store, now time.Time) error {
	day := func(ago int) string { return model.Today(now.AddDate(0, 0, -ago)) }

	clays := map[string]int64{}
	for _, c := range []db.Clay{
		{Name: "Speckled Buff", Code: "SB-5", Price: "$32 / 50 lb", Notes: "Cone 6 stoneware with iron speckles."},
		{Name: "B-Mix", Code: "BM-5", Price: "$30 / 50 lb", Notes: "Smooth, throws well."},
	} {
		id, err := s.EnsureClay(ctx, c.Name)
		if err != nil {
			return err
		}
		c.ID = id
		if err := s.UpdateClay(ctx, c); err != nil {
			return err
		}
		clays[c.Name] = id
	}
	for _, g := range []string{"Floating Blue", "Shino", "Tenmoku", "Celadon"} {
		if _, err := s.EnsureGlaze(ctx, g); err != nil {
			return err
		}
	}

	projects := []project{
		{name: "Teapot", count: 1, form: "teapot", clay: "B-Mix", weight: 3,
			steps: []step{{action: model.Thrown, daysAgo: 30}, {action: model.Broken, daysAgo: 28}},
			notes: "Cracked at the spout join while trimming."},
		{name: "Bud vases", count: 2, form: "vase", clay: "Speckled Buff", weight: 1,
			steps: []step{
				{action: model.Thrown, daysAgo: 35, details: db.StepDetails{Dims: model.Dims{H: 6, W: 3, D: 3}}},
				{action: model.Trimmed, daysAgo: 33},
				{action: model.QueuedBisque, daysAgo: 25, details: db.StepDetails{Dims: model.Dims{H: 5.5, W: 2.75, D: 2.75}}},
				{action: model.Glazed, daysAgo: 15, details: db.StepDetails{GlazeText: "Celadon inside and out, wiped back at the foot"}},
				{action: model.Finished, daysAgo: 6, details: db.StepDetails{Dims: model.Dims{H: 5, W: 2.5, D: 2.5}}},
			},
			notes:   "Pooled nicely in the throwing lines.",
			ratings: db.Ratings{Glaze: 5, Shape: 4, Overall: 4},
			photos: []pot{
				{w: 900, h: 1200, body: color.RGBA{150, 182, 160, 255}, shape: vase},
				{w: 1200, h: 900, body: color.RGBA{140, 175, 152, 255}, shape: vase},
			}},
		{name: "Serving bowl", count: 1, form: "bowl", clay: "Speckled Buff", weight: 4,
			steps: []step{
				{action: model.Thrown, daysAgo: 20, details: db.StepDetails{Dims: model.Dims{H: 4, W: 11, D: 11}}},
				{action: model.Trimmed, daysAgo: 18},
				{action: model.QueuedBisque, daysAgo: 10, details: db.StepDetails{Dims: model.Dims{H: 3.75, W: 10.25, D: 10.25}}},
				{action: model.Glazed, daysAgo: 3, details: db.StepDetails{GlazeText: "Shino with Tenmoku on the rim"}},
			},
			photos: []pot{{w: 1200, h: 800, body: color.RGBA{214, 196, 170, 255}, shape: bowl}}},
		{name: "Small bowls", count: 3, form: "bowl", clay: "B-Mix", weight: 1.5,
			steps: []step{
				{action: model.Thrown, daysAgo: 14},
				{action: model.Trimmed, daysAgo: 12},
				{action: model.QueuedBisque, daysAgo: 1, details: db.StepDetails{Dims: model.Dims{H: 2.5, W: 5.5, D: 5.5}}},
			}},
		{name: "Set of 4 mugs", count: 4, form: "mug", clay: "Speckled Buff", weight: 1,
			steps: []step{{action: model.Thrown, daysAgo: 9}, {action: model.Trimmed, daysAgo: 8}},
			notes: "Handles pulled the day after trimming."},
		{name: "Dinner plates", count: 2, form: "plate", clay: "B-Mix", weight: 3.5,
			steps: []step{{action: model.Thrown, daysAgo: 2, details: db.StepDetails{Dims: model.Dims{H: 1.25, W: 11, D: 11}}}}},
		{name: "Tall vase", count: 1, form: "vase", clay: "B-Mix",
			steps: []step{{action: model.Started, daysAgo: 1}},
			notes: "Coil-built; adding a few rows each session."},
	}

	for _, p := range projects {
		first := p.steps[0]
		ids, err := s.CreatePieces(ctx, db.NewPieces{
			Count: p.count, Name: p.name, Action: first.action, Date: day(first.daysAgo),
			Form: p.form, ClayIDs: []int64{clays[p.clay]}, ClayWeight: p.weight, Dims: first.details.Dims,
		})
		if err != nil {
			return fmt.Errorf("%s: %w", p.name, err)
		}
		for _, st := range p.steps[1:] {
			if err := s.AddEvents(ctx, ids, st.action, day(st.daysAgo), st.details); err != nil {
				return fmt.Errorf("%s, %s: %w", p.name, st.action, err)
			}
		}
		target := ids[0]
		if p.notes != "" {
			if err := s.UpdatePieceNotes(ctx, target, p.notes); err != nil {
				return err
			}
		}
		if p.ratings != (db.Ratings{}) {
			if err := s.SetRatings(ctx, target, p.ratings); err != nil {
				return err
			}
		}
		for _, pt := range p.photos {
			ph, err := files.Ingest(pt.jpeg())
			if err != nil {
				return err
			}
			if _, err := s.AddPhoto(ctx, target, ph.SHA256, ph.Ext, ph.Width, ph.Height); err != nil {
				return err
			}
		}
	}
	return nil
}

// pot is a placeholder photo: a simple shaded pot on a plain background.
type pot struct {
	w, h  int
	body  color.RGBA
	shape func(t float64) float64 // half-width (0–1) at height t (0 = foot, 1 = rim)
}

func vase(t float64) float64 { return 0.22 + 0.2*math.Sin(math.Pi*t*0.95) - 0.1*t }
func bowl(t float64) float64 { return 0.25 + 0.45*math.Sqrt(t) }

func (p pot) jpeg() []byte {
	img := image.NewRGBA(image.Rect(0, 0, p.w, p.h))
	bg := color.RGBA{238, 233, 225, 255}
	potH := float64(p.h) * 0.62
	scale := math.Min(float64(p.w), float64(p.h)) * 0.5
	top, bottom := (float64(p.h)-potH)/2, (float64(p.h)+potH)/2
	cx := float64(p.w) / 2
	for y := 0; y < p.h; y++ {
		for x := 0; x < p.w; x++ {
			c := bg
			if fy := float64(y); fy >= top && fy <= bottom {
				t := (bottom - fy) / potH
				half := p.shape(t) * scale
				if dx := (float64(x) - cx) / half; math.Abs(dx) <= 1 {
					shade := 0.75 + 0.35*math.Cos(dx*math.Pi/2) - 0.15*dx
					c = color.RGBA{clamp(float64(p.body.R) * shade), clamp(float64(p.body.G) * shade), clamp(float64(p.body.B) * shade), 255}
				}
			} else if fy > bottom && fy < bottom+potH*0.04 && math.Abs(float64(x)-cx) < p.shape(0)*scale*1.3 {
				c = color.RGBA{215, 208, 198, 255} // shadow
			}
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85})
	return buf.Bytes()
}

func clamp(v float64) uint8 { return uint8(math.Max(0, math.Min(255, v))) }
