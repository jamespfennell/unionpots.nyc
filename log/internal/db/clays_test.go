package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestClays(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	speckle, err := s.EnsureClay(ctx, "Speckled buff")
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := s.EnsureClay(ctx, " speckled BUFF "); again != speckle {
		t.Fatalf("EnsureClay should match names ignoring case and spaces")
	}
	porcelain, _ := s.EnsureClay(ctx, "Porcelain")
	white, _ := s.EnsureClay(ctx, "White stoneware")

	if ids, _ := s.LastUsedClays(ctx); len(ids) != 0 {
		t.Fatalf("no pieces yet, so no default: %v", ids)
	}
	create(t, s, NewPieces{ClayIDs: []int64{porcelain}})      // #120
	create(t, s, NewPieces{ClayIDs: []int64{speckle, white}}) // #121, two clays
	create(t, s, NewPieces{})                                 // #122, none

	if ids, _ := s.LastUsedClays(ctx); len(ids) != 2 || ids[0] != speckle || ids[1] != white {
		t.Fatalf("default should be the latest piece's clays: %v", ids)
	}
	clays, _ := s.Clays(ctx)
	var order []string
	for _, c := range clays {
		order = append(order, c.Name)
	}
	if got := order; len(got) != 3 || got[2] != "Porcelain" {
		t.Fatalf("most recently used first: %v", got)
	}
	if cs, _ := s.PieceClays(ctx, 121); len(cs) != 2 || cs[0].Name != "Speckled buff" {
		t.Fatalf("piece clays: %+v", cs)
	}

	// Change a piece's clay.
	if err := s.SetPieceClays(ctx, 121, []int64{porcelain}); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.GetClay(ctx, porcelain); c.Pieces != 2 {
		t.Fatalf("porcelain used by %d pieces, want 2", c.Pieces)
	}

	// Details, and unique names.
	if err := s.UpdateClay(ctx, Clay{ID: white, Name: "Porcelain"}); err == nil {
		t.Fatalf("renaming onto an existing name should fail")
	}
	if err := s.UpdateClay(ctx, Clay{ID: white, Name: "White SW", Notes: "WC-401, $32 / 25 lb"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.GetClay(ctx, white); c.Name != "White SW" || c.Notes != "WC-401, $32 / 25 lb" {
		t.Fatalf("details: %+v", c)
	}

	// Only unused clays can be deleted.
	var ue *UserError
	if err := s.DeleteClay(ctx, porcelain); !errors.As(err, &ue) {
		t.Fatalf("deleting a used clay: %v", err)
	}
	if err := s.DeleteClay(ctx, white); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetClay(ctx, white); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted clay still there: %v", err)
	}
}

func TestMigration10DropsClayCodeAndPrice(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "log.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	ctx := context.Background()
	if err := migrateTo(ctx, sqlDB, 9); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec("INSERT INTO clays (name, code, price, notes) VALUES ('Brown', 'B-1', '$30', 'nice')"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, sqlDB); err != nil {
		t.Fatal(err)
	}
	s := &Store{DB: sqlDB}
	cs, err := s.Clays(ctx)
	if err != nil || len(cs) != 1 || cs[0].Name != "Brown" || cs[0].Notes != "nice" {
		t.Fatalf("clays after migration: %+v %v", cs, err)
	}
	if _, err := sqlDB.Exec("SELECT code FROM clays"); err == nil {
		t.Errorf("the code column should be gone")
	}
}
