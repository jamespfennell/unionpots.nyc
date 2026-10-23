package db

import (
	"context"
	"testing"
)

func TestNotes(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if _, err := s.AddNote(ctx, "  "); err == nil {
		t.Error("a note needs a title")
	}
	a, err := s.AddNote(ctx, " Kiln schedule ")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.AddNote(ctx, "Glaze ratios")
	ns, _ := s.Notes(ctx)
	if len(ns) != 2 || ns[0].ID != b || ns[1].Title != "Kiln schedule" || ns[1].Updated().IsZero() {
		t.Fatalf("newest first: %+v", ns)
	}

	// Editing a moves it to the top; saving it unchanged doesn't move it.
	if err := s.UpdateNote(ctx, a, "Kiln schedule", "bisque on Mondays"); err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetNote(ctx, a)
	s.UpdateNote(ctx, a, "Kiln schedule", "bisque on Mondays")
	if after, _ := s.GetNote(ctx, a); after.UpdatedAt != before.UpdatedAt || after.Text != "bisque on Mondays" {
		t.Errorf("unchanged save: %+v then %+v", before, after)
	}
	if ns, _ := s.Notes(ctx); ns[0].ID != a {
		t.Errorf("edited note first: %+v", ns)
	}
	if err := s.UpdateNote(ctx, a, " ", "x"); err == nil {
		t.Error("the title can't be emptied")
	}
	if err := s.UpdateNote(ctx, 999, "x", ""); err != ErrNotFound {
		t.Errorf("missing note: %v", err)
	}

	s.DeleteNote(ctx, a)
	if _, err := s.GetNote(ctx, a); err != ErrNotFound {
		t.Errorf("deleted: %v", err)
	}
}
