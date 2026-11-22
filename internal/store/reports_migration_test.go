// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package store

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

// OpenExisting is also used by admin commands against a previous-release volume.
func TestReportsMigrationPreservesPreviousDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kit.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, old, files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.UpTo(ctx, 5); err != nil {
		t.Fatal(err)
	}
	if _, err = old.Exec("INSERT INTO users(id,email,email_key,display_name,created_at,updated_at) VALUES(17,'reader@example.org','reader@example.org','Reader',1,2)"); err != nil {
		t.Fatal(err)
	}
	if _, err = old.Exec("INSERT INTO sync_accounts(user_id,seq,floor_seq) VALUES(17,103,10)"); err != nil {
		t.Fatal(err)
	}
	old.Close()
	db, err := OpenExisting(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var user string
	var seq, floor, reports int
	err = db.QueryRow(`SELECT u.display_name,s.seq,s.floor_seq,(SELECT count(*) FROM title_reports)
 FROM users u JOIN sync_accounts s ON s.user_id=u.id WHERE u.id=17`).Scan(&user, &seq, &floor, &reports)
	if err != nil || user != "Reader" || seq != 103 || floor != 10 || reports != 0 {
		t.Fatalf("after migration: %q %d %d %d, %v", user, seq, floor, reports, err)
	}
}
