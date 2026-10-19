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
	"regexp"
	"strings"

	"github.com/goccy/go-yaml"

	"unionpots.nyc/log/internal/backup"
)

// storage is the configuration every command shares: where the data lives
// and where it's backed up. Its flags are accepted before the command
// (`log -data-dir … serve`, handy in a compose entrypoint so `restore` gets
// them too) and after it (`log serve -data-dir …`).
type storage struct {
	DataDir      string
	BackupConfig string
	BackupDir    string
	// Deprecated: a single bucket, from before -backup-config.
	Spaces backup.S3Config
	Prefix string
}

func (s *storage) addFlags(fs *flag.FlagSet) {
	fs.StringVar(&s.DataDir, "data-dir", s.DataDir, "directory holding log.db and photos/")
	fs.StringVar(&s.BackupConfig, "backup-config", s.BackupConfig, "YAML file listing the buckets to back up to (see README.md)")
	fs.StringVar(&s.BackupDir, "backup-dir", s.BackupDir, "back up to this local directory instead of a bucket (testing)")
	const old = " (deprecated: use -backup-config)"
	fs.StringVar(&s.Spaces.Endpoint, "spaces-endpoint", s.Spaces.Endpoint, "backup storage endpoint"+old)
	fs.StringVar(&s.Spaces.Region, "spaces-region", s.Spaces.Region, "backup storage region"+old)
	fs.StringVar(&s.Spaces.Bucket, "spaces-bucket", s.Spaces.Bucket, "backup bucket"+old)
	fs.StringVar(&s.Spaces.KeyID, "spaces-key", s.Spaces.KeyID, "backup storage access key"+old)
	fs.StringVar(&s.Spaces.SecretKey, "spaces-secret", s.Spaces.SecretKey, "backup storage secret key"+old)
	fs.StringVar(&s.Prefix, "backup-prefix", s.Prefix, "folder inside the bucket for this deployment"+old)
}

func (s storage) dbPath() string   { return filepath.Join(s.DataDir, "log.db") }
func (s storage) photoDir() string { return filepath.Join(s.DataDir, "photos") }

// anyBackupFlag reports whether any backup setting was given.
func (s storage) anyBackupFlag() bool {
	sp := s.Spaces
	return s.BackupConfig != "" || s.BackupDir != "" || s.Prefix != "" ||
		sp.Endpoint != "" || sp.Region != "" || sp.Bucket != "" || sp.KeyID != "" || sp.SecretKey != ""
}

// targets returns where backups go, or none if backups aren't configured.
// problem explains configuration that is incomplete or contradictory (there
// are then no targets either).
func (s storage) targets() (targets []backup.Target, problem string) {
	if s.BackupConfig != "" {
		if s.BackupDir != "" || s.Prefix != "" || s.Spaces != (backup.S3Config{}) {
			return nil, "-backup-config can't be combined with -backup-dir, -backup-prefix or -spaces-*"
		}
		targets, err := loadBackupConfig(s.BackupConfig)
		if err != nil {
			return nil, err.Error()
		}
		return targets, ""
	}
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
	var t backup.Target
	switch {
	case s.BackupDir != "" && anySpaces:
		return nil, "both -backup-dir and -spaces-* are set; use one"
	case s.BackupDir != "":
		t = backup.Target{Name: "local", Store: backup.DirStore{Root: s.BackupDir}}
	case anySpaces && len(missing) > 0:
		return nil, "missing " + strings.Join(missing, ", ")
	case anySpaces:
		t = backup.Target{Name: "spaces", Store: backup.NewS3Store(sp)}
	case s.Prefix != "":
		return nil, "-backup-prefix is set but no bucket is configured"
	default:
		return nil, ""
	}
	// Several deployments (e.g. the log and a demo) can share a bucket.
	t.Store = backup.Prefixed(t.Store, s.Prefix)
	return []backup.Target{t}, ""
}

// requireTargets is for commands that can't do anything without backups.
func (s storage) requireTargets() ([]backup.Target, error) {
	targets, problem := s.targets()
	if problem != "" {
		return nil, errors.New("backups are misconfigured: " + problem)
	}
	if len(targets) == 0 {
		return nil, errors.New("no backup storage configured: set -backup-config (or -backup-dir)")
	}
	return targets, nil
}

// backupConfig is the -backup-config file: every place backups go. Each
// target is either a bucket (endpoint, region, bucket, key and secret) or a
// local directory (dir).
type backupConfig struct {
	Targets []targetConfig `yaml:"targets"`
}

type targetConfig struct {
	Name     text `yaml:"name"`
	Endpoint text `yaml:"endpoint"`
	Region   text `yaml:"region"`
	Bucket   text `yaml:"bucket"`
	Key      text `yaml:"key"`
	Secret   text `yaml:"secret"`
	Dir      text `yaml:"dir"`
	// Prefix is the folder inside the bucket for this deployment, so
	// several deployments can share a bucket.
	Prefix text `yaml:"prefix"`
}

// text is a config value that must be a YAML string. Unquoted, YAML reads
// some values as other types (012345 as the number 5349, true as a bool),
// which for a key or secret would silently be the wrong value.
type text string

func (t *text) UnmarshalYAML(raw []byte) error {
	var v any
	if err := yaml.Unmarshal(raw, &v); err != nil {
		return err
	}
	switch v := v.(type) {
	case string:
		*t = text(v)
	case nil:
		*t = ""
	default:
		return fmt.Errorf("put %s in quotes: unquoted, YAML reads it as %v, not text", strings.TrimSpace(string(raw)), v)
	}
	return nil
}

var targetName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func loadBackupConfig(path string) ([]backup.Target, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading -backup-config: %w", err)
	}
	var c backupConfig
	if err := yaml.UnmarshalWithOptions(data, &c, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("%s: %s", path, yaml.FormatError(err, false, false))
	}
	if len(c.Targets) == 0 {
		return nil, fmt.Errorf("%s: no targets", path)
	}
	var targets []backup.Target
	seen := map[string]bool{}
	for i, tc := range c.Targets {
		if !targetName.MatchString(string(tc.Name)) {
			return nil, fmt.Errorf("%s: target %d: name %q must be lower-case letters, digits and dashes", path, i+1, tc.Name)
		}
		if seen[string(tc.Name)] {
			return nil, fmt.Errorf("%s: two targets are named %q", path, tc.Name)
		}
		seen[string(tc.Name)] = true
		var missing []string
		for _, f := range []struct {
			name string
			v    text
		}{
			{"endpoint", tc.Endpoint}, {"region", tc.Region}, {"bucket", tc.Bucket}, {"key", tc.Key}, {"secret", tc.Secret},
		} {
			if f.v == "" {
				missing = append(missing, f.name)
			}
		}
		var store backup.ObjectStore
		switch {
		case tc.Dir != "" && len(missing) < 5:
			return nil, fmt.Errorf("%s: target %q: set either dir or a bucket, not both", path, tc.Name)
		case tc.Dir != "":
			store = backup.DirStore{Root: string(tc.Dir)}
		case len(missing) > 0:
			return nil, fmt.Errorf("%s: target %q: missing %s", path, tc.Name, strings.Join(missing, ", "))
		default:
			store = backup.NewS3Store(backup.S3Config{Endpoint: string(tc.Endpoint), Region: string(tc.Region),
				Bucket: string(tc.Bucket), KeyID: string(tc.Key), SecretKey: string(tc.Secret)})
		}
		targets = append(targets, backup.Target{Name: string(tc.Name), Store: backup.Prefixed(store, string(tc.Prefix))})
	}
	return targets, nil
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
