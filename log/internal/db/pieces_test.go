package db

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"unionpots.nyc/log/internal/model"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	sqlDB, err := Open(filepath.Join(t.TempDir(), "log.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := Migrate(context.Background(), sqlDB); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec("UPDATE id_sequence SET next_piece_id = 120"); err != nil { // tests number from #120
		t.Fatal(err)
	}
	return &Store{DB: sqlDB}
}

func create(t *testing.T, s *Store, np NewPieces) []int64 {
	t.Helper()
	if np.Count == 0 {
		np.Count = 1
	}
	if np.Action == "" {
		np.Action = model.Thrown
	}
	if np.Date == "" {
		np.Date = "2026-10-01"
	}
	ids, err := s.CreatePieces(context.Background(), np)
	if err != nil {
		t.Fatal(err)
	}
	return ids
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	if err := Migrate(ctx, s.DB); err != nil {
		t.Fatal(err)
	}
	v, err := Version(ctx, s.DB)
	if err != nil || v != LatestVersion() {
		t.Fatalf("version = %d, %v; want %d", v, err, LatestVersion())
	}
}

func TestIDAllocationStartsAt120AndNeverReuses(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ids := create(t, s, NewPieces{Count: 3})
	if want := []int64{120, 121, 122}; !equal(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	if err := s.DeletePiece(ctx, 122); err != nil {
		t.Fatal(err)
	}
	if ids := create(t, s, NewPieces{}); ids[0] != 123 {
		t.Fatalf("after delete got #%d, want #123", ids[0])
	}
}

func TestStateFollowsLatestEvent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := create(t, s, NewPieces{Date: "2026-10-01"})[0]
	if p, _ := s.GetPiece(ctx, id); p.State != model.PendingTrimming {
		t.Fatalf("new thrown piece is %s, want pending trimming", p.State)
	}

	if err := s.AddEvents(ctx, []int64{id}, model.Trimmed, "2026-10-03", StepDetails{}); err != nil {
		t.Fatal(err)
	}
	// A backdated event does not become current.
	if err := s.AddEvents(ctx, []int64{id}, model.Glazed, "2026-09-01", StepDetails{}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.GetPiece(ctx, id)
	if p.State != model.Drying || p.StateDate != "2026-10-03" {
		t.Fatalf("state = %s on %s, want drying on 2026-10-03", p.State, p.StateDate)
	}

	if err := s.AddEvents(ctx, []int64{id}, model.Broken, "2026-10-04", StepDetails{}); err != nil {
		t.Fatal(err)
	}
	if p, _ = s.GetPiece(ctx, id); p.State != model.StateBroken {
		t.Fatalf("got %s, want broken", p.State)
	}

	events, _ := s.PieceEvents(ctx, id)
	last := events[len(events)-1]
	if err := s.DeleteEvent(ctx, id, last.ID); err != nil {
		t.Fatal(err)
	}
	if p, _ = s.GetPiece(ctx, id); p.State != model.Drying {
		t.Fatalf("after delete got %s, want drying", p.State)
	}
}

func TestCannotDeleteOnlyEvent(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := create(t, s, NewPieces{})[0]
	events, _ := s.PieceEvents(ctx, id)
	var ue *UserError
	if err := s.DeleteEvent(ctx, id, events[0].ID); !errors.As(err, &ue) {
		t.Fatalf("err = %v, want UserError", err)
	}
}

func TestEventValidation(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	id := create(t, s, NewPieces{})[0]
	var ue *UserError
	if err := s.AddEvents(ctx, []int64{id}, "bisqued", "2026-10-02", StepDetails{}); !errors.As(err, &ue) {
		t.Fatalf("unknown action: err = %v", err)
	}
	if err := s.AddEvents(ctx, []int64{id, 999}, model.Trimmed, "2026-10-02", StepDetails{}); !errors.As(err, &ue) {
		t.Fatalf("missing piece: err = %v", err)
	}
	// The failed bulk insert must not have partially applied.
	if p, _ := s.GetPiece(ctx, id); p.State != model.PendingTrimming {
		t.Fatalf("state = %s, bulk insert was not atomic", p.State)
	}
}

func TestBulkAdvanceAndLists(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ids := create(t, s, NewPieces{Count: 4, Name: "Bowls"})
	if err := s.AddEvents(ctx, ids[:3], model.Trimmed, "2026-10-02", StepDetails{}); err != nil {
		t.Fatal(err)
	}
	drying, _ := s.PiecesInState(ctx, model.Drying)
	pending, _ := s.PiecesInState(ctx, model.PendingTrimming)
	if len(drying) != 3 || len(pending) != 1 {
		t.Fatalf("drying = %d, pending trimming = %d; want 3, 1", len(drying), len(pending))
	}
}

func TestProjectsAndMoving(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	set := create(t, s, NewPieces{Count: 2, Name: "Planter set"})
	single := create(t, s, NewPieces{Name: "Plate"})[0]

	a, _ := s.GetPiece(ctx, set[0])
	proj, _ := s.GetProject(ctx, a.ProjectID)
	if proj.Name != "Planter set" || len(proj.Pieces) != 2 {
		t.Fatalf("project = %+v", proj)
	}
	if p, _ := s.GetPiece(ctx, single); p.ProjectName != "Plate" || p.Label() != "Plate" {
		t.Fatalf("a single piece's name should be its project's: %q", p.ProjectName)
	}

	// Move the single piece into the set; its old project disappears.
	old, _ := s.GetPiece(ctx, single)
	if err := s.MovePiece(ctx, single, set[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetProject(ctx, old.ProjectID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty project should be deleted: %v", err)
	}
	proj, _ = s.GetProject(ctx, a.ProjectID)
	if len(proj.Pieces) != 3 {
		t.Fatalf("project has %d pieces, want 3", len(proj.Pieces))
	}

	// Move it back out into a new project of its own.
	if err := s.MovePiece(ctx, single, 0); err != nil {
		t.Fatal(err)
	}
	moved, _ := s.GetPiece(ctx, single)
	if moved.ProjectID == a.ProjectID {
		t.Fatalf("piece should have its own project")
	}
}

func TestProjectDisplayName(t *testing.T) {
	p := Project{Pieces: []Piece{{ID: 120}, {ID: 121}, {ID: 122}}}
	if got := p.DisplayName(); got != "Project of #120–#122" {
		t.Errorf("got %q", got)
	}
	p.Pieces = []Piece{{ID: 120}, {ID: 125}}
	if got := p.DisplayName(); got != "Project of #120, #125" {
		t.Errorf("got %q", got)
	}
}

func equal(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestPieceLabelAndOrder(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	create(t, s, NewPieces{Count: 3, Name: "Set of 3 bowls"})      // 120-122
	create(t, s, NewPieces{Name: "Sculpture", Date: "2026-01-01"}) // 123, older event
	create(t, s, NewPieces{Count: 2})                              // 124-125, unnamed project

	pieces, err := s.PiecesInState(ctx, model.PendingTrimming)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range pieces {
		got = append(got, fmt.Sprintf("#%d %s", p.ID, p.Label()))
	}
	want := []string{
		"#120 Set of 3 bowls (1/3)", "#121 Set of 3 bowls (2/3)", "#122 Set of 3 bowls (3/3)",
		"#123 Sculpture", "#124 (1/2)", "#125 (2/2)",
	}
	if strings.Join(got, "; ") != strings.Join(want, "; ") {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestMigration2MovesPieceNamesToProjects(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "log.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	ctx := context.Background()
	if err := migrateTo(ctx, sqlDB, 1); err != nil {
		t.Fatal(err)
	}
	// Schema v1: names could be on pieces.
	for _, q := range []string{
		`INSERT INTO projects (id, name, created_at, updated_at) VALUES (1, NULL, '', ''), (2, 'Bowls', '', ''), (3, 'Mug', '', '')`,
		`INSERT INTO pieces (id, project_id, name, notes, state, created_at, updated_at) VALUES
		   (120, 1, 'Sculpture', '', 'drying', '', ''),
		   (121, 2, 'Big bowl', 'speckled', 'drying', '', ''),
		   (122, 2, NULL, '', 'drying', '', ''),
		   (123, 3, 'Mug', '', 'drying', '', '')`,
	} {
		if _, err := sqlDB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(ctx, sqlDB); err != nil {
		t.Fatal(err)
	}
	s := &Store{DB: sqlDB}
	checks := []struct {
		id          int64
		label, note string
	}{
		{120, "Sculpture", ""},                           // lone piece's name moved to its project
		{121, "Bowls (1/2)", "Name: Big bowl\nspeckled"}, // name kept in notes
		{122, "Bowls (2/2)", ""},
		{123, "Mug", ""}, // already matched its project
	}
	for _, c := range checks {
		p, err := s.GetPiece(ctx, c.id)
		if err != nil {
			t.Fatal(err)
		}
		if p.Label() != c.label || p.Notes != c.note {
			t.Errorf("#%d: label %q notes %q; want %q, %q", c.id, p.Label(), p.Notes, c.label, c.note)
		}
	}
}

func TestNumberingStartsAtOne(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "log.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err := Migrate(context.Background(), sqlDB); err != nil {
		t.Fatal(err)
	}
	s := &Store{DB: sqlDB}
	if ids := create(t, s, NewPieces{Count: 2}); !equal(ids, []int64{1, 2}) {
		t.Fatalf("ids = %v, want [1 2]", ids)
	}
	if ids := create(t, s, NewPieces{Count: 3}); !equal(ids, []int64{3, 4, 5}) {
		t.Fatalf("ids = %v, want [3 4 5]", ids)
	}
}

func TestStartedPieces(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ids := create(t, s, NewPieces{Count: 2, Action: model.Started})
	if p, _ := s.GetPiece(ctx, ids[0]); p.State != model.StateStarted {
		t.Fatalf("state = %s, want started", p.State)
	}
	// One is thrown, the other built: two ways on from started.
	if err := s.AddEvents(ctx, ids[:1], model.Thrown, "2026-10-05", StepDetails{Dims: model.Dims{H: 5}}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddEvents(ctx, ids[1:], model.Built, "2026-10-09", StepDetails{}); err != nil {
		t.Fatal(err)
	}
	a, _ := s.GetPiece(ctx, ids[0])
	b, _ := s.GetPiece(ctx, ids[1])
	if a.State != model.PendingTrimming || b.State != model.Drying {
		t.Fatalf("states = %s, %s", a.State, b.State)
	}
	if m, _ := s.GetMeasurements(ctx, ids[0]); m.Dims[model.Thrown].H != 5 {
		t.Fatalf("thrown size not recorded: %+v", m)
	}
}

func TestMigration4KeepsEvents(t *testing.T) {
	sqlDB, err := Open(filepath.Join(t.TempDir(), "log.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	ctx := context.Background()
	if err := migrateTo(ctx, sqlDB, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec("UPDATE id_sequence SET next_piece_id = 120"); err != nil { // tests number from #120
		t.Fatal(err)
	}
	s := &Store{DB: sqlDB}
	ids := create(t, s, NewPieces{Count: 2})
	if err := s.AddEvents(ctx, ids, model.Trimmed, "2026-10-02", StepDetails{}); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, sqlDB); err != nil {
		t.Fatal(err)
	}
	events, _ := s.PieceEvents(ctx, ids[1])
	if len(events) != 2 || events[1].Action != model.Trimmed {
		t.Fatalf("events after migration: %+v", events)
	}
	// Deleting a piece still deletes its events (the foreign key survived).
	if err := s.DeletePiece(ctx, ids[1]); err != nil {
		t.Fatal(err)
	}
	var n int
	sqlDB.QueryRow("SELECT COUNT(*) FROM events WHERE piece_id = ?", ids[1]).Scan(&n)
	if n != 0 {
		t.Fatalf("%d events left for a deleted piece", n)
	}
	if _, err := s.CreatePieces(ctx, NewPieces{Count: 1, Action: model.Started, Date: "2026-10-05"}); err != nil {
		t.Fatalf("started should be allowed after migration: %v", err)
	}
}
