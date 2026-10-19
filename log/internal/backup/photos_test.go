package backup

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
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
	b.Targets = []Target{{"main", flaky}}

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
	// nothing is stored.
	if _, err := b.RunOnce(ctx, false); err == nil {
		t.Fatal("expected a photo backup error")
	}
	if st := b.Status().Targets[0]; !strings.Contains(st.LastError, "bucket unreachable") {
		t.Fatalf("status = %+v", st)
	}
	if keys, _ := store.List(ctx, PhotoPrefix); len(keys) != 0 {
		t.Fatalf("stored while down: %v", keys)
	}

	// Next time it works.
	flaky.fail = false
	if err := b.SyncPhotos(ctx); err != nil {
		t.Fatal(err)
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
	if _, _, err := Restore(ctx, b.Targets, filepath.Join(dest, "log.db"), filepath.Join(dest, "photos"), RestoreOptions{}, b.Log); err != nil {
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

// addPhoto stores a photo on a new piece.
func addPhoto(t *testing.T, s *db.Store, files *photos.Store) photos.Photo {
	t.Helper()
	ctx := context.Background()
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
	return p
}

func TestTargetsAreIndependent(t *testing.T) {
	b, s, a := setup(t)
	ctx := context.Background()
	photoDir := filepath.Join(b.DataDir, "photos")
	b.Photos, b.PhotoDir = s, photoDir
	p := addPhoto(t, s, &photos.Store{Dir: photoDir})
	bStore := DirStore{Root: t.TempDir()}
	flaky := &failingStore{ObjectStore: bStore, fail: true}
	b.Targets = []Target{{"a", a}, {"b", flaky}}

	// b is down: a is backed up anyway, and only b reports a problem.
	up, err := b.RunOnce(ctx, false)
	if !up || err == nil || !strings.HasPrefix(err.Error(), "b: ") {
		t.Fatalf("uploaded=%v err=%v; want an upload and an error from b only", up, err)
	}
	st := b.Status()
	if st.Targets[0].LastError != "" || st.Targets[0].LastOK.IsZero() || st.Targets[1].LastError == "" || !st.Targets[1].LastOK.IsZero() {
		t.Fatalf("status = %+v", st.Targets)
	}
	if snaps, _ := a.List(ctx, snapshotPrefix); len(snaps) != 1 {
		t.Fatalf("a's snapshots = %v", snaps)
	}

	// b comes back: it catches up, and a, already up to date, gets nothing new.
	flaky.fail = false
	if _, err := b.RunOnce(ctx, false); err != nil {
		t.Fatal(err)
	}
	if snaps, _ := a.List(ctx, snapshotPrefix); len(snaps) != 1 {
		t.Fatalf("a was uploaded to again: %v", snaps)
	}
	if snaps, _ := bStore.List(ctx, snapshotPrefix); len(snaps) != 1 {
		t.Fatalf("b's snapshots = %v", snaps)
	}
	for _, rel := range p.Paths() {
		if _, err := bStore.Get(ctx, PhotoPrefix+rel); err != nil {
			t.Fatalf("b is missing %s: %v", rel, err)
		}
	}
	if st := b.Status().Targets[1]; st.LastError != "" || st.LastOK.IsZero() {
		t.Fatalf("b's status = %+v", st)
	}

	// Removing the photo removes it everywhere.
	if err := b.DeletePhoto(ctx, p); err != nil {
		t.Fatal(err)
	}
	for _, store := range []ObjectStore{a, bStore} {
		if keys, _ := store.List(ctx, PhotoPrefix); len(keys) != 0 {
			t.Fatalf("left in %s: %v", store, keys)
		}
	}
}

func TestNewTargetCatchesUp(t *testing.T) {
	b, s, a := setup(t)
	ctx := context.Background()
	photoDir := filepath.Join(b.DataDir, "photos")
	b.Photos, b.PhotoDir = s, photoDir
	p := addPhoto(t, s, &photos.Store{Dir: photoDir})
	if _, err := b.RunOnce(ctx, false); err != nil {
		t.Fatal(err)
	}

	// A target added later gets the photos and the database, though
	// nothing has changed.
	added := DirStore{Root: t.TempDir()}
	b.Targets = append(b.Targets, Target{"added", added})
	if up, err := b.RunOnce(ctx, false); err != nil || !up {
		t.Fatalf("uploaded=%v err=%v", up, err)
	}
	if snaps, _ := added.List(ctx, snapshotPrefix); len(snaps) != 1 {
		t.Fatalf("added target's snapshots = %v", snaps)
	}
	if keys, _ := added.List(ctx, PhotoPrefix); len(keys) != len(p.Paths()) {
		t.Fatalf("added target's photos = %v", keys)
	}
	if snaps, _ := a.List(ctx, snapshotPrefix); len(snaps) != 1 {
		t.Fatalf("the first target was uploaded to again: %v", snaps)
	}
}

func TestPreMigrationBackupNeedsOneTarget(t *testing.T) {
	b, _, a := setup(t)
	ctx := context.Background()
	down := &failingStore{ObjectStore: DirStore{Root: t.TempDir()}, fail: true}
	b.Targets = []Target{{"a", a}, {"down", down}}
	if err := b.BackupBeforeMigration(ctx, 1); err != nil {
		t.Fatalf("one target got the copy, so this should succeed: %v", err)
	}
	if keys, _ := a.List(ctx, preMigrationPrefix); len(keys) != 1 {
		t.Fatalf("a's pre-migration copies = %v", keys)
	}
	b.Targets = []Target{{"down", down}}
	if err := b.BackupBeforeMigration(ctx, 1); err == nil {
		t.Fatal("no target got the copy, so this must fail")
	}
}

// failingLists fails every List.
type failingLists struct{ ObjectStore }

func (failingLists) List(context.Context, string) ([]string, error) {
	return nil, errors.New("bucket unreachable")
}

func TestRestoreUsesNewestSnapshotAndAnyTargetsPhotos(t *testing.T) {
	b, s, _ := setup(t)
	ctx := context.Background()
	older, newer := DirStore{Root: t.TempDir()}, DirStore{Root: t.TempDir()}

	// older has the first version and a photo; newer has a later version,
	// but missed the photo.
	b.Targets = []Target{{"older", older}}
	photoDir := filepath.Join(b.DataDir, "photos")
	b.Photos, b.PhotoDir = s, photoDir
	p := addPhoto(t, s, &photos.Store{Dir: photoDir})
	if _, err := b.RunOnce(ctx, false); err != nil {
		t.Fatal(err)
	}
	b.Photos = nil
	if _, err := s.CreatePieces(ctx, db.NewPieces{Count: 1, Action: model.Thrown, Date: "2026-10-04"}); err != nil {
		t.Fatal(err)
	}
	b.Targets = []Target{{"newer", newer}}
	if _, err := b.RunOnce(ctx, false); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	all := []Target{{"older", older}, {"newer", newer}}
	target, _, err := Restore(ctx, all, filepath.Join(dest, "log.db"), filepath.Join(dest, "photos"), RestoreOptions{}, b.Log)
	if err != nil || target != "newer" {
		t.Fatalf("restored from %q, %v; want the newer snapshot", target, err)
	}
	restored, err := db.Open(filepath.Join(dest, "log.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if _, err := (&db.Store{DB: restored}).GetPiece(ctx, 121); err != nil {
		t.Fatalf("the second piece should be restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "photos", filepath.FromSlash(p.Paths()[0]))); err != nil {
		t.Fatalf("the photo should come from the older target: %v", err)
	}

	// An unreachable target could hold something newer, so restore stops.
	dest2 := t.TempDir()
	down := []Target{{"older", older}, {"down", failingLists{newer}}}
	if _, _, err := Restore(ctx, down, filepath.Join(dest2, "log.db"), filepath.Join(dest2, "photos"), RestoreOptions{}, b.Log); err == nil || !strings.Contains(err.Error(), "down: bucket unreachable") {
		t.Fatalf("err = %v", err)
	}
}
