// Package backup snapshots the SQLite database to one or more object stores
// every few minutes and restores it. Photos are mirrored separately as they
// are uploaded; Restore downloads both.
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
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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

// TargetTimeout bounds one run's work on one target. Targets are worked on
// at the same time, so a slow one doesn't delay the others, but a run isn't
// finished (and the next can't start) until every target is done.
const TargetTimeout = 5 * time.Minute

const (
	snapshotPrefix     = "db/snapshots/"
	preMigrationPrefix = "db/pre-migration/" // restored only by -key (older schema)
	PhotoPrefix        = "photos/"

	keyLayout = "2006-01-02T15-04-05Z"

	tmpFile = "backup-tmp.db"
)

// Target is one place backups go. Each target is backed up, caught up,
// pruned and reported on independently, so one that is down or new doesn't
// hold up the others.
type Target struct {
	Name  string // short and file-name safe, e.g. "spaces"
	Store ObjectStore
}

// eachTarget runs f for every target at the same time, each limited to
// TargetTimeout, and returns their errors prefixed with the target's name.
func eachTarget(ctx context.Context, targets []Target, f func(ctx context.Context, t Target) error) error {
	errs := make([]error, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(ctx, TargetTimeout)
			defer cancel()
			if err := f(ctx, t); err != nil {
				errs[i] = fmt.Errorf("%s: %w", t.Name, err)
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// Status is the backup state shown in the site-wide banner.
type Status struct {
	Enabled bool
	Problem string // backups are configured but the configuration is incomplete (then not Enabled)
	Targets []TargetStatus
}

// TargetStatus is how backups to one target are going.
type TargetStatus struct {
	Name        string
	Location    string
	LastOK      time.Time // last run that left the remote copy up to date
	LastUpload  time.Time
	LastKey     string
	LastSize    int
	LastError   string
	LastErrorAt time.Time
}

// Stale reports whether backups to the target haven't succeeded recently.
func (s TargetStatus) Stale(now time.Time) bool {
	return now.Sub(s.LastOK) > time.Hour
}

type state struct {
	SHA256     string    `json:"sha256"`
	Key        string    `json:"key"`
	UploadedAt time.Time `json:"uploaded_at"`
}

type Backuper struct {
	DB      *sql.DB
	Targets []Target
	DataDir string
	Now     func() time.Time
	Log     *slog.Logger
	// Photos, if set, lists the photos whose files in PhotoDir are copied to
	// every target.
	Photos   PhotoSource
	PhotoDir string

	run      sync.Mutex // serialises runs
	mu       sync.Mutex // guards status
	status   map[string]TargetStatus
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
	s := Status{Enabled: true}
	for _, t := range b.Targets {
		ts := b.status[t.Name]
		ts.Name, ts.Location = t.Name, t.Store.String()
		s.Targets = append(s.Targets, ts)
	}
	return s
}

func (b *Backuper) updateStatus(target string, f func(*TargetStatus)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.status == nil {
		b.status = map[string]TargetStatus{}
	}
	ts := b.status[target]
	f(&ts)
	b.status[target] = ts
}

func (b *Backuper) recordError(target string, err error) {
	now := b.Now()
	b.updateStatus(target, func(ts *TargetStatus) { ts.LastError, ts.LastErrorAt = err.Error(), now })
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
			}
			goto wait
		}
	}
}

// RunOnce snapshots the database, then, for all targets at once, uploads
// any photos each is missing, uploads the snapshot unless the target already
// has this version of the database (or force is set), and prunes expired
// snapshots. It reports whether it uploaded the database anywhere. One
// target failing doesn't stop the others; the errors are returned together.
// A photo failure doesn't stop that target's database backup; a pruning
// failure is logged, not returned.
func (b *Backuper) RunOnce(ctx context.Context, force bool) (uploaded bool, err error) {
	b.run.Lock()
	defer b.run.Unlock()
	snap, snapErr := b.snapshot(ctx)
	var any atomic.Bool
	err = eachTarget(ctx, b.Targets, func(ctx context.Context, t Target) error {
		up, err := b.runTarget(ctx, t, snap, snapErr, force)
		if up {
			any.Store(true)
		}
		return err
	})
	return any.Load(), err
}

func (b *Backuper) runTarget(ctx context.Context, t Target, snap *dbSnapshot, snapErr error, force bool) (uploaded bool, err error) {
	defer func() {
		if err != nil {
			b.recordError(t.Name, err)
			return
		}
		now := b.Now()
		b.updateStatus(t.Name, func(ts *TargetStatus) { ts.LastOK, ts.LastError = now, "" })
	}()
	photoErr := b.syncPhotos(ctx, t)
	if photoErr != nil {
		photoErr = fmt.Errorf("photo backup: %w", photoErr)
	}
	if snapErr != nil {
		return false, errors.Join(snapErr, photoErr)
	}
	uploaded, err = b.backupDB(ctx, t, snap, force)
	if err == nil {
		if perr := b.prune(ctx, t.Store); perr != nil {
			b.Log.Error("pruning old backups failed", "target", t.Name, "err", perr)
		}
	}
	return uploaded, errors.Join(err, photoErr)
}

// dbSnapshot is one copy of the database, uploaded to each target that
// doesn't have it yet.
type dbSnapshot struct {
	sha256 string
	gz     []byte
}

func (b *Backuper) backupDB(ctx context.Context, t Target, snap *dbSnapshot, force bool) (bool, error) {
	st := b.loadState(t.Name)
	if !force && snap.sha256 == st.SHA256 {
		return false, nil
	}
	now := b.Now().UTC()
	key := snapshotPrefix + now.Format(keyLayout) + ".db.gz"
	if err := t.Store.Put(ctx, key, snap.gz); err != nil {
		return false, err
	}
	st.SHA256, st.Key, st.UploadedAt = snap.sha256, key, now
	if err := b.saveState(t.Name, st); err != nil {
		return false, err
	}
	b.updateStatus(t.Name, func(ts *TargetStatus) { ts.LastUpload, ts.LastKey, ts.LastSize = now, key, len(snap.gz) })
	b.Log.Info("backup uploaded", "target", t.Name, "key", key, "bytes", len(snap.gz))
	return true, nil
}

// BackupBeforeMigration uploads a snapshot of the database as it is before a
// schema migration from version from. It goes under its own prefix, so a
// regular backup taken in the same second can't overwrite it. It doesn't
// touch the change-detection state. It succeeds if at least one target got
// the copy; failures on the others are logged.
func (b *Backuper) BackupBeforeMigration(ctx context.Context, from int) error {
	b.run.Lock()
	defer b.run.Unlock()
	snap, err := b.snapshot(ctx)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("%s%s-v%d.db.gz", preMigrationPrefix, b.Now().UTC().Format(keyLayout), from)
	var ok atomic.Bool
	err = eachTarget(ctx, b.Targets, func(ctx context.Context, t Target) error {
		if err := t.Store.Put(ctx, key, snap.gz); err != nil {
			b.Log.Error("pre-migration backup failed", "target", t.Name, "err", err)
			return err
		}
		ok.Store(true)
		b.Log.Info("pre-migration backup uploaded", "target", t.Name, "key", key, "bytes", len(snap.gz))
		return nil
	})
	if !ok.Load() {
		return err
	}
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

// Stored is what one target holds (for the backups page).
type Stored struct {
	Target    string
	Snapshots []Snapshot // oldest first
	Err       error
}

// Stored lists the database copies held by each target.
func (b *Backuper) Stored(ctx context.Context) []Stored {
	out := make([]Stored, len(b.Targets))
	var wg sync.WaitGroup
	for i, t := range b.Targets {
		wg.Go(func() {
			snaps, err := listSnapshots(ctx, t.Store)
			out[i] = Stored{t.Name, snaps, err}
		})
	}
	wg.Wait()
	return out
}

// prune deletes copies older than Retention, but never the newest regular
// copy nor the newest copy of any kind: there is always at least one backup.
func (b *Backuper) prune(ctx context.Context, store ObjectStore) error {
	snaps, err := listSnapshots(ctx, store)
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
		if err := store.Delete(ctx, sn.Key); err != nil {
			return err
		}
		b.Log.Info("deleted expired backup", "store", store.String(), "key", sn.Key)
	}
	return nil
}

// snapshot returns a consistent, integrity-checked, compressed copy of the
// database.
func (b *Backuper) snapshot(ctx context.Context) (*dbSnapshot, error) {
	tmp := filepath.Join(b.DataDir, tmpFile)
	os.Remove(tmp)
	defer os.Remove(tmp)
	if _, err := b.DB.ExecContext(ctx, "VACUUM INTO ?", tmp); err != nil {
		return nil, fmt.Errorf("vacuum into: %w", err)
	}
	if err := integrityCheck(tmp); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(tmp)
	if err != nil {
		return nil, err
	}
	gz, err := gzipBytes(data)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	return &dbSnapshot{sha256: hex.EncodeToString(sum[:]), gz: gz}, nil
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

// stateFile records what was last uploaded to a target, so an unchanged
// database isn't uploaded again.
func stateFile(target string) string { return "backup-state-" + target + ".json" }

func (b *Backuper) loadState(target string) state {
	var st state
	data, err := os.ReadFile(filepath.Join(b.DataDir, stateFile(target)))
	if err == nil {
		json.Unmarshal(data, &st)
	}
	return st
}

func (b *Backuper) saveState(target string, st state) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(b.DataDir, stateFile(target)), data)
}

// HasBackups reports whether the store holds any database snapshots.
func HasBackups(ctx context.Context, store ObjectStore) (bool, error) {
	keys, err := store.List(ctx, "db/")
	return len(keys) > 0, err
}

// RestoreOptions picks which snapshot to restore. Zero value = the newest
// any target holds.
type RestoreOptions struct {
	Key string // exact key
	At  string // YYYY-MM-DD: newest snapshot taken on or before this date
}

// Restore writes the chosen snapshot to dbPath (which must not exist) and
// downloads all photos into photoDir. It looks in every target: the
// database comes from whichever holds the newest matching snapshot (the
// first listed, on a tie), and each photo from the first target that has
// it. Every target must be reachable, so that a newer snapshot can't be
// missed; to restore without one, pass only the others. It returns the
// target and key restored.
func Restore(ctx context.Context, targets []Target, dbPath, photoDir string, opts RestoreOptions, log *slog.Logger) (target, key string, err error) {
	if _, err := os.Stat(dbPath); err == nil {
		return "", "", fmt.Errorf("%s already exists; move it aside first", dbPath)
	}
	listings := make([][]string, len(targets))
	errs := make([]error, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Go(func() {
			if listings[i], errs[i] = t.Store.List(ctx, ""); errs[i] != nil {
				errs[i] = fmt.Errorf("%s: %w", t.Name, errs[i])
			}
		})
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return "", "", fmt.Errorf("listing backups (to restore without an unreachable target, choose another with -from): %w", err)
	}
	i, key, err := chooseSnapshot(listings, opts)
	if err != nil {
		return "", "", err
	}
	t := targets[i]
	log.Info("restoring database", "target", t.Name, "key", key)
	rc, err := t.Store.Get(ctx, key)
	if err != nil {
		return "", "", err
	}
	defer rc.Close()
	zr, err := gzip.NewReader(rc)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", key, err)
	}
	data, err := io.ReadAll(zr)
	if err != nil {
		return "", "", fmt.Errorf("%s: %w", key, err)
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return "", "", err
	}
	tmp := dbPath + ".restore"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", "", err
	}
	if err := integrityCheck(tmp); err != nil {
		os.Remove(tmp)
		return "", "", err
	}
	if err := os.Rename(tmp, dbPath); err != nil {
		return "", "", err
	}

	// Each photo from the first target that has it.
	from := map[string]Target{}
	var photoKeys []string
	for i, keys := range listings {
		for _, k := range keys {
			if _, seen := from[k]; strings.HasPrefix(k, PhotoPrefix) && !seen {
				from[k] = targets[i]
				photoKeys = append(photoKeys, k)
			}
		}
	}
	sort.Strings(photoKeys)
	n := 0
	for _, pk := range photoKeys {
		dst := filepath.Join(photoDir, filepath.FromSlash(strings.TrimPrefix(pk, PhotoPrefix)))
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if err := download(ctx, from[pk].Store, pk, dst); err != nil {
			return t.Name, key, fmt.Errorf("%s: %w", from[pk].Name, err)
		}
		n++
	}
	log.Info("restored photos", "downloaded", n, "total", len(photoKeys))
	return t.Name, key, nil
}

// chooseSnapshot picks the snapshot to restore from each target's keys,
// returning the target's index and the key.
func chooseSnapshot(listings [][]string, opts RestoreOptions) (int, string, error) {
	if opts.Key != "" {
		for i, keys := range listings {
			if slices.Contains(keys, opts.Key) {
				return i, opts.Key, nil
			}
		}
		return 0, "", fmt.Errorf("no backup target holds %s", opts.Key)
	}
	best, bestAt := -1, time.Time{}
	var bestKey string
	for i, keys := range listings {
		for _, k := range keys {
			sn, ok := parseSnapshot(k)
			if !ok || sn.PreMigration { // older schema; only restored when asked for by key
				continue
			}
			if opts.At != "" && sn.At.Format("2006-01-02") > opts.At {
				continue
			}
			if best < 0 || sn.At.After(bestAt) {
				best, bestAt, bestKey = i, sn.At, k
			}
		}
	}
	if best < 0 {
		return 0, "", errors.New("no matching database snapshot found")
	}
	return best, bestKey, nil
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
