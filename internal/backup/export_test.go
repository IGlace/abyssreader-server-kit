// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

func exportDatabase(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "kit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
	INSERT INTO users(id,email,email_key,display_name,created_at,updated_at) VALUES(1,'a','a','A',0,0),(2,'b','b','B',0,0);
	INSERT INTO sources(id,slug,name,website,lang,hosted,created_at,updated_at) VALUES
	 (1,'moon','Paper Moon','https://moon.example','en',0,0,0),
	 (2,'amber','Amber','https://amber.example','en',0,0,0),(3,'hosted','hosted','','en',1,0,0);
	INSERT INTO source_tachiyomi_ids VALUES ('123',1);
	INSERT INTO titles(id,ref,slug,name,name_key,status,manifest_hash,created_at,updated_at) VALUES
	 (1,'f','f','Ferry','ferry','unknown','',0,0),(2,'p','p','Port','port','unknown','',0,0);
	INSERT INTO source_titles(id,title_id,source_id,url,lang,position) VALUES
	 (1,1,1,'/f','en',0),(2,1,2,'/amber-f','en',1),(3,2,1,'/port','en',0),
	 (4,2,3,'/source-titles/4','en',0),(5,2,3,'/source-titles/5','fr',1);
	INSERT INTO chapters(id,source_title_id,ref,position,published_at,page_count,manifest_hash)
	 VALUES(10,4,'en-one',0,0,1,''),(11,5,'fr-one',0,0,1,'');`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func exportRecord(t *testing.T, db *sql.DB, user, title, at int64, kind, source, url string, data any) {
	t.Helper()
	key := title
	hash := []byte{}
	if kind == "chapterMark" {
		key = 0
		sum := sha256.Sum256([]byte(url))
		hash = sum[:]
	}
	_, err := db.Exec(`INSERT INTO sync_records(user_id,kind,key_title,source_slug,chapter_url,url_hash,title_id,data,client_updated_at,deleted,seq)
	    SELECT ?,?,?,?,?,?,?,?,?,0,COALESCE(MAX(seq),0)+1 FROM sync_records WHERE user_id=?`, user, kind, key, source, url, hash, title, data, at, user)
	if err != nil {
		t.Fatal(err)
	}
}

type exportWriter struct {
	bytes.Buffer
	db *sql.DB
}

func (w *exportWriter) Write(p []byte) (int, error) {
	if n := w.db.Stats().InUse; n != 0 {
		return 0, fmt.Errorf("%d database connections still leased during response write", n)
	}
	return w.Buffer.Write(p)
}

func runExport(t *testing.T, db *sql.DB, hosted bool) *File {
	t.Helper()
	w := &exportWriter{db: db}
	if err := Export(t.Context(), db, 1, hosted, "https://kit.example", "Lantern", w); err != nil {
		t.Fatal(err)
	}
	raw := w.Bytes()
	if !bytes.HasPrefix(raw, []byte{0x1f, 0x8b}) {
		t.Fatal("wanted a gzip backup")
	}
	f, err := Decode(bytes.NewReader(raw), 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestExportCallerMarksListsVoteAndPosition(t *testing.T) {
	db := exportDatabase(t)
	exportRecord(t, db, 1, 1, 10123, "favorite", "", "", nil)
	exportRecord(t, db, 1, 1, 20234, "planning", "", "", nil)
	exportRecord(t, db, 1, 1, 40555, "rating", "", "", `{"opinion":"down"}`)
	exportRecord(t, db, 1, 1, 11111, "chapterMark", "moon", "/ch/1", `{"read":true,"bookmarked":true,"chapterName":"One","lastPageRead":3,"chapterNumber":1.5}`)
	exportRecord(t, db, 1, 1, 50678, "position", "", "", `{"sourceSlug":"amber","chapterUrl":"/other","chapterName":"Two","pageIndex":8,"chapterNumber":2}`)
	exportRecord(t, db, 1, 1, 30000, "blacklist", "", "", nil)
	if _, err := db.Exec("UPDATE sync_records SET deleted=1 WHERE kind='blacklist'"); err != nil {
		t.Fatal(err)
	}
	exportRecord(t, db, 2, 1, 10000, "chapterMark", "moon", "/private", `{"read":true}`)
	exportRecord(t, db, 1, 2, 10000, "inbox", "", "", `{"seen":false}`)
	f := runExport(t, db, true)
	if len(f.Entries) != 2 || f.SourceNames[123] != "Paper Moon" {
		t.Fatalf("unexpected backup: %+v", f)
	}
	entries := map[string]Entry{}
	for _, e := range f.Entries {
		entries[e.URL] = e
		if e.Title != "Ferry" || !e.Favorite || e.Modified != 40 || e.FavoriteModified != 10 ||
			e.Extra == nil || e.Extra.Vote != "down" || len(e.Extra.Lists) != 2 || !slices.Contains(e.Extra.Lists, "favorite") || !slices.Contains(e.Extra.Lists, "planning") {
			t.Fatalf("title state: %+v", e)
		}
	}
	m := entries["/f"]
	if m.Source != 123 || m.History != nil || m.Extra.Website != "https://moon.example" || len(m.Chapters) != 1 || m.Chapters[0] != (Chapter{URL: "/ch/1", Name: "One", Read: true, Bookmark: true, Page: 3, Number: 1.5, Modified: 11}) {
		t.Fatalf("marked entry: %+v", m)
	}
	p := entries["/amber-f"]
	if p.History == nil || *p.History != (History{URL: "/other", LastRead: 50678}) || len(p.Chapters) != 1 || p.Chapters[0] != (Chapter{URL: "/other", Name: "Two", positionOnly: true, Page: 8, Number: 2, Modified: 50}) {
		t.Fatalf("position: %+v", p)
	}
}

func TestExportReadableFallbackAndHostedLanguage(t *testing.T) {
	db := exportDatabase(t)
	exportRecord(t, db, 1, 2, 0, "planning", "", "", nil)
	for _, tc := range []struct {
		hosted       bool
		url, website string
	}{{true, "/source-titles/4", "https://kit.example"}, {false, "/port", "https://moon.example"}} {
		f := runExport(t, db, tc.hosted)
		if len(f.Entries) != 1 || f.Entries[0].Extra == nil || f.Entries[0].URL != tc.url || f.Entries[0].Extra.Website != tc.website || f.Entries[0].Favorite || (tc.hosted && f.SourceNames[f.Entries[0].Source] != "Lantern") {
			t.Fatalf("fallback (hosted=%v): %+v", tc.hosted, f)
		}
	}
	exportRecord(t, db, 1, 2, 12345, "chapterMark", "hosted", "/chapters/11", `{"read":true,"lastPageRead":9}`)
	exportRecord(t, db, 1, 2, 23456, "position", "", "", `{"sourceSlug":"hosted","chapterUrl":"/chapters/11","pageIndex":2}`)
	f := runExport(t, db, true)
	if len(f.Entries) != 1 || f.Entries[0].URL != "/source-titles/5" || len(f.Entries[0].Chapters) != 1 || f.Entries[0].Chapters[0].Page != 9 || f.Entries[0].History == nil {
		t.Fatalf("hosted French mark and position: %+v", f)
	}
}

func TestExportPagesOneLargeTitleWithoutLosingMarks(t *testing.T) {
	db := exportDatabase(t)
	const count = 10010
	_, err := db.Exec(`WITH RECURSIVE ns(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM ns WHERE i<?)
	INSERT INTO sync_records(user_id,kind,key_title,source_slug,chapter_url,url_hash,title_id,data,client_updated_at,deleted,seq)
	SELECT 1,'chapterMark',0,'moon','/ch/'||i,CAST(i AS BLOB),1,'{"read":true}',1000,0,i FROM ns`, count)
	if err != nil {
		t.Fatal(err)
	}
	exportRecord(t, db, 1, 1, 2000, "position", "", "", `{"sourceSlug":"moon","chapterUrl":"/ch/1","pageIndex":3}`)
	f := runExport(t, db, false)
	seen := map[string]bool{}
	positions := 0
	for _, e := range f.Entries {
		if e.URL != "/f" {
			t.Fatalf("unexpected source title %+v", e)
		}
		if e.History != nil {
			positions++
			if !slices.ContainsFunc(e.Chapters, func(c Chapter) bool { return c.URL == e.History.URL }) {
				t.Fatal("history missing its chapter")
			}
		}
		for _, c := range e.Chapters {
			if seen[c.URL] {
				t.Fatalf("duplicate %s", c.URL)
			}
			seen[c.URL] = true
		}
	}
	if positions != 1 || len(seen) != count {
		t.Fatalf("positions=%d chapters=%d", positions, len(seen))
	}
}

func TestExportEmptyAccount(t *testing.T) {
	f := runExport(t, exportDatabase(t), true)
	if len(f.Entries) != 0 || len(f.SourceNames) != 0 {
		t.Fatalf("empty account: %+v", f)
	}
}

type changingExportWriter struct {
	bytes.Buffer
	firstWrite func()
}

func (w *changingExportWriter) Write(p []byte) (int, error) {
	if w.firstWrite != nil {
		w.firstWrite()
		w.firstWrite = nil
	}
	return w.Buffer.Write(p)
}

func TestExportOverlappingSyncCannotProduceCorruptCompleteBackup(t *testing.T) {
	db := exportDatabase(t)
	// The second page of this source title changes after sizing the protobuf,
	// when the first response write releases control to the client.
	_, err := db.Exec(`WITH RECURSIVE ns(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM ns WHERE i<256)
	INSERT INTO sync_records(user_id,kind,key_title,source_slug,chapter_url,url_hash,title_id,data,client_updated_at,deleted,seq)
	SELECT 1,'chapterMark',0,'moon','/ch/'||i,CAST(i AS BLOB),1,'{"read":true}',1000,0,i FROM ns`)
	if err != nil {
		t.Fatal(err)
	}
	w := changingExportWriter{firstWrite: func() {
		if n := db.Stats().InUse; n != 0 {
			t.Fatalf("database still in use on response write: %d", n)
		}
		if _, err := db.Exec("UPDATE sync_records SET deleted=1 WHERE seq=250"); err != nil {
			t.Fatal(err)
		}
	}}
	if err := Export(t.Context(), db, 1, true, "https://kit.example", "Kit", &w); err == nil {
		t.Fatal("changed chapter page produced a successful download")
	}
	if _, err := Decode(bytes.NewReader(w.Bytes()), 16<<20); err == nil {
		t.Fatal("truncated download decoded successfully")
	}
	if got := runExport(t, db, true); len(got.Entries) != 1 || len(got.Entries[0].Chapters) != 255 {
		t.Fatalf("retry after sync lost marks: %+v", got)
	}
}

func TestExportUnicodeSourceAndChapterURLsRoundTrip(t *testing.T) {
	db := exportDatabase(t)
	// The official decoder limits URL fields to 2048 encoded bytes and names to 1024.
	titleURL := "/title/" + strings.Repeat("雪", 680)
	chapterURL := "/chapter/" + strings.Repeat("月", 679)
	name := strings.Repeat("星", 341)
	if _, err := db.Exec("UPDATE source_titles SET url=? WHERE id=1", titleURL); err != nil {
		t.Fatal(err)
	}
	exportRecord(t, db, 1, 1, 1000, "chapterMark", "moon", chapterURL, `{"read":true,"chapterName":"`+name+`"}`)
	f := runExport(t, db, true)
	if len(f.Entries) != 1 || f.Entries[0].URL != titleURL || len(f.Entries[0].Chapters) != 1 || f.Entries[0].Chapters[0].URL != chapterURL || f.Entries[0].Chapters[0].Name != name {
		t.Fatal("Unicode source-title URL, chapter URL or name changed on round trip")
	}
}
