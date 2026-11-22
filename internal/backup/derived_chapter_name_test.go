// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

func TestRestoreDerivedChapterNamesFromOfficialWire(t *testing.T) {
	for _, tt := range []struct {
		name, markName, positionName string
		marker, existingNames        bool
	}{
		{"official export", "null", "null", true, false},
		{"official export over real names", `"Saved mark"`, `"Saved position"`, true, true},
		{"Mihon without extension", `"Chapter 8"`, `"Chapter"`, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := exportDatabase(t)
			if tt.existingNames {
				old := int64(positionOnlyTestTime - 3000)
				exportRecord(t, db, 2, 1, old, "chapterMark", "moon", "/ch/8",
					`{"bookmarked":true,"chapterName":"Saved mark"}`)
				exportRecord(t, db, 2, 1, old, "position", "", "",
					`{"sourceSlug":"moon","chapterUrl":"/reader?id=9","chapterName":"Saved position","pageIndex":2}`)
				if _, err := db.Exec("INSERT INTO sync_accounts(user_id,seq) VALUES (2,2)"); err != nil {
					t.Fatal(err)
				}
			}

			// Construct the official layout directly, with the extra after the chapters.
			// The first chapter counts in wire order even though the decoder discards it.
			fields := [][]byte{rawInt(1, 123), rawText(2, "/f"), rawText(3, "Ferry"),
				rawField(16, rawText(1, "/ch/unread"), rawText(2, "Unread")),
				rawField(16, rawText(1, "/ch/8"), rawText(2, "Chapter 8"), rawInt(4, 1), rawInt(11, positionOnlyTestTime/1000)),
				rawField(16, rawText(1, "/ch/9"), rawText(2, "Interlude"), rawInt(4, 1)),
				rawField(16, rawText(1, "/reader?id=9"), rawText(2, "Chapter"), rawInt(6, 8)),
				rawInt(100, 0), rawField(104, rawText(1, "/reader?id=9"), rawInt(2, positionOnlyTestTime)),
			}
			wantMarks := 3
			if tt.marker {
				fields = append(fields, rawField(9100, rawText(1, "https://moon.example"), rawInt(4, 1),
					rawInt(5, 0), rawInt(5, 2), rawInt(5, 2), rawInt(5, 4), rawInt(5, 5),
					rawInt(5, 10001), rawInt(5, 1<<32|3), rawInt(5, ^uint64(0))))
				wantMarks = 2
			}
			restoreDerivedNameBackup(t, db, compressFixture(t, rawField(1, fields...)), wantMarks)
			assertSyncChapterName(t, db, "chapterMark", "/ch/8", tt.markName)
			assertSyncChapterName(t, db, "chapterMark", "/ch/9", `"Interlude"`)
			assertSyncChapterName(t, db, "position", "/reader?id=9", tt.positionName)
		})
	}
}

func TestExportDerivedChapterNameIndexes(t *testing.T) {
	db := exportDatabase(t)
	exportRecord(t, db, 1, 1, positionOnlyTestTime, "chapterMark", "moon", "/ch/number",
		`{"read":true,"chapterNumber":7.5}`)
	exportRecord(t, db, 1, 1, positionOnlyTestTime, "chapterMark", "moon", "/ch/part%20A+1/",
		`{"read":true}`)
	for i := 2; i < 128; i++ {
		exportRecord(t, db, 1, 1, positionOnlyTestTime, "chapterMark", "moon", fmt.Sprintf("/ch/%d", i),
			`{"read":true,"chapterName":"Stored name"}`)
	}
	exportRecord(t, db, 1, 1, positionOnlyTestTime, "chapterMark", "moon", "/ch/episode%2Easpx?id=7",
		`{"read":true}`)
	exportRecord(t, db, 1, 1, positionOnlyTestTime, "chapterMark", "moon", "/ch/%zz",
		`{"read":true}`)
	exportRecord(t, db, 1, 1, positionOnlyTestTime, "position", "", "",
		`{"sourceSlug":"moon","chapterUrl":"/READer?id=9","pageIndex":8}`)
	exportRecord(t, db, 1, 2, positionOnlyTestTime, "chapterMark", "moon", "/ch/named",
		`{"read":true,"chapterName":"Chapter"}`)

	var out bytes.Buffer
	if err := Export(t.Context(), db, 1, false, "https://kit.example", "Kit", &out); err != nil {
		t.Fatal(err)
	}
	// The synthesized position is first on the wire, before either page of marks.
	// The second title has only a stored name and must omit field 5 entirely.
	got := readDerivedChapterIndexes(t, out.Bytes())
	want := map[string][]int64{"/f": {1, 2, 3, 130, 131}, "/port": nil}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("field 5 by manga URL: got %v, want %v", got, want)
	}

	wantLabels := map[string]string{
		"/ch/number": "Chapter 7.5", "/ch/part%20A+1/": "part A+1", "/ch/episode%2Easpx?id=7": "Chapter",
		"/ch/%zz": "Chapter", "/READer?id=9": "Chapter", "/ch/named": "Chapter",
	}
	for _, item := range readMihonBackup(t, out.Bytes()) {
		for _, c := range item.chapters {
			if label, ok := wantLabels[c.url]; ok {
				if c.name != label {
					t.Errorf("Mihon name for %q: got %q, want %q", c.url, c.name, label)
				}
				delete(wantLabels, c.url)
			}
		}
	}
	if len(wantLabels) != 0 {
		t.Errorf("Mihon read missing chapters: %v", wantLabels)
	}

	restoreDerivedNameBackup(t, db, out.Bytes(), 131)
	assertSyncChapterName(t, db, "chapterMark", "/ch/number", "null")
	assertSyncChapterName(t, db, "chapterMark", "/ch/part%20A+1/", "null")
	assertSyncChapterName(t, db, "chapterMark", "/ch/episode%2Easpx?id=7", "null")
	assertSyncChapterName(t, db, "chapterMark", "/ch/%zz", "null")
	assertSyncChapterName(t, db, "chapterMark", "/ch/2", `"Stored name"`)
	assertSyncChapterName(t, db, "chapterMark", "/ch/named", `"Chapter"`)
	assertSyncChapterName(t, db, "position", "/READer?id=9", "null")
}

func TestDerivedChapterURLLabelMatchesOfficialFallback(t *testing.T) {
	long := strings.Repeat("a", 199)
	for _, tt := range []struct{ url, want string }{
		{"/book/reader", "reader"},
		{"/book/ViEw?chapter=1", "Chapter"},
		{"/book/chapter.php%C2%85", "Chapter"},
		{"/book/%E7%AC%AC%201%2F2", "第 1/2"},
		{"/book/" + long + "%20b", long + " "},
	} {
		got, derived := exportChapterName("", tt.url, nil)
		if !derived || got != tt.want {
			t.Errorf("%q: got %q (derived %t), want %q", tt.url, got, derived, tt.want)
		}
	}
}

func TestExportRejectsChangedDerivedNameOnReplayedPage(t *testing.T) {
	db := exportDatabase(t)
	_, err := db.Exec(`WITH RECURSIVE ns(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM ns WHERE i<=128)
	INSERT INTO sync_records(user_id,kind,key_title,source_slug,chapter_url,url_hash,title_id,data,client_updated_at,deleted,seq)
	SELECT 1,'chapterMark',0,'moon','/ch/'||i,CAST(i AS BLOB),1,'{"read":true}',1000,0,i FROM ns`)
	if err != nil {
		t.Fatal(err)
	}
	w := changingExportWriter{firstWrite: func() {
		// Both versions produce identical chapter bytes. Only field 5 changes.
		if _, err := db.Exec(`UPDATE sync_records SET data='{"read":true,"chapterName":"129"}' WHERE seq=129`); err != nil {
			t.Fatal(err)
		}
	}}
	if err := Export(t.Context(), db, 1, false, "https://kit.example", "Kit", &w); err == nil {
		t.Fatal("changed marker on a replayed page produced a successful export")
	}
}

func restoreDerivedNameBackup(t *testing.T, db *sql.DB, raw []byte, wantMarks int) {
	t.Helper()
	f, err := Decode(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	report, err := Restore(t.Context(), db, usersync.New(db), 2, f, false, "https://kit.example")
	if err != nil || report.ChapterMarks.Restored != wantMarks || report.ChapterMarks.Skipped != 0 {
		t.Fatalf("restore: %+v, %v; want %d marks", report, err, wantMarks)
	}
}

func assertSyncChapterName(t *testing.T, db *sql.DB, kind, url, wantJSON string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT data -> '$.chapterName' FROM sync_records
		WHERE user_id=2 AND kind=? AND COALESCE(NULLIF(chapter_url,''), json_extract(data,'$.chapterUrl'))=?`,
		kind, url).Scan(&got); err != nil {
		t.Errorf("%s %q name: %v", kind, url, err)
	} else if got != wantJSON {
		t.Errorf("%s %q name: got %s, want %s", kind, url, got, wantJSON)
	}
}

func readDerivedChapterIndexes(t *testing.T, raw []byte) map[string][]int64 {
	t.Helper()
	z, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	w := wireReader{r: bufio.NewReader(z), end: 1<<63 - 1}
	got := make(map[string][]int64)
	for f := w.next(); f != 0; f = w.next() {
		if f != 1 {
			w.skip()
			continue
		}
		var url string
		var indexes []int64
		w.message(func() {
			for f := w.next(); f != 0; f = w.next() {
				switch f {
				case 2:
					url = string(w.value(w.length()))
				case 9100:
					w.message(func() {
						for f := w.next(); f != 0; f = w.next() {
							if f == 5 {
								indexes = append(indexes, w.integer())
							} else {
								w.skip()
							}
						}
					})
				default:
					w.skip()
				}
			}
		})
		got[url] = indexes
	}
	if w.err != nil {
		t.Fatalf("reading unpacked field 5: %v", w.err)
	}
	return got
}
