// Command log runs the Union Pots work log.
//
//	log serve          run the web app
//	log backup         snapshot the database to backup storage now
//	log restore        restore the database and photos from backup storage
//	log hash-password  print a bcrypt hash for -password-hash
//
// Run `log <command> -h` for its flags. The data directory and backup flags
// can also go before the command (see storage). See README.md.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"unionpots.nyc/log/internal/backup"
	"unionpots.nyc/log/internal/db"
	"unionpots.nyc/log/internal/demo"
	"unionpots.nyc/log/internal/photos"
	"unionpots.nyc/log/internal/web"
)

const usage = `usage: log [storage flags] <command> [flags]

commands:
  serve          run the web app
  backup         snapshot the database to backup storage now
  restore        restore the database and photos from backup storage
  hash-password  print a bcrypt hash for serve -password-hash

Run "log <command> -h" for a command's flags.
`

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	st := storage{DataDir: "./data"}
	global := flag.NewFlagSet("log", flag.ExitOnError)
	global.Usage = func() {
		fmt.Fprint(os.Stderr, usage+"\nstorage flags (also accepted after the command):\n")
		global.PrintDefaults()
	}
	st.addFlags(global)
	global.Parse(os.Args[1:])
	if global.NArg() < 1 {
		global.Usage()
		os.Exit(2)
	}
	var err error
	switch cmd, args := global.Arg(0), global.Args()[1:]; cmd {
	case "serve":
		err = serve(logger, st, args)
	case "backup":
		err = backupNow(logger, st, args)
	case "restore":
		err = restore(logger, st, args)
	case "hash-password":
		err = hashPassword()
	default:
		global.Usage()
		os.Exit(2)
	}
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
}

// DefaultPassword is used when serve is given no -password-hash, and always
// in demo mode.
const DefaultPassword = "potter"

type serveOptions struct {
	storage
	Addr         string
	PasswordHash string
	MinPieceID   int64
	AllowEmptyDB bool
	Demo         bool
}

func parseServe(st storage, args []string) (serveOptions, error) {
	o := serveOptions{storage: st}
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	o.addFlags(fs)
	fs.StringVar(&o.Addr, "addr", ":8080", "listen address")
	fs.StringVar(&o.PasswordHash, "password-hash", "", `bcrypt hash of the password, from "log hash-password" (default: the password "`+DefaultPassword+`", with a warning)`)
	fs.Int64Var(&o.MinPieceID, "min-piece-id", 1, "lowest number given to a new piece automatically")
	fs.BoolVar(&o.AllowEmptyDB, "allow-empty-db", false, "start with an empty database even though backups exist")
	fs.BoolVar(&o.Demo, "demo", false, `demo mode: password "`+DefaultPassword+`" shown on the login page, sample data reset daily, no photo uploads; no backups or -password-hash allowed`)
	fs.Parse(args)
	switch {
	case fs.NArg() > 0:
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	case o.MinPieceID < 1:
		return o, errors.New("-min-piece-id must be at least 1")
	case o.Demo && o.anyBackupFlag():
		return o, errors.New("-demo can't be combined with backup flags: the demo is never backed up")
	case o.Demo && o.PasswordHash != "":
		return o, errors.New(`-demo can't be combined with -password-hash: the demo password is always "` + DefaultPassword + `"`)
	}
	if o.PasswordHash != "" {
		if _, err := bcrypt.Cost([]byte(o.PasswordHash)); err != nil {
			return o, errors.New(`-password-hash isn't a bcrypt hash (make one with "log hash-password"; quote it, it contains $)`)
		}
	}
	return o, nil
}

func serve(logger *slog.Logger, st storage, args []string) error {
	o, err := parseServe(st, args)
	if err != nil {
		return err
	}
	store, problem := o.store()
	switch {
	case o.Demo:
		logger.Info("DEMO MODE: sample data, reset daily; no backups")
	case problem != "":
		logger.Warn("BACKUPS ARE MISCONFIGURED, running without them", "problem", problem)
	case store == nil:
		logger.Warn("BACKUPS ARE DISABLED: no backup storage configured")
	}
	defaultPassword := o.PasswordHash == ""
	if defaultPassword {
		h, err := bcrypt.GenerateFromPassword([]byte(DefaultPassword), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		o.PasswordHash = string(h)
		if !o.Demo {
			logger.Warn(`USING THE DEFAULT PASSWORD "` + DefaultPassword + `": set -password-hash`)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(o.DataDir, 0o755); err != nil {
		return err
	}
	key, err := sessionKey(o.DataDir, []byte(o.PasswordHash))
	if err != nil {
		return err
	}
	_, statErr := os.Stat(o.dbPath())
	dbExists := statErr == nil
	if store != nil && !o.AllowEmptyDB {
		// Make sure backup storage works. If it doesn't, carry on with a
		// warning, unless the database is missing: that's when backups
		// matter most, and starting an empty log would make its (empty)
		// snapshots the newest ones.
		checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		has, err := backup.HasBackups(checkCtx, store)
		cancel()
		switch {
		case err != nil && !dbExists:
			return fmt.Errorf("%s does not exist and backup storage can't be checked for existing backups (%v): fix the backup flags, or pass -allow-empty-db to start an empty log", o.dbPath(), err)
		case err != nil:
			logger.Warn("backup storage isn't working; will keep retrying", "err", err)
		case has && !dbExists:
			return fmt.Errorf("%s does not exist but backups exist in %s: run `log restore`, or pass -allow-empty-db to start an empty log", o.dbPath(), store)
		}
	}

	sqlDB, err := db.Open(o.dbPath())
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	var backuper *backup.Backuper
	if store != nil {
		backuper = &backup.Backuper{DB: sqlDB, Store: store, DataDir: o.DataDir, Now: time.Now, Log: logger,
			Photos: &db.Store{DB: sqlDB}, PhotoDir: o.photoDir()}
	}
	version, err := db.Version(ctx, sqlDB)
	if err != nil {
		return err
	}
	if dbExists && version < db.LatestVersion() && backuper != nil {
		// Never change the schema without a copy of the data as it was.
		logger.Info("backing up before migrating", "from", version, "to", db.LatestVersion())
		if err := backuper.BackupBeforeMigration(ctx, version); err != nil {
			return fmt.Errorf("pre-migration backup failed, so the database was not migrated: %w", err)
		}
	}
	if err := db.Migrate(ctx, sqlDB); err != nil {
		return err
	}

	pieces := &db.Store{DB: sqlDB, MinPieceID: o.MinPieceID}
	files := &photos.Store{Dir: o.photoDir()}
	backupCtx, stopBackups := context.WithCancel(context.Background())
	defer stopBackups()
	backupsDone := make(chan struct{})
	status := func() backup.Status { return backup.Status{Problem: problem} }
	var snapshots func(context.Context) ([]backup.Snapshot, error)
	var photoBackup web.PhotoBackup
	switch {
	case backuper != nil:
		status = backuper.Status
		snapshots = backuper.Snapshots
		photoBackup = backuper
		go func() {
			defer close(backupsDone)
			backuper.Run(backupCtx, backup.Interval)
		}()
	case o.Demo:
		if err := demo.Reset(ctx, pieces, files, time.Now()); err != nil {
			return fmt.Errorf("loading the demo data: %w", err)
		}
		go func() {
			defer close(backupsDone)
			t := time.NewTicker(demo.ResetEvery)
			defer t.Stop()
			for {
				select {
				case <-backupCtx.Done():
					return
				case <-t.C:
					if err := demo.Reset(backupCtx, pieces, files, time.Now()); err != nil {
						logger.Error("resetting the demo data", "err", err)
					}
				}
			}
		}()
	default:
		close(backupsDone)
	}

	srv := &web.Server{
		Store: pieces,
		Auth: &web.Auth{PasswordHash: []byte(o.PasswordHash), Secret: key, Now: time.Now,
			Rotate: func() ([]byte, error) { return rotateSessionKey(o.DataDir, []byte(o.PasswordHash)) }},
		Backup:          status,
		Snapshots:       snapshots,
		Photos:          files,
		PhotoBackup:     photoBackup,
		DefaultPassword: defaultPassword && !o.Demo,
		Demo:            o.Demo,
		Version:         versionText(),
		Now:             time.Now,
		Log:             logger,
	}
	if o.Demo {
		srv.LoginMessage = "This is a demo. The password is “" + DefaultPassword + "”."
	}
	httpServer := &http.Server{
		Addr:              o.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute, // photo uploads on slow connections
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", o.Addr, "data", o.DataDir, "min_piece_id", o.MinPieceID, "demo", o.Demo, "version", versionText())
		errc <- httpServer.ListenAndServe()
	}()

	select {
	case err := <-errc:
		stopBackups()
		return err
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown", "err", err)
	}
	stopBackups()
	<-backupsDone
	if backuper != nil {
		finalCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := backuper.RunOnce(finalCtx, false); err != nil {
			return fmt.Errorf("final backup failed: %w", err)
		}
	}
	return nil
}

func backupNow(logger *slog.Logger, st storage, args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	st.addFlags(fs)
	fs.Parse(args)
	store, err := st.requireStore()
	if err != nil {
		return err
	}
	if _, err := os.Stat(st.dbPath()); err != nil {
		return err
	}
	sqlDB, err := db.Open(st.dbPath())
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	b := &backup.Backuper{DB: sqlDB, Store: store, DataDir: st.DataDir, Now: time.Now, Log: logger,
		Photos: &db.Store{DB: sqlDB}, PhotoDir: st.photoDir()}
	uploaded, err := b.RunOnce(context.Background(), false)
	if err != nil {
		return err
	}
	if !uploaded {
		logger.Info("no changes since the last backup")
	}
	return nil
}

func restore(logger *slog.Logger, st storage, args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	st.addFlags(fs)
	at := fs.String("at", "", "restore the newest snapshot taken on or before this date (YYYY-MM-DD)")
	key := fs.String("key", "", "restore this exact snapshot key, e.g. db/snapshots/2026-10-03T14-00-00Z.db.gz")
	fs.Parse(args)
	store, err := st.requireStore()
	if err != nil {
		return err
	}
	restored, err := backup.Restore(context.Background(), store, st.dbPath(), st.photoDir(),
		backup.RestoreOptions{Key: *key, At: *at}, logger)
	if err != nil {
		return err
	}
	logger.Info("restore complete", "snapshot", restored, "db", st.dbPath())
	return nil
}

func hashPassword() error {
	var password []byte
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "Password: ")
		p, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		password = p
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return err
		}
		password = []byte(strings.TrimRight(line, "\r\n"))
	}
	if len(password) < 8 {
		return errors.New("use at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword(password, bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	fmt.Println(string(hash))
	return nil
}
