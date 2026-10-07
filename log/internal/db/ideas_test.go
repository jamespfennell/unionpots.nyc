package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestIdeas(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a, err := s.AddIdea(ctx, "  search page ", false)
	if err != nil || len(a.Hash) != 32 || a.Text != "search page" {
		t.Fatalf("add: %+v %v", a, err)
	}
	b, _ := s.AddIdea(ctx, "export", true)
	if !b.Simple {
		t.Error("simple should be kept")
	}
	if a.Hash == b.Hash {
		t.Fatal("hashes must differ")
	}
	if _, err := s.AddIdea(ctx, "  ", false); err == nil {
		t.Error("empty ideas are refused")
	}
	s.UpdateIdea(ctx, b.ID, "export to zip", false)
	n, err := s.MarkIdeasDone(ctx, []string{a.Hash, "ffffffffffffffffffffffffffffffff"})
	if err != nil || n != 1 {
		t.Fatalf("mark done: %d %v", n, err)
	}
	if n, _ := s.MarkIdeasDone(ctx, []string{a.Hash}); n != 0 {
		t.Errorf("marking again changes nothing")
	}
	open, done, _ := s.Ideas(ctx)
	if len(open) != 1 || open[0].Text != "export to zip" || open[0].Simple || len(done) != 1 || done[0].DoneAt == "" {
		t.Errorf("open %+v done %+v", open, done)
	}
	s.DeleteIdea(ctx, b.ID)
	if open, _, _ := s.Ideas(ctx); len(open) != 0 {
		t.Errorf("deleted: %+v", open)
	}
}

func TestMigration9SplitsFreeTextIdeas(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "log.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	ctx := context.Background()
	if err := migrateTo(ctx, sqlDB, 8); err != nil {
		t.Fatal(err)
	}
	s := &Store{DB: sqlDB}
	s.SetAppText(ctx, "ideas", "- a search page\r\n\n* export to zip\n  rate pieces when finishing  \n")
	if err := Migrate(ctx, sqlDB); err != nil {
		t.Fatal(err)
	}
	open, _, _ := s.Ideas(ctx)
	var texts []string
	for _, i := range open {
		texts = append(texts, i.Text)
		if len(i.Hash) != 32 {
			t.Errorf("hash %q", i.Hash)
		}
	}
	if len(texts) != 3 || texts[0] != "a search page" || texts[1] != "export to zip" || texts[2] != "rate pieces when finishing" {
		t.Errorf("ideas = %q", texts)
	}
	if left, _ := s.AppText(ctx, "ideas"); left != "" {
		t.Errorf("the free text should be cleared: %q", left)
	}
}
