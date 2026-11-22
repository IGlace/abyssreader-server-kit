// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package usersync

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/store"
	"modernc.org/sqlite"
)

// Count the SELECTs SQLite receives, including those inside write transactions.
type countedConnector struct {
	d     sqlite.Driver
	path  string
	reads int
}

func (c *countedConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := c.d.Open(c.path)
	if err != nil {
		return nil, err
	}
	return &countedConn{sqliteConn: conn.(sqliteConn), c: c}, nil
}
func (c *countedConnector) Driver() driver.Driver { return &c.d }

type sqliteConn interface {
	driver.Conn
	driver.ConnBeginTx
	driver.ExecerContext
	driver.QueryerContext
}
type countedConn struct {
	sqliteConn
	c *countedConnector
}

func (c *countedConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.c.reads++
	return c.sqliteConn.QueryContext(ctx, q, args)
}

func TestChapterPathPreservesLiteralEscapesAndUnicode(t *testing.T) {
	for _, raw := range []string{"https://old.test/漫画/%2f/%C3%84/?x=日#fragment", "/漫画/%2f/%C3%84?x=日"} {
		if got := ChapterPathKey(raw); got != "/漫画/%2f/%C3%84?x=日" {
			t.Errorf("key for %s = %s", raw, got)
		}
	}
}

func TestRestoreRepeatedSlashesAcrossBatches(t *testing.T) {
	s, user, _ := restoreFixture(t)
	w := s.NewRestoreWriter(user, false)
	mark := Change{Kind: ChapterMark, Key: Key{MainMangaID: 1, SourceSlug: "paper-moon", ChapterURL: "/c//?p=1"}, Data: json.RawMessage(`{"read":true,"bookmarked":false}`)}
	for _, want := range []string{"applied", "stale"} {
		out, err := w.RestoreBatch(t.Context(), []Change{mark})
		if err != nil || len(out) != 1 || out[0].Status != want {
			t.Fatalf("got %v %v want %s", out, err, want)
		}
	}
}

// A fresh account restoring 2,000 catalog titles with 50 marks each. Isolate
// the writer, including validation and transactions, from decoding/matching.
func BenchmarkRestoreWriter100k(b *testing.B) {
	for range b.N {
		b.StopTimer()
		path := filepath.Join(b.TempDir(), "test.db")
		initial, err := store.Open(b.Context(), path)
		if err != nil {
			b.Fatal(err)
		}
		initial.Close()
		c := &countedConnector{path: "file:" + path + "?_txlock=immediate&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"}
		db := sql.OpenDB(c)
		_, err = db.Exec(`INSERT INTO sources(id,slug,name,website,lang,created_at,updated_at) VALUES(1,'paper','paper','https://paper.test','en',0,0);
			INSERT INTO users(id,email,email_key,display_name,created_at,updated_at) VALUES(1,'a@b.test','a@b.test','me',0,0);
			WITH RECURSIVE ids(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM ids WHERE x<2000)
			INSERT INTO titles(id,ref,slug,name,name_key,status,manifest_hash,created_at,updated_at) SELECT x,'t'||x,'t'||x,'t','t','unknown','',0,0 FROM ids;
			INSERT INTO source_titles(title_id,source_id,url,lang,position) SELECT id,1,'/t/'||id,'en',0 FROM titles`)
		if err != nil {
			b.Fatal(err)
		}
		items := make([]Change, 100000)
		for i := range items {
			items[i] = Change{Kind: ChapterMark, Key: Key{MainMangaID: int64(i/50 + 1), SourceSlug: "paper", ChapterURL: fmt.Sprintf("/c/%d", i)}, ClientUpdatedAt: early, Data: json.RawMessage(`{"read":true,"bookmarked":false,"lastPageRead":3,"chapterName":"Chapter","chapterNumber":5}`)}
		}
		w := New(db).NewRestoreWriter(1, false)
		b.StartTimer()
		for batch := range slices.Chunk(items, MaxBatch) {
			out, err := w.RestoreBatch(b.Context(), batch)
			if err != nil {
				b.Fatal(err)
			}
			for _, r := range out {
				if r.Status != "applied" {
					b.Fatal(r)
				}
			}
		}
		b.StopTimer()
		b.ReportMetric(float64(c.reads), "SELECTs/restore")
		db.Close()
	}
}

// A long restore can reuse cap counts between batches until another sync write.
func TestRestoreDoesNotRecountEveryAccountRowInEachBatch(t *testing.T) {
	s, user, db := restoreFixture(t)
	var seq int
	var name, path string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	conn := &countedConnector{path: "file:" + path + "?_txlock=immediate&_pragma=foreign_keys(1)"}
	counted := sql.OpenDB(conn)
	defer counted.Close()
	s = New(counted)
	s.caps[Favorite] = 2
	w := s.NewRestoreWriter(user, false)
	restore := func(id int64, want string) {
		t.Helper()
		out, err := w.RestoreBatch(t.Context(), []Change{{Kind: Favorite, Key: Key{MainMangaID: id}, ClientUpdatedAt: early}})
		if err != nil || len(out) != 1 || out[0].Status != want {
			t.Fatalf("title %d %v %v", id, out, err)
		}
	}
	restore(1, "applied")
	first := conn.reads
	restore(2, "applied")
	if got := conn.reads - first; got != 3 {
		t.Errorf("second batch used %d SELECTs, want the account, catalog pairs and current rows", got)
	}
	if got := push1(t, s, user, fav(t, 1, late, true)); got != "applied" {
		t.Fatal(got)
	}
	restore(3, "applied")
}

func TestRestoreRechecksStoredCeilingAfterPurge(t *testing.T) {
	s, user, _ := restoreFixture(t)
	s.maxStored = 2
	w := s.NewRestoreWriter(user, false)
	if got := push1(t, s, user, fav(t, 2, early, true)); got != "applied" {
		t.Fatal(got)
	}
	try := func(id int64, want string) {
		t.Helper()
		out, err := w.RestoreBatch(t.Context(), []Change{{Kind: Planning, Key: Key{MainMangaID: id}, ClientUpdatedAt: late}})
		if err != nil || len(out) != 1 || out[0].Status != want {
			t.Fatalf("got %v %v, want %s", out, err, want)
		}
	}
	try(1, "applied")
	try(3, "rejected")
	if n, err := s.PurgeTombstones(t.Context(), mustTime(t, "2027-01-01T00:00:00.000Z")); err != nil || n != 1 {
		t.Fatalf("purged %d %v", n, err)
	}
	try(3, "applied")
}
