package db

import (
	"context"
	"errors"
	"testing"

	"unionpots.nyc/log/internal/model"
)

func TestSetEventDate(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ids := create(t, s, NewPieces{Count: 3, Action: model.Thrown, Date: "2026-09-01"})
	s.AddEvents(ctx, ids, model.Trimmed, "2026-09-08", StepDetails{})
	s.AddEvents(ctx, ids[:2], model.QueuedBisque, "2026-09-15", StepDetails{})
	s.AddEvents(ctx, ids[2:], model.QueuedBisque, "2026-09-09", StepDetails{}) // #122 queued earlier
	s.DB.Exec("UPDATE events SET approximate = 1")
	events, _ := s.PieceEvents(ctx, ids[0])
	trimmed := events[1]

	var ue *UserError
	for _, bad := range []string{"2026-08-31", "2026-09-16"} {
		if _, err := s.SetEventDate(ctx, ids[0], trimmed.ID, bad, "2026-10-01", false); !errors.As(err, &ue) {
			t.Errorf("%s is outside thrown..queued: %v", bad, err)
		}
	}
	// Move #120's trimming to Sep 10, with siblings: #121 follows; #122 can't
	// (it was queued Sep 9), so it keeps Sep 8.
	changed, err := s.SetEventDate(ctx, ids[0], trimmed.ID, "2026-09-10", "2026-10-01", true)
	if err != nil || len(changed) != 1 || changed[0] != ids[1] {
		t.Fatalf("changed = %v, %v", changed, err)
	}
	for id, want := range map[int64]string{ids[0]: "2026-09-10", ids[1]: "2026-09-10", ids[2]: "2026-09-08"} {
		ev, _ := s.PieceEvents(ctx, id)
		if ev[1].Date != want {
			t.Errorf("#%d trimmed %s, want %s", id, ev[1].Date, want)
		}
		if exact := want == "2026-09-10"; ev[1].Approximate == exact {
			t.Errorf("#%d: a date that's set is exact (approximate=%v)", id, ev[1].Approximate)
		}
	}
	// The latest step can't be after today.
	ev, _ := s.PieceEvents(ctx, ids[0])
	if _, err := s.SetEventDate(ctx, ids[0], ev[2].ID, "2026-10-02", "2026-10-01", false); !errors.As(err, &ue) {
		t.Errorf("future date: %v", err)
	}
	// Another piece's event isn't found.
	if _, err := s.SetEventDate(ctx, ids[1], trimmed.ID, "2026-09-09", "2026-10-01", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("wrong piece: %v", err)
	}
}
