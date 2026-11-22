// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefuseNewerSchemaAtEveryMigrationEntry(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "data.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO goose_db_version(version_id,is_applied) VALUES (999999,1)"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err == nil || !strings.Contains(err.Error(), "999999") {
		t.Errorf("Migrate accepted newer schema or omitted its version: %v", err)
	}
	db.Close()
	for _, open := range []func(context.Context, string) (*sql.DB, error){Open, OpenExisting} {
		db, err := open(ctx, path)
		if err == nil {
			db.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "999999") {
			t.Errorf("open accepted newer schema or omitted its version: %v", err)
		}
	}
}
