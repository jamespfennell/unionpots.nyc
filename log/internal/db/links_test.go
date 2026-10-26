package db

import (
	"context"
	"testing"
)

func TestLinks(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	for _, bad := range [][2]string{{" ", "example.com"}, {"Form", " "}, {"Form", "javascript:alert(1)"}, {"Form", "ftp://example.com"}} {
		if _, err := s.AddLink(ctx, bad[0], bad[1]); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	a, err := s.AddLink(ctx, " Studio firing form ", " www.example.com/firing ")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddLink(ctx, "clay ordering", "http://clay.example.com"); err != nil {
		t.Fatal(err)
	}
	ls, _ := s.Links(ctx)
	if len(ls) != 2 || ls[0].Title != "clay ordering" || ls[1].URL != "https://www.example.com/firing" || ls[1].Host() != "example.com" {
		t.Fatalf("by title, case-insensitively: %+v", ls)
	}

	if err := s.UpdateLink(ctx, a, "Firing form", "https://example.com/f"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateLink(ctx, a, "Firing form", "nope nope"); err == nil {
		t.Error("the URL must stay a web address")
	}
	if err := s.UpdateLink(ctx, 999, "x", "example.com"); err != ErrNotFound {
		t.Errorf("missing link: %v", err)
	}
	if ls, _ := s.Links(ctx); ls[1].Title != "Firing form" || ls[1].URL != "https://example.com/f" {
		t.Errorf("updated: %+v", ls)
	}

	s.DeleteLink(ctx, a)
	if ls, _ := s.Links(ctx); len(ls) != 1 {
		t.Errorf("deleted: %+v", ls)
	}
}
