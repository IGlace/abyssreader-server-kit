// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/store"
	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

// Run this benchmark once in an isolated test process to measure peak RSS of
// gzip decode, catalog matching and SQLite writes together.
func BenchmarkAccountRestoreNearLimit(b *testing.B) {
	ctx := b.Context()
	db, err := store.Open(ctx, filepath.Join(b.TempDir(), "kit.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO sources(id,slug,name,website,lang,created_at,updated_at) VALUES(1,'paper','Paper','https://paper.test','en',0,0);
 INSERT INTO source_tachiyomi_ids VALUES ('7',1);
 WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<2000)
 INSERT INTO titles(id,ref,slug,name,name_key,status,manifest_hash,created_at,updated_at) SELECT i,'m'||i,'m'||i,'M'||i,'m'||i,'unknown','',0,0 FROM n;
 INSERT INTO source_titles(title_id,source_id,url,lang,position) SELECT id,1,'/m/'||id,'en',0 FROM titles;`)
	if err != nil {
		b.Fatal(err)
	}
	var payload bytes.Buffer
	z := gzip.NewWriter(&payload)
	name := strings.Repeat("n", 455)
	for id := 1; id <= 2000; id++ {
		e := Entry{Source: 7, URL: fmt.Sprintf("/m/%d", id), Title: fmt.Sprintf("M%d", id), Extra: &Extra{Website: "https://paper.test"}}
		for chapter := range 50 {
			e.Chapters = append(e.Chapters, Chapter{URL: fmt.Sprintf("/m/%d/c/%d", id, chapter), Read: true, Name: name, Modified: 1700000000})
		}
		if err := WriteEntry(z, e); err != nil {
			b.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		b.Fatal(err)
	}
	svc := usersync.New(db)
	b.ResetTimer()
	for i := 1; i <= b.N; i++ {
		_, err := db.Exec(`INSERT INTO users(id,email,email_key,display_name,created_at,updated_at) VALUES(?,?,?,'Reader',0,0)`, i, fmt.Sprint(i), fmt.Sprint(i))
		if err != nil {
			b.Fatal(err)
		}
		f, err := Decode(bytes.NewReader(payload.Bytes()), 20<<20)
		if err != nil {
			b.Fatal(err)
		}
		r, err := Restore(ctx, db, svc, int64(i), f, false, "")
		if err != nil || r.Titles.Matched != 2000 || r.ChapterMarks.Restored != 100000 {
			b.Fatalf("restored %d titles, %d marks: %v", r.Titles.Matched, r.ChapterMarks.Restored, err)
		}
	}
	b.ReportMetric(float64(payload.Len()), "gzip-bytes/file")
}
