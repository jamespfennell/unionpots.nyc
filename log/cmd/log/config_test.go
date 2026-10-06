package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupStorageConfig(t *testing.T) {
	full := storage{}
	full.Spaces.Endpoint, full.Spaces.Region, full.Spaces.Bucket, full.Spaces.KeyID, full.Spaces.SecretKey = "https://e", "nyc3", "b", "k", "s"

	if st, problem := (storage{}).store(); st != nil || problem != "" {
		t.Errorf("nothing set: backups off without a problem, got %v %q", st, problem)
	}
	if st, problem := full.store(); st == nil || problem != "" {
		t.Errorf("all set: %v %q", st, problem)
	}
	partial := full
	partial.Spaces.SecretKey, partial.Spaces.Region = "", ""
	if st, problem := partial.store(); st != nil || problem != "missing -spaces-region, -spaces-secret" {
		t.Errorf("partial: %v %q", st, problem)
	}
	both := full
	both.BackupDir = "/tmp/x"
	if _, problem := both.store(); !strings.Contains(problem, "use one") {
		t.Errorf("both: %q", problem)
	}
	if _, problem := (storage{Prefix: "demo"}).store(); !strings.Contains(problem, "no bucket") {
		t.Errorf("prefix only: %q", problem)
	}
	if st, _ := (storage{BackupDir: "/tmp/x", Prefix: "demo"}).store(); st == nil || !strings.HasSuffix(st.String(), "/demo") {
		t.Errorf("prefix applies: %v", st)
	}
}

func TestParseServe(t *testing.T) {
	const hash = "$2a$10$abcdefghijklmnopqrstuuJ4kq4ZpQ6d1mRZb3QG9I0g3G6t4m5aW"
	cases := []struct {
		args []string
		err  string
	}{
		{nil, ""},
		{[]string{"-password-hash", hash}, ""},
		{[]string{"-password-hash", "plaintext"}, "isn't a bcrypt hash"},
		{[]string{"-demo"}, ""},
		{[]string{"-demo", "-spaces-bucket", "b"}, "-demo can't be combined with backup flags"},
		{[]string{"-demo", "-backup-prefix", "demo"}, "-demo can't be combined with backup flags"},
		{[]string{"-demo", "-password-hash", hash}, "-demo can't be combined with -password-hash"},
		{[]string{"extra"}, "unexpected argument"},
		{[]string{"-public-base-url", "https://unionpots.nyc"}, ""},
		{[]string{"-public-base-url", "unionpots.nyc"}, "must start with https://"},
		{[]string{"-demo", "-public-base-url", "https://unionpots.nyc"}, "no public pages"},
	}
	for _, c := range cases {
		_, err := parseServe(storage{DataDir: "./data"}, c.args)
		if (err == nil) != (c.err == "") || err != nil && !strings.Contains(err.Error(), c.err) {
			t.Errorf("%v: err = %v, want %q", c.args, err, c.err)
		}
	}
	// Storage flags given before the command carry through.
	o, _ := parseServe(storage{DataDir: "/data", Prefix: "prod"}, []string{"-addr", ":9000"})
	if o.DataDir != "/data" || o.Prefix != "prod" || o.Addr != ":9000" {
		t.Errorf("options = %+v", o)
	}
}

func TestSessionKey(t *testing.T) {
	dir := t.TempDir()
	k1, err := sessionKey(dir, []byte("hash-a"))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, sessionSecretFile))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("secret file: %v %v", info, err)
	}
	if k2, _ := sessionKey(dir, []byte("hash-a")); !bytes.Equal(k1, k2) {
		t.Errorf("the key should survive restarts")
	}
	if k3, _ := sessionKey(dir, []byte("hash-b")); bytes.Equal(k1, k3) {
		t.Errorf("a new password should change the key (logging everyone out)")
	}
	os.Remove(filepath.Join(dir, sessionSecretFile))
	if k4, _ := sessionKey(dir, []byte("hash-a")); bytes.Equal(k1, k4) {
		t.Errorf("deleting the secret should change the key")
	}
}

func TestRotateSessionKey(t *testing.T) {
	dir := t.TempDir()
	k1, _ := sessionKey(dir, []byte("h"))
	k2, err := rotateSessionKey(dir, []byte("h"))
	if err != nil || bytes.Equal(k1, k2) {
		t.Fatalf("rotate should change the key: %v", err)
	}
	if k3, _ := sessionKey(dir, []byte("h")); !bytes.Equal(k2, k3) {
		t.Errorf("the rotated key should be the one used after a restart")
	}
}
