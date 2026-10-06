package db

import (
	"context"
	"testing"
)

func TestAppText(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if got, err := s.AppText(ctx, "ideas"); err != nil || got != "" {
		t.Fatalf("unwritten: %q, %v", got, err)
	}
	s.SetAppText(ctx, "ideas", "first")
	s.SetAppText(ctx, "ideas", "- search page\n- export")
	if got, _ := s.AppText(ctx, "ideas"); got != "- search page\n- export" {
		t.Fatalf("got %q", got)
	}
}
