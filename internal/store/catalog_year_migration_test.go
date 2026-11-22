// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package store

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestCatalogMigrationsPreserveExistingTitles(t *testing.T) {
	ctx := t.Context()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "kit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	old, err := goose.NewProvider(goose.DialectSQLite3, db, files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.UpTo(ctx, 8); err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO titles (id, ref, slug, name, name_key, status, rating, views, manifest_hash, created_at, updated_at)
        VALUES (17, 'old', 'old', 'Old Title', 'old title', 'ongoing', 7.2, 103, 'original', 0, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}

	var id, views int64
	var rating float64
	var hash string
	var year sql.NullInt64
	err = db.QueryRowContext(ctx, `SELECT id, views, rating, manifest_hash, publication_year FROM titles WHERE ref='old'`).
		Scan(&id, &views, &rating, &hash, &year)
	if err != nil || id != 17 || views != 103 || rating != 7.2 || hash != "original" || year.Valid {
		t.Fatalf("preserved title: id=%d views=%d rating=%g hash=%q year=%v err=%v", id, views, rating, hash, year, err)
	}
}
