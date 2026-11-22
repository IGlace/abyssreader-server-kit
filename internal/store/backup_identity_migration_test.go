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

func TestHostedBackupIdentityPersistsAfterUpgrade(t *testing.T) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "kit.db")
	db, err := sql.Open("sqlite", path)
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
	if _, err = old.UpTo(ctx, 7); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO users (id,email,email_key,display_name,created_at,updated_at) VALUES(17,'r','r','Reader',1,2);
	INSERT INTO sync_accounts(user_id,seq,floor_seq) VALUES(17,103,10);
	INSERT INTO sources(id,slug,name,website,lang,hosted,created_at,updated_at) VALUES(77,'hosted','Host','','en',1,3,4)`); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	var first int64
	if err = db.QueryRow("SELECT source_id FROM backup_hosted_identity WHERE singleton=1").Scan(&first); err != nil || first >= 0 {
		t.Fatalf("upgraded identity: %d %v", first, err)
	}
	reopened, err := OpenExisting(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var second, seq, floor, source, updated int64
	if err = reopened.QueryRow(`SELECT b.source_id,a.seq,a.floor_seq,s.id,s.updated_at FROM backup_hosted_identity b,sync_accounts a,sources s WHERE a.user_id=17 AND s.slug='hosted'`).Scan(&second, &seq, &floor, &source, &updated); err != nil || first != second || seq != 103 || floor != 10 || source != 77 || updated != 4 {
		t.Fatalf("reopened state: %d, %d/%d, %d/%d, %v", second, seq, floor, source, updated, err)
	}
}
