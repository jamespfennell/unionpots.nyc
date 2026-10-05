package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"unionpots.nyc/log/internal/backup"
)

// storage is the configuration every command shares: where the data lives
// and where it's backed up. Its flags are accepted before the command
// (`log -data-dir … serve`, handy in a compose entrypoint so `restore` gets
// them too) and after it (`log serve -data-dir …`).
type storage struct {
	DataDir   string
	Spaces    backup.S3Config
	Prefix    string
	BackupDir string
}

func (s *storage) addFlags(fs *flag.FlagSet) {
	fs.StringVar(&s.DataDir, "data-dir", s.DataDir, "directory holding log.db and photos/")
	fs.StringVar(&s.Spaces.Endpoint, "spaces-endpoint", s.Spaces.Endpoint, "backup storage endpoint, e.g. https://nyc3.digitaloceanspaces.com")
	fs.StringVar(&s.Spaces.Region, "spaces-region", s.Spaces.Region, "backup storage region, e.g. nyc3")
	fs.StringVar(&s.Spaces.Bucket, "spaces-bucket", s.Spaces.Bucket, "backup bucket")
	fs.StringVar(&s.Spaces.KeyID, "spaces-key", s.Spaces.KeyID, "backup storage access key")
	fs.StringVar(&s.Spaces.SecretKey, "spaces-secret", s.Spaces.SecretKey, "backup storage secret key")
	fs.StringVar(&s.Prefix, "backup-prefix", s.Prefix, "folder inside the bucket for this deployment, e.g. prod")
	fs.StringVar(&s.BackupDir, "backup-dir", s.BackupDir, "back up to this local directory instead of a bucket (testing)")
}

func (s storage) dbPath() string   { return filepath.Join(s.DataDir, "log.db") }
func (s storage) photoDir() string { return filepath.Join(s.DataDir, "photos") }

// anyBackupFlag reports whether any backup setting was given.
func (s storage) anyBackupFlag() bool {
	sp := s.Spaces
	return s.BackupDir != "" || s.Prefix != "" ||
		sp.Endpoint != "" || sp.Region != "" || sp.Bucket != "" || sp.KeyID != "" || sp.SecretKey != ""
}

// store returns the backup storage, or nil if backups aren't configured.
// problem explains configuration that is incomplete or contradictory (the
// store is then nil too).
func (s storage) store() (store backup.ObjectStore, problem string) {
	sp := s.Spaces
	var missing []string
	for _, f := range []struct{ name, v string }{
		{"-spaces-endpoint", sp.Endpoint}, {"-spaces-region", sp.Region}, {"-spaces-bucket", sp.Bucket},
		{"-spaces-key", sp.KeyID}, {"-spaces-secret", sp.SecretKey},
	} {
		if f.v == "" {
			missing = append(missing, f.name)
		}
	}
	anySpaces := len(missing) < 5
	switch {
	case s.BackupDir != "" && anySpaces:
		return nil, "both -backup-dir and -spaces-* are set; use one"
	case s.BackupDir != "":
		store = backup.DirStore{Root: s.BackupDir}
	case anySpaces && len(missing) > 0:
		return nil, "missing " + strings.Join(missing, ", ")
	case anySpaces:
		store = backup.NewS3Store(sp)
	case s.Prefix != "":
		return nil, "-backup-prefix is set but no bucket is configured"
	default:
		return nil, ""
	}
	// Several deployments (e.g. the log and a demo) can share a bucket.
	return backup.Prefixed(store, s.Prefix), ""
}

// requireStore is for commands that can't do anything without backups.
func (s storage) requireStore() (backup.ObjectStore, error) {
	store, problem := s.store()
	if problem != "" {
		return nil, errors.New("backups are misconfigured: " + problem)
	}
	if store == nil {
		return nil, errors.New("no backup storage configured: set -spaces-endpoint, -spaces-region, -spaces-bucket, -spaces-key and -spaces-secret (or -backup-dir)")
	}
	return store, nil
}

const sessionSecretFile = "session-secret"

// sessionKey returns the key that signs login cookies. It comes from a
// random secret kept in the data directory (created on first start) and
// the password hash, so changing the password, or deleting the secret file,
// logs out every device.
func sessionKey(dataDir string, passwordHash []byte) ([]byte, error) {
	secret, err := os.ReadFile(filepath.Join(dataDir, sessionSecretFile))
	if errors.Is(err, os.ErrNotExist) || err == nil && len(strings.TrimSpace(string(secret))) < 32 {
		if secret, err = newSessionSecret(dataDir); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write([]byte(strings.TrimSpace(string(secret))))
	h.Write([]byte{0})
	h.Write(passwordHash)
	return h.Sum(nil), nil
}

// rotateSessionKey replaces the stored secret ("Log out everywhere") and
// returns the new key.
func rotateSessionKey(dataDir string, passwordHash []byte) ([]byte, error) {
	if _, err := newSessionSecret(dataDir); err != nil {
		return nil, err
	}
	return sessionKey(dataDir, passwordHash)
}

func newSessionSecret(dataDir string) ([]byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	secret := []byte(hex.EncodeToString(b))
	path := filepath.Join(dataDir, sessionSecretFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(secret, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("saving the session secret: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, fmt.Errorf("saving the session secret: %w", err)
	}
	return secret, nil
}
