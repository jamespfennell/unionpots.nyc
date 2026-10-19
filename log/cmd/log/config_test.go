package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"unionpots.nyc/log/internal/backup"
)

func TestBackupStorageFlags(t *testing.T) {
	full := storage{}
	full.Spaces.Endpoint, full.Spaces.Region, full.Spaces.Bucket, full.Spaces.KeyID, full.Spaces.SecretKey = "https://e", "nyc3", "b", "k", "s"

	if ts, problem := (storage{}).targets(); ts != nil || problem != "" {
		t.Errorf("nothing set: backups off without a problem, got %v %q", ts, problem)
	}
	if ts, problem := full.targets(); len(ts) != 1 || ts[0].Name != "spaces" || problem != "" {
		t.Errorf("all set: %v %q", ts, problem)
	}
	partial := full
	partial.Spaces.SecretKey, partial.Spaces.Region = "", ""
	if ts, problem := partial.targets(); ts != nil || problem != "missing -spaces-region, -spaces-secret" {
		t.Errorf("partial: %v %q", ts, problem)
	}
	both := full
	both.BackupDir = "/tmp/x"
	if _, problem := both.targets(); !strings.Contains(problem, "use one") {
		t.Errorf("both: %q", problem)
	}
	if _, problem := (storage{Prefix: "demo"}).targets(); !strings.Contains(problem, "no bucket") {
		t.Errorf("prefix only: %q", problem)
	}
	if ts, _ := (storage{BackupDir: "/tmp/x", Prefix: "demo"}).targets(); len(ts) != 1 || !strings.HasSuffix(ts[0].Store.String(), "/demo") {
		t.Errorf("prefix applies: %v", ts)
	}
	withConfig := full
	withConfig.BackupConfig = "backups.yml"
	if _, problem := withConfig.targets(); !strings.Contains(problem, "can't be combined") {
		t.Errorf("-backup-config with -spaces-*: %q", problem)
	}
}

func TestBackupConfigFile(t *testing.T) {
	write := func(content string) string {
		p := filepath.Join(t.TempDir(), "backups.yml")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write(`
# Every target gets every backup.
targets:
  - name: spaces
    endpoint: https://nyc3.digitaloceanspaces.com
    region: nyc3
    bucket: unionpots-log
    key: DO801JVMAL2W6ENR4J38
    secret: zZ8jzTsBdN2eKF+aU2RWmNF/DG6Y2RU+G/oqsP3y/qI
    prefix: prod
  - name: local
    dir: /backups
`)
	ts, problem := storage{BackupConfig: good}.targets()
	if problem != "" || len(ts) != 2 {
		t.Fatalf("targets = %v, %q", ts, problem)
	}
	if ts[0].Name != "spaces" || ts[0].Store.String() != "s3://unionpots-log/prod" || ts[1].Name != "local" || ts[1].Store.String() != "dir:/backups" {
		t.Errorf("targets = %v %v, %v %v", ts[0].Name, ts[0].Store, ts[1].Name, ts[1].Store)
	}

	const s3 = "endpoint: https://e\n    region: r\n    bucket: b\n"
	cases := map[string]string{
		"":            "no targets",
		"targets: []": "no targets",
		"targets:\n  - name: a\n    dir: /x\nextra: 1":                        `unknown field "extra"`,
		"targets:\n  - name: a\n    dri: /x":                                  `unknown field "dri"`,
		"targets:\n  - name: a\n    name: b\n    dir: /x":                     `mapping key "name" already defined`,
		"targets:\n  - name: A b\n    dir: /x":                                "lower-case letters",
		"targets:\n  - name: a\n    dir: /x\n  - name: a\n    dir: /y":        `two targets are named "a"`,
		"targets:\n  - name: a":                                               `target "a": missing endpoint, region, bucket, key, secret`,
		"targets:\n  - name: a\n    bucket: b\n    secret: s":                 `target "a": missing endpoint, region, key`,
		"targets:\n  - name: a\n    " + s3 + "    key: null\n    secret: s":   `target "a": missing key`,
		"targets:\n  - name: a\n    dir: /x\n    bucket: b":                   "not both",
		"targets:\n  - name: a\n    " + s3 + "    key: 012345\n    secret: s": "put 012345 in quotes",
		"targets:\n  - name: a\n    " + s3 + "    key: k\n    secret: true":   "put true in quotes",
		"targets: [": "",
	}
	for content, want := range cases {
		if _, problem := (storage{BackupConfig: write(content)}).targets(); problem == "" || !strings.Contains(problem, want) {
			t.Errorf("%q: problem %q, want %q", content, problem, want)
		}
	}
	// Quoted, a number-like value is text.
	quoted := write("targets:\n  - name: a\n    " + s3 + "    key: \"012345\"\n    secret: s")
	if ts, problem := (storage{BackupConfig: quoted}).targets(); problem != "" || len(ts) != 1 {
		t.Errorf("quoted key: %v %q", ts, problem)
	}
	if _, problem := (storage{BackupConfig: "/nonexistent/backups.yml"}).targets(); !strings.Contains(problem, "reading -backup-config") {
		t.Errorf("missing file: %q", problem)
	}
}

func TestPickTargets(t *testing.T) {
	ts := []backup.Target{{Name: "spaces"}, {Name: "r2"}}
	if got, err := pickTargets(ts, ""); err != nil || len(got) != 2 {
		t.Errorf("default: %v %v", got, err)
	}
	if got, err := pickTargets(ts, "r2"); err != nil || len(got) != 1 || got[0].Name != "r2" {
		t.Errorf("by name: %v %v", got, err)
	}
	if _, err := pickTargets(ts, "s3"); err == nil || !strings.Contains(err.Error(), "have spaces, r2") {
		t.Errorf("unknown: %v", err)
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
		{[]string{"-demo", "-backup-config", "backups.yml"}, "-demo can't be combined with backup flags"},
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
