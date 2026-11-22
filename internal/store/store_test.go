// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenMigratesAndReopens(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "abyssreader.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var fk int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, %v; want 1", fk, err)
	}
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %q, %v; want wal", mode, err)
	}
	db.Close()
	// A second start finds nothing left to migrate.
	db, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
}

// Every column named user_id must reference users(id) with ON DELETE CASCADE, so that deleting
// an account deletes everything it created. This guards every later migration too.
func TestEveryUserIDColumnCascadesFromUsers(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "abyssreader.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tables, err := db.Query(`SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name != 'goose_db_version'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var n string
		if err := tables.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	tables.Close()
	checked := 0
	for _, table := range names {
		var hasUserID bool
		cols, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		for cols.Next() {
			var c string
			cols.Scan(&c)
			if c == "user_id" {
				hasUserID = true
			}
		}
		cols.Close()
		if !hasUserID {
			continue
		}
		checked++
		var refTable, onDelete string
		err = db.QueryRow(`SELECT "table", on_delete FROM pragma_foreign_key_list(?) WHERE "from" = 'user_id'`, table).Scan(&refTable, &onDelete)
		if err != nil || refTable != "users" || onDelete != "CASCADE" {
			t.Errorf("%s.user_id must reference users(id) ON DELETE CASCADE; got %q %q %v", table, refTable, onDelete, err)
		}
	}
	if checked == 0 {
		t.Fatal("no user_id column found; the sessions table should have one")
	}
}

func TestOpenExistingNeverCreates(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "abyssreader.db")
	if _, err := OpenExisting(ctx, path); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("OpenExisting on nothing: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("OpenExisting created a file")
	}
	if _, err := OpenExisting(ctx, dir); !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("OpenExisting on a folder: %v", err)
	}
	db, _ := Open(ctx, path)
	db.Close()
	db, err := OpenExisting(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
}
