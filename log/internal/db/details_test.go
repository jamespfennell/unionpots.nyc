package db

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"unionpots.nyc/log/internal/model"
)

func TestFormAndGlazeText(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ids := create(t, s, NewPieces{Count: 2, Form: " bowl "})
	if d, _ := s.GetDetails(ctx, 121); d.Form != "bowl" {
		t.Fatalf("form = %q", d.Form)
	}
	for _, n := range []string{"Pink", "Red", "Celadon"} {
		if _, err := s.EnsureGlaze(ctx, n); err != nil {
			t.Fatal(err)
		}
	}

	// Glaze text recorded with the glazed step, for every piece in it; known
	// glazes are recognised in it.
	if err := s.AddEvents(ctx, ids, model.Glazed, "2026-10-10", StepDetails{GlazeText: " Pink with dabs of red, dipped "}); err != nil {
		t.Fatal(err)
	}
	d, _ := s.GetDetails(ctx, 121)
	if d.GlazeText != "Pink with dabs of red, dipped" || strings.Join(d.Glazes, "|") != "Pink|Red" {
		t.Fatalf("details = %+v", d)
	}
	// Glaze text sent with another step is ignored.
	if err := s.AddEvents(ctx, ids[:1], model.Finished, "2026-10-20", StepDetails{GlazeText: "Celadon"}); err != nil {
		t.Fatal(err)
	}
	if d, _ = s.GetDetails(ctx, 120); strings.Contains(d.GlazeText, "Celadon") {
		t.Fatalf("finished step shouldn't change glazes: %+v", d)
	}

	// Glazes know which pieces mention them.
	pink, _ := s.EnsureGlaze(ctx, "pink")
	if pink.Pieces != 2 {
		t.Fatalf("Pink mentioned by %d pieces, want 2", pink.Pieces)
	}
	if ps, _ := s.GlazePieces(ctx, pink.ID); len(ps) != 2 {
		t.Fatalf("GlazePieces = %d", len(ps))
	}

	// Editing re-scans; clearing removes.
	if err := s.SetGlazeText(ctx, 120, "Celadon inside"); err != nil {
		t.Fatal(err)
	}
	if d, _ = s.GetDetails(ctx, 120); strings.Join(d.Glazes, "|") != "Celadon" {
		t.Fatalf("after edit: %+v", d)
	}
	if err := s.SetGlazeText(ctx, 120, ""); err != nil {
		t.Fatal(err)
	}
	if d, _ = s.GetDetails(ctx, 120); d.GlazeText != "" || len(d.Glazes) != 0 {
		t.Fatalf("after clearing: %+v", d)
	}
}

func TestRenameAndDeleteGlaze(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := create(t, s, NewPieces{})[0]
	typo, _ := s.EnsureGlaze(ctx, "Celdon")
	s.EnsureGlaze(ctx, "Tenmoku")
	if err := s.SetGlazeText(ctx, id, "celdon inside, Tenmoku out"); err != nil {
		t.Fatal(err)
	}
	// Fixing the typo rewrites the piece's text.
	if err := s.RenameGlaze(ctx, typo.ID, "Celadon"); err != nil {
		t.Fatal(err)
	}
	d, _ := s.GetDetails(ctx, id)
	if d.GlazeText != "Celadon inside, Tenmoku out" || strings.Join(d.Glazes, "|") != "Celadon|Tenmoku" {
		t.Fatalf("after rename: %+v", d)
	}
	// Renaming onto an existing glaze merges.
	extra, _ := s.EnsureGlaze(ctx, "Tenmoku Brown")
	if err := s.SetGlazeText(ctx, id, "Celadon, Tenmoku Brown"); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameGlaze(ctx, extra.ID, "tenmoku"); err != nil {
		t.Fatal(err)
	}
	names, _ := s.GlazeNames(ctx)
	if d, _ = s.GetDetails(ctx, id); strings.Join(names, "|") != "Celadon|Tenmoku" || strings.Join(d.Glazes, "|") != "Celadon|Tenmoku" {
		t.Fatalf("after merge: names %v, piece %+v", names, d)
	}
	// Only unmentioned glazes can be deleted.
	cel, _ := s.EnsureGlaze(ctx, "Celadon")
	var ue *UserError
	if err := s.DeleteGlaze(ctx, cel.ID); !errors.As(err, &ue) {
		t.Fatalf("deleting a used glaze: %v", err)
	}
	unused, _ := s.EnsureGlaze(ctx, "Shino")
	if err := s.DeleteGlaze(ctx, unused.ID); err != nil {
		t.Fatal(err)
	}
}

func TestMigration5GlazeText(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "log.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	ctx := context.Background()
	if err := migrateTo(ctx, sqlDB, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec("UPDATE id_sequence SET next_piece_id = 120"); err != nil { // tests number from #120
		t.Fatal(err)
	}
	s := &Store{DB: sqlDB}
	create(t, s, NewPieces{Count: 3})
	for _, q := range []string{
		`INSERT INTO metadata (piece_id, key, value, updated_at) VALUES
		   (120, 'glazes', '["Celadon","Tenmoku"]', ''), (120, 'glaze_notes', '"dipped"', ''),
		   (121, 'glazes', '["Randy''s Red"]', ''),
		   (122, 'glaze_notes', '"wax resist"', '')`,
	} {
		if _, err := sqlDB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, sqlDB); err != nil {
		t.Fatal(err)
	}
	want := map[int64][2]string{
		120: {"Celadon, Tenmoku. dipped", "Celadon|Tenmoku"},
		121: {"Randy's Red", "Randy's Red"},
		122: {"wax resist", ""},
	}
	for id, w := range want {
		d, _ := s.GetDetails(ctx, id)
		if d.GlazeText != w[0] || strings.Join(d.Glazes, "|") != w[1] {
			t.Errorf("#%d: %+v, want text %q glazes %q", id, d, w[0], w[1])
		}
	}
	if names, _ := s.GlazeNames(ctx); strings.Join(names, "|") != "Celadon|Randy's Red|Tenmoku" {
		t.Errorf("known glazes = %v", names)
	}
	var notes int
	sqlDB.QueryRow("SELECT COUNT(*) FROM metadata WHERE key = 'glaze_notes'").Scan(&notes)
	if notes != 0 {
		t.Errorf("glaze_notes should be gone")
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
