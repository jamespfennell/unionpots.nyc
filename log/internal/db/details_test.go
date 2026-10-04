package db

import (
	"context"
	"strings"
	"testing"

	"unionpots.nyc/log/internal/model"
)

func TestFormAndGlazes(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ids := create(t, s, NewPieces{Count: 2, Form: " bowl "})
	if d, _ := s.GetDetails(ctx, 121); d.Form != "bowl" {
		t.Fatalf("form = %q", d.Form)
	}

	// Glazes recorded with the glazed step, for every piece in it.
	g := Glazing{Glazes: []string{" Celadon", "Tenmoku", "celadon", ""}, Notes: "dipped, then poured"}
	if err := s.AddEvents(ctx, ids, model.Glazed, "2026-10-10", StepDetails{Glazing: g}); err != nil {
		t.Fatal(err)
	}
	d, _ := s.GetDetails(ctx, 121)
	if strings.Join(d.Glazing.Glazes, "|") != "Celadon|Tenmoku" || d.Glazing.Notes != "dipped, then poured" {
		t.Fatalf("glazing = %+v", d.Glazing)
	}

	// Glazes sent with another step are ignored.
	if err := s.AddEvents(ctx, ids[:1], model.Finished, "2026-10-20", StepDetails{Glazing: Glazing{Glazes: []string{"Shino"}}}); err != nil {
		t.Fatal(err)
	}
	if names, _ := s.GlazeNames(ctx); strings.Join(names, "|") != "Celadon|Tenmoku" {
		t.Fatalf("glaze names = %v", names)
	}

	// Editing replaces; clearing removes.
	if err := s.SetGlazing(ctx, 120, Glazing{Glazes: []string{"Shino"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetForm(ctx, 120, ""); err != nil {
		t.Fatal(err)
	}
	d, _ = s.GetDetails(ctx, 120)
	if d.Form != "" || strings.Join(d.Glazing.Glazes, "|") != "Shino" || d.Glazing.Notes != "" {
		t.Fatalf("after edit: %+v", d)
	}
	if names, _ := s.GlazeNames(ctx); strings.Join(names, "|") != "Celadon|Shino|Tenmoku" {
		t.Fatalf("glaze names = %v", names)
	}
}

func TestRatings(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := create(t, s, NewPieces{})[0]
	if r, _ := s.GetRatings(ctx, id); r != (Ratings{}) {
		t.Fatalf("unrated piece: %+v", r)
	}
	if err := s.SetRatings(ctx, id, Ratings{Glaze: 5, Overall: 4}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.GetRatings(ctx, id); r != (Ratings{Glaze: 5, Overall: 4}) {
		t.Fatalf("ratings: %+v", r)
	}
	if err := s.SetRatings(ctx, id, Ratings{Glaze: 6}); err == nil {
		t.Fatalf("6 should be rejected")
	}
	if err := s.SetRatings(ctx, id, Ratings{Shape: 2}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.GetRatings(ctx, id); r != (Ratings{Shape: 2}) {
		t.Fatalf("ratings should be replaced: %+v", r)
	}
}
