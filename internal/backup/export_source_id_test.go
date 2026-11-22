// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"maps"
	"testing"
)

func TestExportUnmappedSourcesHaveStableMihonIdentities(t *testing.T) {
	db := exportDatabase(t)
	// Zero is Mihon's built-in local library. Out-of-range decimal IDs also
	// need a stub. The third source has a usable mapped Mihon ID.
	if _, err := db.Exec(`INSERT INTO source_tachiyomi_ids VALUES ('0',2),('9999999999999999999',2)`); err != nil {
		t.Fatal(err)
	}
	exportRecord(t, db, 1, 1, 10, "chapterMark", "moon", "/c", `{"read":true}`)
	exportRecord(t, db, 1, 1, 20, "chapterMark", "amber", "/c", `{"read":true}`)
	exportRecord(t, db, 1, 2, 30, "planning", "", "", nil)
	// Stable vectors allow an exporter in another language to emit the
	// same identity. The signed values are ordinary protobuf int64s.
	var hostedID int64
	if err := db.QueryRow("SELECT source_id FROM backup_hosted_identity").Scan(&hostedID); err != nil || hostedID >= 0 {
		t.Fatalf("stored hosted identity: %d, %v", hostedID, err)
	}
	wantIDs := map[string]int64{
		"https://moon.example":  123,
		"https://amber.example": -6195919353325148376,
		"https://kit.example":   hostedID,
	}
	wantNames := map[int64]string{
		123: "Paper Moon", -6195919353325148376: "Amber", hostedID: "Lantern",
	}
	check := func() {
		t.Helper()
		f := runExport(t, db, true)
		ids := map[string]int64{}
		for _, e := range f.Entries {
			ids[e.Extra.Website] = e.Source
		}
		if !maps.Equal(ids, wantIDs) || !maps.Equal(f.SourceNames, wantNames) {
			t.Errorf("export identities: %v, names: %v", ids, f.SourceNames)
		}
	}
	check()
	// The same source can have another database row ID after a fresh import.
	if _, err := db.Exec(`BEGIN;
		PRAGMA defer_foreign_keys=ON;
		UPDATE sources SET id=42 WHERE id=2;
		UPDATE source_titles SET source_id=42 WHERE source_id=2;
		UPDATE source_tachiyomi_ids SET source_id=42 WHERE source_id=2;
		COMMIT;`); err != nil {
		t.Fatal(err)
	}
	check()
}
