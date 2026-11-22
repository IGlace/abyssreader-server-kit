// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"maps"
	"slices"
	"strconv"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

func TestMihonRoundTripAcrossSourceAddressChanges(t *testing.T) {
	db := exportDatabase(t)
	exportRecord(t, db, 1, 1, 1000, "chapterMark", "amber", "/amber/1", `{"read":true}`)
	exportRecord(t, db, 1, 2, 2000, "chapterMark", "hosted", "/chapters/10", `{"bookmarked":true}`)
	old := runExport(t, db, true)
	if len(old.Entries) != 2 {
		t.Fatalf("expected two original entries, got %d", len(old.Entries))
	}

	// Model a Mihon re-export: it serializes its own manga and source fields
	// but does not know AbyssReader field 9100.
	var mihon bytes.Buffer
	for _, e := range old.Entries {
		e.Extra = nil
		if err := WriteEntry(&mihon, e); err != nil {
			t.Fatal(err)
		}
	}
	for id, name := range old.SourceNames {
		if err := WriteSource(&mihon, id, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`BEGIN;
		PRAGMA defer_foreign_keys=ON;
		UPDATE sources SET website='https://moved.example' WHERE slug='amber';
		UPDATE sources SET id=42 WHERE slug='hosted';
		UPDATE source_titles SET source_id=42 WHERE source_id=3;
		COMMIT;`); err != nil {
		t.Fatal(err)
	}
	const newAddress = "https://new-kit.example"
	// This fixture uses a single connection. Lookup must also work when
	// SQLite disallows writes, including on the first export after a move.
	if _, err := db.Exec("PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	var moved bytes.Buffer
	if err := Export(t.Context(), db, 1, true, newAddress, "Lantern", &moved); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA query_only=OFF"); err != nil {
		t.Fatal(err)
	}
	newFile, err := Decode(&moved, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(newFile.SourceNames, old.SourceNames) {
		t.Errorf("moving addresses changed source identities: %v -> %v", old.SourceNames, newFile.SourceNames)
	}
	reexport, err := Decode(&mihon, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	r, err := Restore(t.Context(), db, usersync.New(db), 2, reexport, true, newAddress)
	if err != nil || r.Titles.Matched != 2 || r.Titles.MatchedByName != 0 || r.ChapterMarks.Restored != 2 {
		t.Fatalf("restore with source stubs: %+v, %v", r, err)
	}
	rows, err := db.Query("SELECT source_slug,chapter_url FROM sync_records WHERE user_id=2 AND kind='chapterMark' ORDER BY source_slug")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var marks []string
	for rows.Next() {
		var slug, url string
		if err := rows.Scan(&slug, &url); err != nil {
			t.Fatal(err)
		}
		marks = append(marks, slug+url)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(marks, []string{"amber/amber/1", "hosted/chapters/10"}) {
		t.Fatalf("restored marks %v", marks)
	}
}

func TestSourceStubMatchPriorityAndVisibility(t *testing.T) {
	db := &countedReader{DB: matchCatalog(t)}
	// Stable v2 slug-only test vectors computed independently of the encoder.
	const paperID int64 = -6110240380609021291
	var hostedID int64
	if err := db.QueryRow("SELECT source_id FROM backup_hosted_identity").Scan(&hostedID); err != nil {
		t.Fatal(err)
	}
	input := []Entry{
		{Source: paperID, URL: "/story?a=1", Title: "Ink"},
		{Source: paperID, URL: "/story?a=1", Extra: &Extra{Website: "https://ink.test"}},
		{Source: hostedID, URL: "/source-titles/3", Title: "Hidden"},
		{Source: 77, URL: "/story?a=1", Title: "The Forest"},
		{Source: paperID, URL: "/gone", Title: "Ink"},
	}
	got, err := matchEntries(t.Context(), db, input, false, "https://kit.test")
	if err != nil {
		t.Fatal(err)
	}
	want := []match{{1, "paper"}, {2, "ink"}, {}, {2, "ink"}, {2, ""}}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
	if db.reads != 3 {
		t.Fatalf("got %d reads, want 3", db.reads)
	}
}

// A negative integer already assigned to another slug represents a collision,
// regardless of whether the candidate came from an alias or a generated ID.
func TestConflictingStubDoesNotRestoreSourceScopedData(t *testing.T) {
	db := matchCatalog(t)
	const paperID = -6110240380609021291
	if _, err := db.Exec("INSERT INTO source_tachiyomi_ids VALUES (?, 2)", strconv.FormatInt(paperID, 10)); err != nil {
		t.Fatal(err)
	}
	got, err := matchEntries(t.Context(), db, []Entry{{Source: paperID, URL: "/story?a=1", Title: "Ink"}}, false, "")
	if err != nil || len(got) != 1 || got[0] != (match{2, ""}) {
		t.Fatalf("conflicting stub with name fallback: %+v, %v", got, err)
	}
}
