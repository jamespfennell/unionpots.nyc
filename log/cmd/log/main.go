// Command log runs the Union Pots work log.
//
//	log serve          run the web app
//	log backup         snapshot the database to backup storage now
//	log restore        restore the database and photos from backup storage
//	log hash-password  print a bcrypt hash for LOG_PASSWORD_HASH
//
// Configuration is via environment variables; see README.md.
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
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"

	"unionpots.nyc/log/internal/backup"
	"unionpots.nyc/log/internal/db"
	"unionpots.nyc/log/internal/web"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: log serve|backup|restore|hash-password")
		os.Exit(2)
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "serve":
		err = serve(logger)
	case "backup":
		err = backupNow(logger)
	case "restore":
		err = restore(logger, args)
	case "hash-password":
		err = hashPassword()
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
}

type config struct {
	Addr          string
	DataDir       string
	PasswordHash  string
	SessionSecret string
	AllowEmptyDB  bool
	Store         backup.ObjectStore // nil when backups are off
}

func (c config) dbPath() string   { return filepath.Join(c.DataDir, "log.db") }
func (c config) photoDir() string { return filepath.Join(c.DataDir, "photos") }

func loadConfig() (config, error) {
	c := config{
		Addr:          envOr("LOG_ADDR", ":8080"),
		DataDir:       envOr("LOG_DATA_DIR", "./data"),
		PasswordHash:  os.Getenv("LOG_PASSWORD_HASH"),
		SessionSecret: os.Getenv("LOG_SESSION_SECRET"),
		AllowEmptyDB:  os.Getenv("LOG_ALLOW_EMPTY_DB") == "1",
	}
	switch {
	case os.Getenv("LOG_BACKUPS") == "off":
	case os.Getenv("LOG_BACKUP_DIR") != "":
		c.Store = backup.DirStore{Root: os.Getenv("LOG_BACKUP_DIR")}
	default:
		s3 := backup.S3Config{
			Endpoint:  os.Getenv("SPACES_ENDPOINT"),
			Region:    os.Getenv("SPACES_REGION"),
			Bucket:    os.Getenv("SPACES_BUCKET"),
			KeyID:     os.Getenv("SPACES_KEY"),
			SecretKey: os.Getenv("SPACES_SECRET"),
		}
		if s3.Endpoint == "" || s3.Region == "" || s3.Bucket == "" || s3.KeyID == "" || s3.SecretKey == "" {
			return c, errors.New("backups are not configured: set SPACES_ENDPOINT, SPACES_REGION, SPACES_BUCKET, SPACES_KEY and SPACES_SECRET, or LOG_BACKUPS=off to run without backups")
		}
		c.Store = backup.NewS3Store(s3)
	}
	return c, nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func serve(logger *slog.Logger) error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	if c.PasswordHash == "" {
		return errors.New("LOG_PASSWORD_HASH is not set (generate one with `log hash-password`)")
	}
	if len(c.SessionSecret) < 32 {
		return errors.New("LOG_SESSION_SECRET must be at least 32 characters (e.g. `openssl rand -hex 32`)")
	}
	if c.Store == nil {
		logger.Warn("BACKUPS ARE DISABLED (LOG_BACKUPS=off)")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(c.DataDir, 0o755); err != nil {
		return err
	}
	_, statErr := os.Stat(c.dbPath())
	dbExists := statErr == nil
	if !dbExists && c.Store != nil && !c.AllowEmptyDB {
		has, err := backup.HasBackups(ctx, c.Store)
		if err != nil {
			return fmt.Errorf("checking for existing backups: %w", err)
		}
		if has {
			return fmt.Errorf("%s does not exist but backups exist in %s: run `log restore`, or set LOG_ALLOW_EMPTY_DB=1 to start an empty log", c.dbPath(), c.Store)
		}
	}

	sqlDB, err := db.Open(c.dbPath())
	if err != nil {
		return err
	}
	defer sqlDB.Close()

	var backuper *backup.Backuper
	if c.Store != nil {
		backuper = &backup.Backuper{DB: sqlDB, Store: c.Store, DataDir: c.DataDir, Now: time.Now, Log: logger}
	}
	version, err := db.Version(ctx, sqlDB)
	if err != nil {
		return err
	}
	if dbExists && version < db.LatestVersion() && backuper != nil {
		logger.Info("backing up before migrating", "from", version, "to", db.LatestVersion())
		if err := backuper.BackupBeforeMigration(ctx, version); err != nil {
			return fmt.Errorf("pre-migration backup failed: %w", err)
		}
	}
	if err := db.Migrate(ctx, sqlDB); err != nil {
		return err
	}

	status := func() backup.Status { return backup.Status{} }
	backupCtx, stopBackups := context.WithCancel(context.Background())
	backupsDone := make(chan struct{})
	if backuper != nil {
		status = backuper.Status
		go func() {
			defer close(backupsDone)
			backuper.Run(backupCtx, time.Hour)
		}()
	} else {
		close(backupsDone)
	}

	srv := &web.Server{
		Store: &db.Store{DB: sqlDB},
		Auth: &web.Auth{
			PasswordHash: []byte(c.PasswordHash),
			Secret:       []byte(c.SessionSecret),
			Now:          time.Now,
		},
		Backup: status,
		Now:    time.Now,
		Log:    logger,
	}
	httpServer := &http.Server{
		Addr:              c.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute, // photo uploads on slow connections
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", c.Addr, "data", c.DataDir)
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

func backupNow(logger *slog.Logger) error {
	c, err := loadConfig()
	if err != nil {
		return err
	}
	if c.Store == nil {
		return errors.New("backups are disabled (LOG_BACKUPS=off)")
	}
	if _, err := os.Stat(c.dbPath()); err != nil {
		return err
	}
	sqlDB, err := db.Open(c.dbPath())
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	b := &backup.Backuper{DB: sqlDB, Store: c.Store, DataDir: c.DataDir, Now: time.Now, Log: logger}
	uploaded, err := b.RunOnce(context.Background(), false)
	if err != nil {
		return err
	}
	if !uploaded {
		logger.Info("no changes since the last backup")
	}
	return nil
}

func restore(logger *slog.Logger, args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	at := fs.String("at", "", "restore the newest snapshot taken on or before this date (YYYY-MM-DD)")
	key := fs.String("key", "", "restore this exact snapshot key, e.g. db/hourly/2026-10-03T14-00-00Z.db.gz")
	fs.Parse(args)
	c, err := loadConfig()
	if err != nil {
		return err
	}
	if c.Store == nil {
		return errors.New("backups are disabled (LOG_BACKUPS=off); nothing to restore from")
	}
	restored, err := backup.Restore(context.Background(), c.Store, c.dbPath(), c.photoDir(),
		backup.RestoreOptions{Key: *key, At: *at}, logger)
	if err != nil {
		return err
	}
	logger.Info("restore complete", "snapshot", restored, "db", c.dbPath())
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
