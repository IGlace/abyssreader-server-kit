// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

type countedReader struct {
	*sql.DB
	reads int
}

func (c *countedReader) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	c.reads++
	return c.DB.QueryContext(ctx, q, args...)
}
func matchCatalog(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`INSERT INTO sources(id,slug,name,website,lang,created_at,updated_at,hosted) VALUES
 (1,'paper','One','https://paper.test','en',1,1,0),(2,'ink','Two','https://ink.test','en',1,1,0),(3,'hosted','Own','','en',1,1,1);
 INSERT INTO source_tachiyomi_ids VALUES ('77',2);
 INSERT INTO titles(id,ref,slug,name,name_key,status,manifest_hash,created_at,updated_at) VALUES
 (1,'w','w','The Forest','the forest','unknown','',1,1),(2,'i','i','Ink','ink','unknown','',1,1),(3,'h','h','Hidden','hidden','unknown','',1,1);
 INSERT INTO source_titles(id,title_id,source_id,url,lang,position) VALUES
 (1,1,1,'https://paper.test/story/?a=1','en',0),(2,2,2,'/story?a=1','en',0),(3,3,3,'/source-titles/3','en',0);
 INSERT INTO title_alt_names VALUES (1,0,'Árbol del Sol','árbol del sol'),(1,1,'Twin','twin'),(2,0,'Twin','twin')`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
func TestSourceMatchPriorityUniqueNameAndVisibility(t *testing.T) {
	db := matchCatalog(t)
	entries := []Entry{
		{URL: "/story?a=1", Source: 77, Title: "Ink", Extra: &Extra{Website: "https://www.PAPER.test"}},
		{URL: "https://other.test/story/?a=1#f", Source: 77, Title: "The Forest"},
		{Title: " ARBOL   DEL SOL "}, {Title: "Twin"}, {Title: "%"}, {Title: "Hidden", URL: "/source-titles/3", Extra: &Extra{Website: "https://self.test"}}, {Title: "missing"},
	}
	got, err := matchEntries(t.Context(), db, entries, false, "https://self.test")
	if err != nil {
		t.Fatal(err)
	}
	want := []match{{1, "paper"}, {2, "ink"}, {1, ""}, {}, {}, {}, {}}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}
func TestMatchingReadCountAtScale(t *testing.T) {
	db := &countedReader{DB: matchCatalog(t)}
	entries := make([]Entry, 2000)
	for i := range entries {
		entries[i] = Entry{Title: fmt.Sprintf("Title %d", i), URL: fmt.Sprintf("/m/%d", i), Source: 77, Extra: &Extra{Website: "https://paper.test"}, Chapters: make([]Chapter, 50)}
	}
	_, err := matchEntries(t.Context(), db, entries, false, "https://self.test")
	if err != nil {
		t.Fatal(err)
	}
	if db.reads != 3 {
		t.Fatalf("2000 titles, 100000 chapters: %d matching SELECTs; want 3", db.reads)
	}
}

func TestNumericMapUsesDecimalIdentityAndSkipsAmbiguousAliases(t *testing.T) {
	db := matchCatalog(t)
	if _, err := db.Exec("UPDATE source_tachiyomi_ids SET tachiyomi_id='00077'"); err != nil {
		t.Fatal(err)
	}
	e := []Entry{{Source: 77, URL: "/story?a=1"}}
	result, err := matchEntries(t.Context(), db, e, false, "")
	if err != nil || result[0] != (match{2, "ink"}) {
		t.Fatalf("decimal alias: %v %v", result, err)
	}
	if _, err := db.Exec("INSERT INTO source_tachiyomi_ids VALUES('77',1)"); err != nil {
		t.Fatal(err)
	}
	result, err = matchEntries(t.Context(), db, e, false, "")
	if err != nil || result[0].title != 0 {
		t.Fatalf("ambiguous numeric identity: %v %v", result, err)
	}
}
