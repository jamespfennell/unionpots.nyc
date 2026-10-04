// Package db opens the SQLite database, applies migrations and implements
// the queries used by the app.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type migration struct {
	version int
	name    string
	sql     string
}

func migrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries {
		prefix, _, ok := strings.Cut(e.Name(), "_")
		v, err := strconv.Atoi(prefix)
		if !ok || err != nil {
			return nil, fmt.Errorf("migration %s: name must start with a number", e.Name())
		}
		b, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{v, e.Name(), string(b)})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("migrations must be numbered 1..n without gaps; got %s", m.name)
		}
	}
	return ms, nil
}

// LatestVersion is the schema version after all migrations are applied.
func LatestVersion() int {
	ms, err := migrations()
	if err != nil {
		panic(err)
	}
	return len(ms)
}

// Open opens (creating if needed) the database at path. It does not migrate.
func Open(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_txlock=immediate"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One user, small data: a single connection serialises writes and avoids
	// SQLITE_BUSY entirely. Code must never use the *sql.DB while holding a tx.
	sqlDB.SetMaxOpenConns(1)
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return sqlDB, nil
}

// Version returns the current schema version (PRAGMA user_version).
func Version(ctx context.Context, sqlDB *sql.DB) (int, error) {
	var v int
	err := sqlDB.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

// Migrate applies all pending migrations, each in its own transaction.
func Migrate(ctx context.Context, sqlDB *sql.DB) error {
	ms, err := migrations()
	if err != nil {
		return err
	}
	current, err := Version(ctx, sqlDB)
	if err != nil {
		return err
	}
	if current > len(ms) {
		return fmt.Errorf("database schema version %d is newer than this binary (%d)", current, len(ms))
	}
	for _, m := range ms[current:] {
		tx, err := sqlDB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", m.version)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// now is a sortable UTC timestamp with microseconds, so "recently changed"
// ordering is exact.
func now() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000000Z") }
