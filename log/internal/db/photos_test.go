package db

import (
	"context"
	"errors"
	"testing"
)

func TestPhotos(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	ids := create(t, s, NewPieces{Count: 2})
	a, err := s.AddPhoto(ctx, ids[0], "aaa", ".jpg", 1000, 2000)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.AddPhoto(ctx, ids[0], "bbb", ".png", 10, 10)
	if again, err := s.AddPhoto(ctx, ids[0], "aaa", ".jpg", 1000, 2000); err != nil || again.ID != a.ID {
		t.Fatalf("re-adding the same photo should be a no-op: %+v, %v", again, err)
	}
	var ue *UserError
	if _, err := s.AddPhoto(ctx, ids[1], "aaa", ".jpg", 1000, 2000); !errors.As(err, &ue) {
		t.Fatalf("a photo can belong to only one piece: %v", err)
	}
	if _, err := s.AddPhoto(ctx, 999, "ccc", ".jpg", 1, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing piece: %v", err)
	}
	ps, _ := s.PiecePhotos(ctx, ids[0])
	if len(ps) != 2 || ps[0].ID != a.ID || ps[1].ID != b.ID {
		t.Fatalf("order added: %+v", ps)
	}

	pending, _ := s.PhotosNotBackedUp(ctx)
	if len(pending) != 2 {
		t.Fatalf("pending = %d", len(pending))
	}
	s.MarkPhotoBackedUp(ctx, a.ID)
	if pending, _ = s.PhotosNotBackedUp(ctx); len(pending) != 1 || pending[0].ID != b.ID {
		t.Fatalf("after backup: %+v", pending)
	}

	if _, err := s.DeletePhoto(ctx, ids[1], a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting via the wrong piece: %v", err)
	}
	if got, err := s.DeletePhoto(ctx, ids[0], a.ID); err != nil || got.SHA256 != "aaa" {
		t.Fatalf("delete: %+v, %v", got, err)
	}
	if ps, _ = s.PiecePhotos(ctx, ids[0]); len(ps) != 1 {
		t.Fatalf("after delete: %+v", ps)
	}
}
