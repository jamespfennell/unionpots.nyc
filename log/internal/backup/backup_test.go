package backup

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"unionpots.nyc/log/internal/db"
	"unionpots.nyc/log/internal/model"
)

func setup(t *testing.T) (*Backuper, *db.Store, DirStore) {
	t.Helper()
	dataDir := t.TempDir()
	sqlDB, err := db.Open(filepath.Join(dataDir, "log.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.Migrate(context.Background(), sqlDB); err != nil {
		t.Fatal(err)
	}
	store := DirStore{Root: t.TempDir()}
	clock := time.Date(2026, 10, 3, 14, 0, 0, 0, time.UTC)
	b := &Backuper{
		DB: sqlDB, Store: store, DataDir: dataDir,
		Now: func() time.Time { clock = clock.Add(time.Hour); return clock },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return b, &db.Store{DB: sqlDB, MinPieceID: 120}, store
}

func TestSkipsUnchangedAndUploadsChanges(t *testing.T) {
	b, s, store := setup(t)
	ctx := context.Background()

	if up, err := b.RunOnce(ctx, false); err != nil || !up {
		t.Fatalf("first run: uploaded=%v err=%v", up, err)
	}
	if up, err := b.RunOnce(ctx, false); err != nil || up {
		t.Fatalf("unchanged run: uploaded=%v err=%v, want skip", up, err)
	}
	if _, err := s.CreatePieces(ctx, db.NewPieces{Count: 1, Action: model.Thrown, Date: "2026-10-03"}); err != nil {
		t.Fatal(err)
	}
	if up, err := b.RunOnce(ctx, false); err != nil || !up {
		t.Fatalf("changed run: uploaded=%v err=%v", up, err)
	}
	if up, err := b.RunOnce(ctx, true); err != nil || !up {
		t.Fatalf("forced run: uploaded=%v err=%v", up, err)
	}

	snaps, _ := store.List(ctx, snapshotPrefix)
	if len(snaps) != 3 {
		t.Fatalf("snapshots = %v, want 3", snaps)
	}
	if st := b.Status(); st.LastError != "" || st.LastOK.IsZero() {
		t.Fatalf("status = %+v", st)
	}
}

func TestRestoreRoundTrip(t *testing.T) {
	b, s, store := setup(t)
	ctx := context.Background()
	if _, err := s.CreatePieces(ctx, db.NewPieces{Count: 2, Name: "Bowls", Action: model.Thrown, Date: "2026-10-03"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.RunOnce(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, PhotoPrefix+"originals/ab/abc.jpg", []byte("jpeg")); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	dbPath := filepath.Join(dest, "log.db")
	key, err := Restore(ctx, store, dbPath, filepath.Join(dest, "photos"), RestoreOptions{}, b.Log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, snapshotPrefix) {
		t.Errorf("restored %s, want the newest snapshot", key)
	}

	restored, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	p, err := (&db.Store{DB: restored}).GetPiece(ctx, 121)
	if err != nil || p.State != model.PendingTrimming {
		t.Fatalf("restored piece #121: %+v, %v", p, err)
	}
	if data, err := os.ReadFile(filepath.Join(dest, "photos", "originals", "ab", "abc.jpg")); err != nil || string(data) != "jpeg" {
		t.Fatalf("restored photo: %q, %v", data, err)
	}

	// Refuses to overwrite an existing database.
	if _, err := Restore(ctx, store, dbPath, filepath.Join(dest, "photos"), RestoreOptions{}, b.Log); err == nil {
		t.Fatal("expected error restoring over existing database")
	}
}

func TestChooseSnapshot(t *testing.T) {
	store := DirStore{Root: t.TempDir()}
	ctx := context.Background()
	for _, k := range []string{
		"db/snapshots/2026-09-30T23-00-00Z.db.gz",
		"db/snapshots/2026-10-01T09-00-00Z.db.gz",
		"db/snapshots/2026-10-02T09-00-00Z.db.gz",
		"db/pre-migration/2026-10-03T09-00-00Z-v4.db.gz", // newest, but never chosen by default
		"db/hourly/2026-10-05T09-00-00Z.db.gz",           // not a current layout: ignored
	} {
		store.Put(ctx, k, []byte("x"))
	}
	cases := map[string]string{
		"":           "db/snapshots/2026-10-02T09-00-00Z.db.gz",
		"2026-10-01": "db/snapshots/2026-10-01T09-00-00Z.db.gz",
		"2026-09-30": "db/snapshots/2026-09-30T23-00-00Z.db.gz",
	}
	for at, want := range cases {
		got, err := chooseSnapshot(ctx, store, RestoreOptions{At: at})
		if err != nil || got != want {
			t.Errorf("At=%q: got %q, %v; want %q", at, got, err, want)
		}
	}
	if _, err := chooseSnapshot(ctx, store, RestoreOptions{At: "2026-01-01"}); err == nil {
		t.Errorf("expected error when nothing matches")
	}
}

func TestPruneKeepsThirtyDaysAndAlwaysOne(t *testing.T) {
	b, _, store := setup(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	b.Now = func() time.Time { return now }
	for _, k := range []string{
		"db/pre-migration/2026-10-03T10-00-00Z-v3.db.gz", // expired
		"db/snapshots/2026-10-20T10-00-00Z.db.gz",        // 31 days: expired
		"db/snapshots/2026-10-22T10-00-00Z.db.gz",        // 29 days: kept
		"db/snapshots/notes.txt",                         // not a backup: left alone
		"db/hourly/2020-01-01T00-00-00Z.db.gz",           // not a current layout: left alone
	} {
		store.Put(ctx, k, []byte("x"))
	}
	if err := b.prune(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := store.List(ctx, "db/")
	want := []string{"db/hourly/2020-01-01T00-00-00Z.db.gz", "db/snapshots/2026-10-22T10-00-00Z.db.gz", "db/snapshots/notes.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("after prune: %v\nwant %v", got, want)
	}

	// Months later, with no new backups, the newest copy is still kept.
	now = now.AddDate(1, 0, 0)
	if err := b.prune(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.List(ctx, snapshotPrefix); len(got) != 2 {
		t.Fatalf("the newest backup must never be deleted: %v", got)
	}

	// If only an old pre-migration copy is left, it is kept too.
	store2 := DirStore{Root: t.TempDir()}
	b.Store = store2
	store2.Put(ctx, "db/pre-migration/2020-01-01T00-00-00Z-v1.db.gz", []byte("x"))
	b.prune(ctx)
	if got, _ := store2.List(ctx, "db/"); len(got) != 1 {
		t.Fatalf("the only backup must never be deleted: %v", got)
	}
}

func TestSnapshotsList(t *testing.T) {
	b, _, store := setup(t)
	ctx := context.Background()
	store.Put(ctx, "db/snapshots/2026-10-04T10-00-00Z.db.gz", []byte("x"))
	store.Put(ctx, "db/snapshots/2026-10-01T08-00-00Z.db.gz", []byte("x"))
	store.Put(ctx, "db/pre-migration/2026-10-03T10-00-00Z-v4.db.gz", []byte("x"))
	store.Put(ctx, "db/daily/2026-09-01.db.gz", []byte("x"))
	store.Put(ctx, "photos/originals/ab/abc.jpg", []byte("x"))
	snaps, err := b.Snapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 3 || snaps[0].Key != "db/snapshots/2026-10-01T08-00-00Z.db.gz" || !snaps[1].PreMigration || snaps[2].At.Hour() != 10 {
		t.Fatalf("snapshots = %+v", snaps)
	}
}

func TestRunOncePrunes(t *testing.T) {
	b, _, store := setup(t)
	ctx := context.Background()
	store.Put(ctx, "db/snapshots/2020-01-01T00-00-00Z.db.gz", []byte("old"))
	if _, err := b.RunOnce(ctx, false); err != nil {
		t.Fatal(err)
	}
	keys, _ := store.List(ctx, "db/")
	if len(keys) != 1 || strings.HasPrefix(keys[0], "db/snapshots/2020") {
		t.Fatalf("old snapshot should be pruned after a run: %v", keys)
	}
}

func TestPreMigrationBackupIsSeparateAndNotRestoredByDefault(t *testing.T) {
	b, _, store := setup(t)
	ctx := context.Background()
	b.Now = func() time.Time { return time.Date(2026, 10, 4, 1, 0, 0, 0, time.UTC) }
	if err := b.BackupBeforeMigration(ctx, 1); err != nil {
		t.Fatal(err)
	}
	// A regular backup in the same second must not overwrite it.
	if _, err := b.RunOnce(ctx, false); err != nil {
		t.Fatal(err)
	}
	keys, _ := store.List(ctx, "db/")
	want := []string{
		"db/pre-migration/2026-10-04T01-00-00Z-v1.db.gz",
		"db/snapshots/2026-10-04T01-00-00Z.db.gz",
	}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("keys = %v\nwant %v", keys, want)
	}
	got, err := chooseSnapshot(ctx, store, RestoreOptions{})
	if err != nil || got != "db/snapshots/2026-10-04T01-00-00Z.db.gz" {
		t.Fatalf("default restore chose %q, %v", got, err)
	}
}

func TestPrefixedStoresStayApart(t *testing.T) {
	ctx := context.Background()
	bucket := DirStore{Root: t.TempDir()}
	prod, demo := Prefixed(bucket, "/prod/"), Prefixed(bucket, "demo")
	if Prefixed(bucket, "") != ObjectStore(bucket) {
		t.Fatalf("an empty prefix should be the bucket itself")
	}
	prod.Put(ctx, "db/snapshots/2020-01-01T00-00-00Z.db.gz", []byte("old prod"))
	prod.Put(ctx, "db/snapshots/2026-10-01T00-00-00Z.db.gz", []byte("prod"))
	demo.Put(ctx, "db/snapshots/2020-01-01T00-00-00Z.db.gz", []byte("old demo"))
	demo.Put(ctx, "db/snapshots/2026-10-02T00-00-00Z.db.gz", []byte("demo"))

	keys, _ := prod.List(ctx, "db/")
	if strings.Join(keys, ",") != "db/snapshots/2020-01-01T00-00-00Z.db.gz,db/snapshots/2026-10-01T00-00-00Z.db.gz" {
		t.Fatalf("prod sees %v", keys)
	}
	rc, err := demo.Get(ctx, "db/snapshots/2026-10-02T00-00-00Z.db.gz")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "demo" {
		t.Fatalf("demo read %q", b)
	}
	// Pruning prod's old snapshots leaves demo's alone.
	bk := &Backuper{Store: prod, Now: func() time.Time { return time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC) },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := bk.prune(ctx); err != nil {
		t.Fatal(err)
	}
	all, _ := bucket.List(ctx, "")
	if strings.Join(all, ",") != "demo/db/snapshots/2020-01-01T00-00-00Z.db.gz,demo/db/snapshots/2026-10-02T00-00-00Z.db.gz,prod/db/snapshots/2026-10-01T00-00-00Z.db.gz" {
		t.Fatalf("bucket after pruning prod: %v", all)
	}
	if got := prod.String(); !strings.HasSuffix(got, "/prod") {
		t.Errorf("String() = %q", got)
	}
}
