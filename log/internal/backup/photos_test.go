package backup

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"unionpots.nyc/log/internal/db"
	"unionpots.nyc/log/internal/model"
	"unionpots.nyc/log/internal/photos"
)

// failingStore fails every Put while fail is set.
type failingStore struct {
	ObjectStore
	fail bool
}

func (f *failingStore) Put(ctx context.Context, key string, data []byte) error {
	if f.fail {
		return errors.New("bucket unreachable")
	}
	return f.ObjectStore.Put(ctx, key, data)
}

func TestPhotosBackedUpRetriedDeletedAndRestored(t *testing.T) {
	b, s, store := setup(t)
	ctx := context.Background()
	photoDir := filepath.Join(b.DataDir, "photos")
	files := &photos.Store{Dir: photoDir}
	b.Photos, b.PhotoDir = s, photoDir
	flaky := &failingStore{ObjectStore: store, fail: true}
	b.Store = flaky

	ids, err := s.CreatePieces(ctx, db.NewPieces{Count: 1, Action: model.Thrown, Date: "2026-10-03"})
	if err != nil {
		t.Fatal(err)
	}
	var img bytes.Buffer
	jpeg.Encode(&img, image.NewRGBA(image.Rect(0, 0, 30, 20)), nil)
	p, err := files.Ingest(img.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddPhoto(ctx, ids[0], p.SHA256, p.Ext, p.Width, p.Height); err != nil {
		t.Fatal(err)
	}

	// The bucket is down: the run reports it (the banner goes stale) and
	// the photo stays pending.
	if _, err := b.RunOnce(ctx, false); err == nil {
		t.Fatal("expected a photo backup error")
	}
	if pending, _ := s.PhotosNotBackedUp(ctx); len(pending) != 1 {
		t.Fatalf("pending = %d", len(pending))
	}

	// Next time it works.
	flaky.fail = false
	if err := b.SyncPhotos(ctx); err != nil {
		t.Fatal(err)
	}
	if pending, _ := s.PhotosNotBackedUp(ctx); len(pending) != 0 {
		t.Fatalf("still pending: %d", len(pending))
	}
	for _, rel := range p.Paths() {
		if _, err := store.Get(ctx, PhotoPrefix+rel); err != nil {
			t.Fatalf("%s not uploaded: %v", rel, err)
		}
	}
	if _, err := b.RunOnce(ctx, false); err != nil {
		t.Fatal(err)
	}

	// A restore brings the photo back.
	dest := t.TempDir()
	if _, err := Restore(ctx, store, filepath.Join(dest, "log.db"), filepath.Join(dest, "photos"), RestoreOptions{}, b.Log); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dest, "photos", filepath.FromSlash(p.DerivedPath(photos.ThumbSize)))); err != nil {
		t.Fatalf("thumbnail not restored: %v", err)
	}

	// Removing it deletes the stored copies.
	if err := b.DeletePhoto(ctx, p); err != nil {
		t.Fatal(err)
	}
	if keys, _ := store.List(ctx, PhotoPrefix); len(keys) != 0 {
		t.Fatalf("left in storage: %v", keys)
	}
}
