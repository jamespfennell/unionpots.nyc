// Package backup snapshots the SQLite database to object storage hourly and
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
	"strings"
	"sync"
	"time"

	"unionpots.nyc/log/internal/model"
)

const (
	hourlyPrefix       = "db/hourly/"
	dailyPrefix        = "db/daily/"
	preMigrationPrefix = "db/pre-migration/" // kept forever; restored only by -key
	PhotoPrefix        = "photos/"

	// Hourly snapshots older than this are deleted, except the newest one, so
	// there is always at least one hourly snapshot. Daily ones are kept forever.
	hourlyRetention = 7 * 24 * time.Hour
	hourlyLayout    = "2006-01-02T15-04-05Z"

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
	return s.Enabled && now.Sub(s.LastOK) > 3*time.Hour
}

type state struct {
	SHA256     string    `json:"sha256"`
	Key        string    `json:"key"`
	UploadedAt time.Time `json:"uploaded_at"`
	LastDaily  string    `json:"last_daily"`
}

type Backuper struct {
	DB      *sql.DB
	Store   ObjectStore
	DataDir string
	Now     func() time.Time
	Log     *slog.Logger

	mu     sync.Mutex // serialises runs and guards status
	status Status
}

func (b *Backuper) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.status
	s.Enabled = true
	s.Target = b.Store.String()
	return s
}

// Run backs up immediately and then every interval until ctx is done.
func (b *Backuper) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := b.RunOnce(ctx, false); err != nil && ctx.Err() == nil {
			b.Log.Error("backup failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RunOnce snapshots the database and uploads it unless it is unchanged since
// the last upload (or force is set), then prunes expired hourly snapshots. It
// reports whether it uploaded. A pruning failure is logged, not returned: the
// backup itself still succeeded.
func (b *Backuper) RunOnce(ctx context.Context, force bool) (uploaded bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	defer func() {
		if err == nil {
			if perr := b.prune(ctx); perr != nil {
				b.Log.Error("pruning old backups failed", "err", perr)
			}
		}
	}()
	defer func() {
		if err != nil {
			b.status.LastError = err.Error()
			b.status.LastErrorAt = b.Now()
		} else {
			b.status.LastOK = b.Now()
			b.status.LastError = ""
		}
	}()

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
	key := hourlyPrefix + now.Format(hourlyLayout) + ".db.gz"
	if err := b.Store.Put(ctx, key, gz); err != nil {
		return false, err
	}
	if today := model.Today(now); st.LastDaily != today {
		if err := b.Store.Put(ctx, dailyPrefix+today+".db.gz", gz); err != nil {
			return false, err
		}
		st.LastDaily = today
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
// regular backup taken in the same second can't overwrite it, and is kept
// forever. It doesn't touch the change-detection state.
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
	key := fmt.Sprintf("%s%s-v%d.db.gz", preMigrationPrefix, b.Now().UTC().Format(hourlyLayout), from)
	if err := b.Store.Put(ctx, key, gz); err != nil {
		return err
	}
	b.Log.Info("pre-migration backup uploaded", "key", key, "bytes", len(gz))
	return nil
}

// prune deletes hourly snapshots older than hourlyRetention, always keeping
// the newest one. Keys it can't parse are left alone.
func (b *Backuper) prune(ctx context.Context) error {
	keys, err := b.Store.List(ctx, hourlyPrefix)
	if err != nil {
		return err
	}
	type snap struct {
		key string
		at  time.Time
	}
	var snaps []snap
	for _, k := range keys {
		ts := strings.TrimSuffix(strings.TrimPrefix(k, hourlyPrefix), ".db.gz")
		if at, err := time.Parse(hourlyLayout, ts); err == nil {
			snaps = append(snaps, snap{k, at})
		}
	}
	if len(snaps) <= 1 {
		return nil
	}
	newest := snaps[0]
	for _, s := range snaps[1:] {
		if s.at.After(newest.at) {
			newest = s
		}
	}
	cutoff := b.Now().Add(-hourlyRetention)
	for _, s := range snaps {
		if s.key == newest.key || !s.at.Before(cutoff) {
			continue
		}
		if err := b.Store.Delete(ctx, s.key); err != nil {
			return err
		}
		b.Log.Info("deleted expired backup", "key", s.key)
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
	keys, err := store.List(ctx, "db/")
	if err != nil {
		return "", err
	}
	best, bestTS := "", ""
	for _, k := range keys {
		if strings.HasPrefix(k, preMigrationPrefix) {
			continue // old schema; only restored when asked for by key
		}
		ts := strings.TrimSuffix(path.Base(k), ".db.gz")
		if opts.At != "" && ts[:min(len(ts), 10)] > opts.At {
			continue
		}
		if ts > bestTS {
			best, bestTS = k, ts
		}
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
