// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package catalog

import (
	"database/sql"
	"testing"
)

func TestImportStoresAndUpdatesPublicationYear(t *testing.T) {
	f := newFixture(t, nil)
	ctx := t.Context()
	m := sampleManifest()
	title := m["titles"].([]obj)[0]
	files := map[string][]byte{"covers/lantern.png": pngBytes(t, 30, 45)}
	get := func() (int64, sql.NullInt64) {
		t.Helper()
		var id int64
		var year sql.NullInt64
		err := f.cat.db.QueryRowContext(ctx, `SELECT id, publication_year FROM titles WHERE ref = 'lantern-keeper'`).Scan(&id, &year)
		if err != nil {
			t.Fatal(err)
		}
		return id, year
	}
	title["year"] = 1905
	dir := folder(t, m, files)
	if r := f.importDir(dir); r.Titles.Added != 2 {
		t.Fatalf("first import: %+v", r)
	}
	id, first := get()
	if first != (sql.NullInt64{Int64: 1905, Valid: true}) {
		t.Fatalf("first publication year: %v", first)
	}
	if r := f.importDir(dir); r.Titles.Unchanged != 2 {
		t.Fatalf("identical reimport: %+v", r)
	}

	title["year"] = 1906
	if r := f.importDir(folder(t, m, files)); r.Titles.Updated != 1 {
		t.Fatalf("updated publication year: %+v", r)
	}
	if gotID, got := get(); gotID != id || got != (sql.NullInt64{Int64: 1906, Valid: true}) {
		t.Fatalf("updated title id/year: %d, %v", gotID, got)
	}

	delete(title, "year")
	if r := f.importDir(folder(t, m, files)); r.Titles.Updated != 1 {
		t.Fatalf("removed publication year: %+v", r)
	}
	if gotID, got := get(); gotID != id || got.Valid {
		t.Fatalf("title id/year after removal: %d, %v", gotID, got)
	}
}
