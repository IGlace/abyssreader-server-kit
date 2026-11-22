// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

func TestExportChapterNamesInMihonModel(t *testing.T) {
	db := exportDatabase(t)
	marks := []struct {
		url, data, name string
	}{
		{"/ch/unnamed", `{"read":true}`, "unnamed"},
		{"/ch/whole", `{"read":true,"chapterName":"   ","chapterNumber":12.0}`, "Chapter 12"},
		{"/ch/fraction", `{"read":true,"chapterNumber":1.23456789}`, "Chapter 1.23456789"},
		{"/ch/small", `{"read":true,"chapterNumber":1e-7}`, "Chapter 0.0000001"},
		{"/ch/large", `{"read":true,"chapterNumber":1e23}`, "Chapter 100000000000000000000000"},
		{"/ch/minimum", `{"read":true,"chapterNumber":5e-324}`, "Chapter 0." + strings.Repeat("0", 323) + "49"},
		{"/ch/negative/?view=1#reader", `{"read":true,"chapterNumber":-3,"chapterName":null}`, "negative"},
		{"/?view=1", `{"read":true}`, "Chapter"},
		{"/ch/zero", `{"read":true,"chapterNumber":0}`, "Chapter 0"},
		{"/ch/named", `{"read":true,"chapterName":"The actual name","chapterNumber":4}`, "The actual name"},
	}
	want := map[string]string{"/chapter/position": "Chapter 2.5"}
	for _, m := range marks {
		exportRecord(t, db, 1, 1, positionOnlyTestTime, "chapterMark", "moon", m.url, m.data)
		want[m.url] = m.name
	}
	exportRecord(t, db, 1, 2, positionOnlyTestTime, "position", "", "",
		`{"sourceSlug":"moon","chapterUrl":"/chapter/position","pageIndex":8,"chapterNumber":2.5}`)
	var out bytes.Buffer
	if err := Export(t.Context(), db, 1, false, "https://kit.example", "Kit", &out); err != nil {
		t.Fatal(err)
	}
	items := readMihonBackup(t, out.Bytes())
	if len(items) != 2 {
		t.Fatalf("Mihon manga count %d, want 2", len(items))
	}
	for _, item := range items {
		for _, c := range item.chapters {
			if name, ok := want[c.url]; !ok || c.name != name {
				t.Errorf("Mihon chapter %q name %q, want %q", c.url, c.name, name)
			}
			delete(want, c.url)
		}
	}
	if len(want) != 0 {
		t.Errorf("Mihon backup missing chapters: %v", want)
	}
	f, err := Decode(&out, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Restore(t.Context(), db, usersync.New(db), 2, f, false, "https://kit.example")
	if err != nil || report.ChapterMarks.Restored != len(marks) {
		t.Fatalf("restore: %+v, %v; want %d marks", report, err, len(marks))
	}
}

func TestRestorePreservesExistingChapterNameFromFallback(t *testing.T) {
	for _, test := range []struct {
		name, storedURL, incomingURL, incomingName, wantMark, wantPosition string
	}{
		{"number fallback", "/ch/2", "/ch/2", "Chapter 1.23456789", "Real mark", "Real position"},
		{"URL fallback", "/ch/2", "/ch/2/", "2", "Real mark", "Real position"},
		{"default fallback", "/ch/2", "/ch/2", "Chapter", "Real mark", "Real position"},
		{"backup supplies a real name", "/ch/2", "/ch/2", "Last Stop", "Last Stop", "Last Stop"},
		{"position changes chapter", "/ch/1", "/ch/2", "2", "2", "2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := exportDatabase(t)
			old := int64(positionOnlyTestTime - 3000)
			exportRecord(t, db, 2, 1, old, "chapterMark", "moon", test.storedURL, `{"bookmarked":true,"chapterName":"Real mark","lastPageRead":2}`)
			oldPosition, err := json.Marshal(map[string]any{"sourceSlug": "moon", "chapterUrl": test.storedURL, "chapterName": "Real position", "pageIndex": 2})
			if err != nil {
				t.Fatal(err)
			}
			exportRecord(t, db, 2, 1, old, "position", "", "", string(oldPosition))
			if _, err := db.Exec("INSERT INTO sync_accounts(user_id,seq) VALUES (2,2)"); err != nil {
				t.Fatal(err)
			}

			var out bytes.Buffer
			if err := WriteEntry(&out, Entry{Source: 123, URL: "/f", Title: "Ferry",
				History: &History{URL: test.incomingURL, LastRead: positionOnlyTestTime},
				Chapters: []Chapter{{URL: test.incomingURL, Name: test.incomingName, Read: true,
					Page: 8, Number: 1.23456789, Modified: positionOnlyTestTime / 1000}},
			}); err != nil {
				t.Fatal(err)
			}
			f, err := Decode(&out, 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			report, err := Restore(t.Context(), db, usersync.New(db), 2, f, false, "https://kit.example")
			if err != nil || report.ChapterMarks.Restored != 1 {
				t.Fatalf("restore: %+v, %v", report, err)
			}
			var positionName, markName string
			var positionPage, markPage int
			if err := db.QueryRow("SELECT json_extract(data,'$.chapterName'),json_extract(data,'$.pageIndex') FROM sync_records WHERE user_id=2 AND kind='position'").
				Scan(&positionName, &positionPage); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow("SELECT json_extract(data,'$.chapterName'),json_extract(data,'$.lastPageRead') FROM sync_records WHERE user_id=2 AND kind='chapterMark' AND json_extract(data,'$.read')=1").
				Scan(&markName, &markPage); err != nil {
				t.Fatal(err)
			}
			if positionName != test.wantPosition || markName != test.wantMark || positionPage != 8 || markPage != 8 {
				t.Fatalf("position %q at %d and mark %q at %d; want %q and %q at 8",
					positionName, positionPage, markName, markPage, test.wantPosition, test.wantMark)
			}
		})
	}
}

type mihonManga struct {
	source   int64
	url      string
	chapters []mihonChapter
	history  []History
}

type mihonChapter struct {
	url, name string
	page      int64
}

// Walk the complete gzip payload using Mihon's field layout and required fields.
// The kit's own decoder accepts a missing BackupChapter.name, so it cannot
// establish compatibility with the third-party decoder.
func readMihonBackup(t *testing.T, raw []byte) []mihonManga {
	t.Helper()
	z, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	w := wireReader{r: bufio.NewReader(z), end: math.MaxInt64}
	readText := func() string { return string(w.value(w.length())) }
	var manga []mihonManga
	for field := w.next(); field != 0; field = w.next() {
		switch field {
		case 1:
			var m mihonManga
			var hasSource bool
			w.message(func() {
				for field := w.next(); field != 0; field = w.next() {
					switch field {
					case 1:
						m.source, hasSource = w.integer(), true
					case 2:
						m.url = readText()
					case 16:
						var c mihonChapter
						w.message(func() {
							for field := w.next(); field != 0; field = w.next() {
								switch field {
								case 1:
									c.url = readText()
								case 2:
									c.name = readText()
								case 6:
									c.page = w.integer()
								default:
									w.skip()
								}
							}
						})
						if c.url == "" || c.name == "" {
							t.Errorf("Mihon chapter %q is missing a required URL or name", c.url)
						}
						m.chapters = append(m.chapters, c)
					case 104:
						var h History
						var hasTime bool
						w.message(func() {
							for field := w.next(); field != 0; field = w.next() {
								switch field {
								case 1:
									h.URL = readText()
								case 2:
									h.LastRead, hasTime = w.integer(), true
								default:
									w.skip()
								}
							}
						})
						if h.URL == "" || !hasTime {
							t.Errorf("Mihon history is missing its URL or timestamp: %+v", h)
						}
						m.history = append(m.history, h)
					default:
						w.skip()
					}
				}
			})
			if !hasSource || m.url == "" {
				t.Errorf("Mihon manga is missing its source or URL: %+v", m)
			}
			manga = append(manga, m)
		case 101:
			var hasID bool
			w.message(func() {
				for field := w.next(); field != 0; field = w.next() {
					if field == 2 {
						w.integer()
						hasID = true
					} else {
						w.skip()
					}
				}
			})
			if !hasID {
				t.Error("Mihon source is missing its ID")
			}
		default:
			w.skip()
		}
	}
	if w.err != nil {
		t.Fatalf("Mihon backup: %v", w.err)
	}
	return manga
}
