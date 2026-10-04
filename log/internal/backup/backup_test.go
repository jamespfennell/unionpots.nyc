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

	hourly, _ := store.List(ctx, hourlyPrefix)
	daily, _ := store.List(ctx, dailyPrefix)
	if len(hourly) != 3 || len(daily) != 1 {
		t.Fatalf("hourly=%v daily=%v", hourly, daily)
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
	if !strings.HasPrefix(key, hourlyPrefix) {
		t.Errorf("restored %s, want newest hourly snapshot", key)
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
		"db/daily/2026-09-30.db.gz",
		"db/hourly/2026-10-01T09-00-00Z.db.gz",
		"db/hourly/2026-10-02T09-00-00Z.db.gz",
	} {
		store.Put(ctx, k, []byte("x"))
	}
	cases := map[string]string{
		"":           "db/hourly/2026-10-02T09-00-00Z.db.gz",
		"2026-10-01": "db/hourly/2026-10-01T09-00-00Z.db.gz",
		"2026-09-30": "db/daily/2026-09-30.db.gz",
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

func TestPruneKeepsRecentAndNewest(t *testing.T) {
	b, _, store := setup(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	b.Now = func() time.Time { return now }
	for _, k := range []string{
		"db/hourly/2026-10-01T10-00-00Z.db.gz", // expired
		"db/hourly/2026-10-12T10-00-00Z.db.gz", // expired (8 days)
		"db/hourly/2026-10-14T10-00-00Z.db.gz", // kept (6 days)
		"db/hourly/notes.txt",                  // unparseable: left alone
		"db/daily/2026-10-01.db.gz",            // daily: kept forever
	} {
		store.Put(ctx, k, []byte("x"))
	}
	if err := b.prune(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := store.List(ctx, "db/")
	want := []string{"db/daily/2026-10-01.db.gz", "db/hourly/2026-10-14T10-00-00Z.db.gz", "db/hourly/notes.txt"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("after prune: %v\nwant %v", got, want)
	}

	// If backups stopped long ago, the newest hourly snapshot survives.
	now = now.AddDate(0, 3, 0)
	if err := b.prune(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.List(ctx, hourlyPrefix); len(got) != 2 || got[0] != "db/hourly/2026-10-14T10-00-00Z.db.gz" {
		t.Fatalf("newest hourly snapshot should be kept: %v", got)
	}
}

func TestRunOncePrunes(t *testing.T) {
	b, _, store := setup(t)
	ctx := context.Background()
	store.Put(ctx, "db/hourly/2020-01-01T00-00-00Z.db.gz", []byte("old"))
	if _, err := b.RunOnce(ctx, false); err != nil {
		t.Fatal(err)
	}
	keys, _ := store.List(ctx, hourlyPrefix)
	if len(keys) != 1 || strings.HasPrefix(keys[0], "db/hourly/2020") {
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
		"db/daily/2026-10-03.db.gz",
		"db/hourly/2026-10-04T01-00-00Z.db.gz",
		"db/pre-migration/2026-10-04T01-00-00Z-v1.db.gz",
	}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("keys = %v\nwant %v", keys, want)
	}
	got, err := chooseSnapshot(ctx, store, RestoreOptions{})
	if err != nil || got != "db/hourly/2026-10-04T01-00-00Z.db.gz" {
		t.Fatalf("default restore chose %q, %v", got, err)
	}
}
