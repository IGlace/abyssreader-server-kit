// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"database/sql"
	"strings"
	"testing"
)

func TestExportQueryPlansStayWithinCaller(t *testing.T) {
	db := exportDatabase(t)
	exportRecord(t, db, 1, 1, 1000, "chapterMark", "moon", "/mine", `{"read":true}`)
	// Most marks on both popular titles belong to someone else. Most of
	// the catalog has no sync records at all.
	_, err := db.Exec(`WITH RECURSIVE n(i) AS (VALUES(1) UNION ALL SELECT i+1 FROM n WHERE i<4096)
	INSERT INTO sync_records(user_id,kind,key_title,source_slug,chapter_url,url_hash,title_id,data,client_updated_at,deleted,seq)
	SELECT 2,'chapterMark',0,'moon','/other/'||i,CAST(i AS BLOB),1+i%2,'{"read":true}',0,0,i FROM n;
	WITH RECURSIVE n(i) AS (VALUES(3) UNION ALL SELECT i+1 FROM n WHERE i<1024)
	INSERT INTO titles(id,ref,slug,name,name_key,status,manifest_hash,created_at,updated_at)
	SELECT i,'catalog-'||i,'title-'||i,'Title '||i,'title '||i,'unknown','',0,0 FROM n;
	ANALYZE;`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, query, predicate string
		args                   []any
	}{
		{"title discovery", exportTitles, "user_id=? AND title_id>?", []any{0, 1, 128}},
		{"source discovery", exportActiveSources, "user_id=? AND title_id=?", []any{1, false, 0, "", 1, 128}},
		{"mark page", exportMarks + exportMarkOrder, "user_id=? AND title_id=? AND rowid>?", []any{1, 1, "moon", 0, 999, 128}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := exportPlan(t, db, tc.query, tc.args...)
			t.Log(plan)
			if !strings.Contains(plan, tc.predicate) {
				t.Errorf("missing indexed caller range %q", tc.predicate)
			}
			if tc.name == "title discovery" && !strings.Contains(plan, "SEARCH t USING INTEGER PRIMARY KEY (rowid=?)") {
				t.Error("expected a title primary-key lookup per owned title")
			}
		})
	}
	got := runExport(t, db, false)
	if len(got.Entries) != 1 || len(got.Entries[0].Chapters) != 1 || got.Entries[0].Chapters[0].URL != "/mine" {
		t.Fatalf("expected only caller's chapter on a popular title, got %+v", got)
	}
}

func exportPlan(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(details, "\n")
}
