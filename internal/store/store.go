// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package store opens the simple setup's database (SQLite in the /data volume) and runs the
// embedded, forward-only migrations at start-up.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // the pure-Go SQLite driver ("sqlite"); no cgo
)

//go:embed migrations/*.sql
var migrations embed.FS

// Open opens (creating it if needed) the SQLite database at path and brings its schema up to
// date. Every connection runs in WAL mode with foreign keys on, which account deletion relies
// on: every table of an account's data cascades from users(id).
func Open(ctx context.Context, path string) (*sql.DB, error) {
	return open(ctx, path, "rwc")
}

// ErrNoDatabase means OpenExisting found no database file.
var ErrNoDatabase = errors.New("no database")

// OpenExisting opens the database at path only if it exists, for the admin commands: a wrong
// ABYSS_DATA_DIR must fail, not create an empty database somewhere else.
func OpenExisting(ctx context.Context, path string) (*sql.DB, error) {
	if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%w at %s", ErrNoDatabase, path)
	}
	return open(ctx, path, "rw")
}

func open(ctx context.Context, path, mode string) (*sql.DB, error) {
	q := url.Values{}
	q.Set("mode", mode)
	for _, p := range []string{"foreign_keys(1)", "journal_mode(WAL)", "busy_timeout(5000)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	if err := Migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// MaxVersion is the newest migration shipped in this binary.
func MaxVersion() int64 {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil || len(names) == 0 {
		panic("missing embedded migrations")
	}
	var newest int64
	for _, name := range names {
		stem, _, _ := strings.Cut(strings.TrimPrefix(name, "migrations/"), "_")
		version, err := strconv.ParseInt(stem, 10, 64)
		if err != nil {
			panic("invalid embedded migration name: " + name)
		}
		newest = max(newest, version)
	}
	return newest
}

// AppliedVersion reads the newest applied forward migration without initializing Goose
// or writing anything to the database. An uninitialized database has version zero.
func AppliedVersion(ctx context.Context, db *sql.DB) (int64, error) {
	var exists bool
	if err := db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE type='table' AND name='goose_db_version')").Scan(&exists); err != nil || !exists {
		return 0, err
	}
	var version int64
	err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(version_id),0) FROM goose_db_version WHERE is_applied=1").Scan(&version)
	return version, err
}

// Migrate applies every migration that has not run yet. There are no down migrations: a
// rollback restores the backup taken before an upgrade.
func Migrate(ctx context.Context, db *sql.DB) error {
	version, err := AppliedVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("read database schema: %w", err)
	}
	if supported := MaxVersion(); version > supported {
		return fmt.Errorf("database schema %d is newer than the supported version %d; restore a compatible backup before starting this image", version, supported)
	}
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, db, sub)
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	return nil
}

// AccountRowCount counts every row that belongs to the account userID: its users row and the
// rows of every table with a user_id column. After an account deletion it is 0.
func AccountRowCount(ctx context.Context, db *sql.DB, userID int64) (int, error) {
	var total int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE id = ?`, userID).Scan(&total); err != nil {
		return 0, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT m.name FROM sqlite_schema m JOIN pragma_table_info(m.name) c
		WHERE m.type = 'table' AND c.name = 'user_id'`)
	if err != nil {
		return 0, err
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return 0, err
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, table := range tables {
		var n int
		// The table name comes from sqlite_schema, not from input; quoted as an identifier.
		q := `SELECT count(*) FROM "` + strings.ReplaceAll(table, `"`, `""`) + `" WHERE user_id = ?`
		if err := db.QueryRowContext(ctx, q, userID).Scan(&n); err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// IsBusy reports whether err is SQLite's "database is busy": a write waited longer than the
// busy timeout for the one writer (the HTTP layer answers 503 with Retry-After).
func IsBusy(err error) bool {
	var e interface{ Code() int }
	return errors.As(err, &e) && e.Code()&0xff == 5 // SQLITE_BUSY and its extended codes
}
