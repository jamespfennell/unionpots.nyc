package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"unionpots.nyc/log/internal/model"
)

func TestStudios(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if st, _ := s.Studios(ctx); len(st) != 0 {
		t.Fatalf("a fresh log has no studios: %+v", st)
	}
	cw, _ := s.EnsureStudio(ctx, "Clayworks")
	if again, _ := s.EnsureStudio(ctx, " clayworks "); again != cw {
		t.Errorf("EnsureStudio should find an existing name, ignoring case")
	}
	home, _ := s.EnsureStudio(ctx, "Home")
	ids := create(t, s, NewPieces{Count: 2, StudioID: cw})
	if last, _ := s.LastUsedStudio(ctx); last != cw {
		t.Errorf("last used = %d", last)
	}
	p, _ := s.GetPiece(ctx, ids[0])
	if p.StudioID != cw || p.StudioName != "Clayworks" {
		t.Errorf("piece studio = %d %q", p.StudioID, p.StudioName)
	}
	s.SetPieceStudio(ctx, ids[1], home)
	if last, _ := s.LastUsedStudio(ctx); last != home {
		t.Errorf("last used follows the newest piece: %d", last)
	}
	st, _ := s.Studios(ctx)
	if len(st) != 2 || st[0].Name != "Clayworks" || st[0].Pieces != 1 || st[1].Pieces != 1 {
		t.Errorf("studios = %+v", st)
	}
	if err := s.RenameStudio(ctx, home, "Clayworks"); err == nil {
		t.Errorf("rename onto an existing name should fail")
	}
	s.RenameStudio(ctx, home, "Greenpoint")
	var ue *UserError
	if err := s.DeleteStudio(ctx, cw); !errors.As(err, &ue) {
		t.Errorf("a used studio can't be deleted: %v", err)
	}
	empty, _ := s.EnsureStudio(ctx, "Never used")
	if err := s.DeleteStudio(ctx, empty); err != nil {
		t.Errorf("delete unused: %v", err)
	}
	if _, err := s.CreatePieces(ctx, NewPieces{Count: 1, StudioID: 999}); !errors.As(err, &ue) {
		t.Errorf("unknown studio: %v", err)
	}
}

func TestMigration8TagsExistingPiecesClayworks(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "log.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	ctx := context.Background()
	if err := migrateTo(ctx, sqlDB, 7); err != nil {
		t.Fatal(err)
	}
	s := &Store{DB: sqlDB}
	create(t, s, NewPieces{Count: 2, Action: model.Thrown})
	if err := Migrate(ctx, sqlDB); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 2} {
		if p, _ := s.GetPiece(ctx, id); p.StudioName != "Clayworks" {
			t.Errorf("#%d studio = %q, want Clayworks", id, p.StudioName)
		}
	}
}
