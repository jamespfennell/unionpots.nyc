package demo

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"unionpots.nyc/log/internal/db"
	"unionpots.nyc/log/internal/model"
	"unionpots.nyc/log/internal/photos"
)

func TestResetLoadsSampleDataAndWipesChanges(t *testing.T) {
	dir := t.TempDir()
	sqlDB, err := db.Open(filepath.Join(dir, "log.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx, sqlDB); err != nil {
		t.Fatal(err)
	}
	s := &db.Store{DB: sqlDB, MinPieceID: 1}
	files := &photos.Store{Dir: filepath.Join(dir, "photos")}
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

	count := func() map[model.State]int {
		m := map[model.State]int{}
		for _, st := range []model.State{model.StateStarted, model.PendingTrimming, model.Drying, model.PendingBisque, model.PendingGlaze, model.StateFinished, model.StateBroken} {
			ps, err := s.PiecesInState(ctx, st)
			if err != nil {
				t.Fatal(err)
			}
			m[st] = len(ps)
		}
		return m
	}
	if err := Reset(ctx, s, files, now); err != nil {
		t.Fatal(err)
	}
	first := count()
	for st, n := range first {
		if n == 0 {
			t.Errorf("no sample piece in state %s", st)
		}
	}
	// A visitor adds a piece; the next reset puts everything back.
	if _, err := s.CreatePieces(ctx, db.NewPieces{Count: 3, Action: model.Thrown, Date: model.Today(now)}); err != nil {
		t.Fatal(err)
	}
	if err := Reset(ctx, s, files, now.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	again := count()
	for st, n := range first {
		if again[st] != n {
			t.Errorf("after reset, %s has %d pieces, want %d", st, again[st], n)
		}
	}
	if next, _ := s.NextPieceID(ctx); next != 15 {
		t.Errorf("numbering should restart: next piece is %d", next)
	}
	// Sample photos are there, and their files exist.
	ps, _ := s.PiecesInState(ctx, model.StateFinished)
	ph, _ := s.PiecePhotos(ctx, ps[0].ID)
	if len(ph) != 2 || files.ServablePath(ph[0].SHA256+"_600.jpg") == "" {
		t.Fatalf("finished sample piece should have 2 photos: %+v", ph)
	}
}
