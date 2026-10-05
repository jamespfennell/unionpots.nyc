// Package backup snapshots the SQLite database to object storage every few minutes and
// restores it. Photos are mirrored separately as they are uploaded; Restore
// downloads both.
package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Backup policy: every Interval the database is copied if it has changed
// (and once more when the app stops). Copies older than Retention are
// deleted, but the newest copy is never deleted, so there is always at least
// one backup however long backups have been stopped.
const (
	Interval  = 10 * time.Minute
	Retention = 30 * 24 * time.Hour
)

const (
	snapshotPrefix     = "db/snapshots/"
	preMigrationPrefix = "db/pre-migration/" // restored only by -key (older schema)
	PhotoPrefix        = "photos/"

	keyLayout = "2006-01-02T15-04-05Z"

	stateFile = "backup-state.json"
	tmpFile   = "backup-tmp.db"
)

// Status is the backup state shown in the site-wide banner.
type Status struct {
	Enabled     bool
	Target      string
	LastOK      time.Time // last run that left the remote copy up to date
	LastUpload  time.Time
	LastKey     string
	LastSize    int
	LastError   string
	LastErrorAt time.Time
}

// Stale reports whether backups haven't succeeded recently.
func (s Status) Stale(now time.Time) bool {
	return s.Enabled && now.Sub(s.LastOK) > time.Hour
}

type state struct {
	SHA256     string    `json:"sha256"`
	Key        string    `json:"key"`
	UploadedAt time.Time `json:"uploaded_at"`
}

type Backuper struct {
	DB      *sql.DB
	Store   ObjectStore
	DataDir string
	Now     func() time.Time
	Log     *slog.Logger
	// Photos, if set, lists photos to copy from PhotoDir on each run.
	Photos   PhotoSource
	PhotoDir string

	mu       sync.Mutex // serialises runs and guards status
	status   Status
	kick     chan struct{}
	kickOnce sync.Once
}

func (b *Backuper) kicks() chan struct{} {
	b.kickOnce.Do(func() { b.kick = make(chan struct{}, 1) })
	return b.kick
}

func (b *Backuper) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.status
	s.Enabled = true
	s.Target = b.Store.String()
	return s
}

// Run backs up immediately and then every interval until ctx is done. A
// Kick uploads new photos straight away.
func (b *Backuper) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := b.RunOnce(ctx, false); err != nil && ctx.Err() == nil {
			b.Log.Error("backup failed", "err", err)
		}
	wait:
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-b.kicks():
			if err := b.SyncPhotos(ctx); err != nil && ctx.Err() == nil {
				b.Log.Error("photo backup failed", "err", err)
				b.mu.Lock()
				b.status.LastError, b.status.LastErrorAt = err.Error(), b.Now()
				b.mu.Unlock()
			}
			goto wait
		}
	}
}

// RunOnce uploads any new photos, snapshots the database and uploads it
// unless it is unchanged since the last upload (or force is set), then
// prunes expired snapshots. It reports whether it uploaded the database. A
// photo failure doesn't stop the database backup but is returned; a pruning
// failure is logged, not returned.
func (b *Backuper) RunOnce(ctx context.Context, force bool) (uploaded bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	defer func() {
		if err != nil {
			b.status.LastError = err.Error()
			b.status.LastErrorAt = b.Now()
		} else {
			b.status.LastOK = b.Now()
			b.status.LastError = ""
		}
	}()
	photoErr := b.syncPhotos(ctx)
	if photoErr != nil {
		photoErr = fmt.Errorf("photo backup: %w", photoErr)
	}
	uploaded, err = b.backupDB(ctx, force)
	if err == nil {
		if perr := b.prune(ctx); perr != nil {
			b.Log.Error("pruning old backups failed", "err", perr)
		}
	}
	return uploaded, errors.Join(err, photoErr)
}

func (b *Backuper) backupDB(ctx context.Context, force bool) (bool, error) {
	snap, err := b.snapshot(ctx)
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(snap)
	hash := hex.EncodeToString(sum[:])
	st := b.loadState()
	if !force && hash == st.SHA256 {
		return false, nil
	}

	gz, err := gzipBytes(snap)
	if err != nil {
		return false, err
	}
	now := b.Now().UTC()
	key := snapshotPrefix + now.Format(keyLayout) + ".db.gz"
	if err := b.Store.Put(ctx, key, gz); err != nil {
		return false, err
	}
	st.SHA256, st.Key, st.UploadedAt = hash, key, now
	if err := b.saveState(st); err != nil {
		return false, err
	}
	b.status.LastUpload, b.status.LastKey, b.status.LastSize = now, key, len(gz)
	b.Log.Info("backup uploaded", "key", key, "bytes", len(gz))
	return true, nil
}

// BackupBeforeMigration uploads a snapshot of the database as it is before a
// schema migration from version from. It goes under its own prefix, so a
// regular backup taken in the same second can't overwrite it. It doesn't
// touch the change-detection state.
func (b *Backuper) BackupBeforeMigration(ctx context.Context, from int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	snap, err := b.snapshot(ctx)
	if err != nil {
		return err
	}
	gz, err := gzipBytes(snap)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%s%s-v%d.db.gz", preMigrationPrefix, b.Now().UTC().Format(keyLayout), from)
	if err := b.Store.Put(ctx, key, gz); err != nil {
		return err
	}
	b.Log.Info("pre-migration backup uploaded", "key", key, "bytes", len(gz))
	return nil
}

// Snapshot is a stored copy of the database.
type Snapshot struct {
	Key          string
	At           time.Time
	PreMigration bool // taken before a schema migration (older schema)
}

// parseSnapshot reads a database copy's key: db/snapshots/<time>.db.gz or
// db/pre-migration/<time>-v<version>.db.gz. Anything else isn't a backup.
func parseSnapshot(key string) (Snapshot, bool) {
	pre := strings.HasPrefix(key, preMigrationPrefix)
	if !pre && !strings.HasPrefix(key, snapshotPrefix) || !strings.HasSuffix(key, ".db.gz") {
		return Snapshot{}, false
	}
	name := strings.TrimSuffix(path.Base(key), ".db.gz")
	if pre {
		i := strings.LastIndex(name, "-v")
		if i < 0 {
			return Snapshot{}, false
		}
		name = name[:i]
	}
	at, err := time.Parse(keyLayout, name)
	if err != nil {
		return Snapshot{}, false
	}
	return Snapshot{key, at, pre}, true
}

// listSnapshots returns the stored database copies, oldest first. Objects it
// can't recognise are left out (and so never pruned).
func listSnapshots(ctx context.Context, store ObjectStore) ([]Snapshot, error) {
	keys, err := store.List(ctx, "db/")
	if err != nil {
		return nil, err
	}
	var snaps []Snapshot
	for _, k := range keys {
		if sn, ok := parseSnapshot(k); ok {
			snaps = append(snaps, sn)
		}
	}
	sort.SliceStable(snaps, func(i, j int) bool { return snaps[i].At.Before(snaps[j].At) })
	return snaps, nil
}

// Snapshots lists the stored database copies, oldest first (for the
// backups page).
func (b *Backuper) Snapshots(ctx context.Context) ([]Snapshot, error) {
	return listSnapshots(ctx, b.Store)
}

// prune deletes copies older than Retention, but never the newest regular
// copy nor the newest copy of any kind: there is always at least one backup.
func (b *Backuper) prune(ctx context.Context) error {
	snaps, err := listSnapshots(ctx, b.Store)
	if err != nil || len(snaps) == 0 {
		return err
	}
	keep := map[string]bool{snaps[len(snaps)-1].Key: true}
	for i := len(snaps) - 1; i >= 0; i-- {
		if !snaps[i].PreMigration {
			keep[snaps[i].Key] = true
			break
		}
	}
	cutoff := b.Now().Add(-Retention)
	for _, sn := range snaps {
		if keep[sn.Key] || !sn.At.Before(cutoff) {
			continue
		}
		if err := b.Store.Delete(ctx, sn.Key); err != nil {
			return err
		}
		b.Log.Info("deleted expired backup", "key", sn.Key)
	}
	return nil
}

// snapshot returns a consistent, integrity-checked copy of the database.
func (b *Backuper) snapshot(ctx context.Context) ([]byte, error) {
	tmp := filepath.Join(b.DataDir, tmpFile)
	os.Remove(tmp)
	defer os.Remove(tmp)
	if _, err := b.DB.ExecContext(ctx, "VACUUM INTO ?", tmp); err != nil {
		return nil, fmt.Errorf("vacuum into: %w", err)
	}
	if err := integrityCheck(tmp); err != nil {
		return nil, err
	}
	return os.ReadFile(tmp)
}

func integrityCheck(dbPath string) error {
	sqlDB, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	var result string
	if err := sqlDB.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return fmt.Errorf("integrity check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity check failed: %s", result)
	}
	return nil
}

func (b *Backuper) loadState() state {
	var st state
	data, err := os.ReadFile(filepath.Join(b.DataDir, stateFile))
	if err == nil {
		json.Unmarshal(data, &st)
	}
	return st
}

func (b *Backuper) saveState(st state) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(b.DataDir, stateFile), data)
}

// HasBackups reports whether the store holds any database snapshots.
func HasBackups(ctx context.Context, store ObjectStore) (bool, error) {
	keys, err := store.List(ctx, "db/")
	return len(keys) > 0, err
}

// RestoreOptions picks which snapshot to restore. Zero value = newest.
type RestoreOptions struct {
	Key string // exact key
	At  string // YYYY-MM-DD: newest snapshot taken on or before this date
}

// Restore writes the chosen snapshot to dbPath (which must not exist) and
// downloads all photos into photoDir. It returns the key restored.
func Restore(ctx context.Context, store ObjectStore, dbPath, photoDir string, opts RestoreOptions, log *slog.Logger) (string, error) {
	if _, err := os.Stat(dbPath); err == nil {
		return "", fmt.Errorf("%s already exists; move it aside first", dbPath)
	}
	key, err := chooseSnapshot(ctx, store, opts)
	if err != nil {
		return "", err
	}
	log.Info("restoring database", "key", key)
	rc, err := store.Get(ctx, key)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	zr, err := gzip.NewReader(rc)
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return "", err
	}
	tmp := dbPath + ".restore"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := integrityCheck(tmp); err != nil {
		os.Remove(tmp)
		return "", err
	}
	if err := os.Rename(tmp, dbPath); err != nil {
		return "", err
	}

	photos, err := store.List(ctx, PhotoPrefix)
	if err != nil {
		return key, err
	}
	n := 0
	for _, pk := range photos {
		dst := filepath.Join(photoDir, filepath.FromSlash(strings.TrimPrefix(pk, PhotoPrefix)))
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if err := download(ctx, store, pk, dst); err != nil {
			return key, err
		}
		n++
	}
	log.Info("restored photos", "downloaded", n, "total", len(photos))
	return key, nil
}

func chooseSnapshot(ctx context.Context, store ObjectStore, opts RestoreOptions) (string, error) {
	if opts.Key != "" {
		return opts.Key, nil
	}
	snaps, err := listSnapshots(ctx, store)
	if err != nil {
		return "", err
	}
	best := ""
	for _, sn := range snaps { // oldest first, so the last match wins
		if sn.PreMigration {
			continue // older schema; only restored when asked for by key
		}
		if opts.At != "" && sn.At.Format("2006-01-02") > opts.At {
			continue
		}
		best = sn.Key
	}
	if best == "" {
		return "", errors.New("no matching database snapshot found")
	}
	return best, nil
}

func download(ctx context.Context, store ObjectStore, key, dst string) error {
	rc, err := store.Get(ctx, key)
	if err != nil {
		return err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(dst, data)
}

func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
