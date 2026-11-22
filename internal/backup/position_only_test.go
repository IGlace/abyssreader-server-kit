// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

const positionOnlyTestTime = 1767312000123

func TestRestorePositionOnlyChapterFlag(t *testing.T) {
	chapter := func(url string, page, read, bookmark uint64) []byte {
		return rawField(16, rawText(1, url), rawText(2, "Chapter 2"),
			rawInt(4, read), rawInt(5, bookmark), rawInt(6, page), rawInt(11, positionOnlyTestTime/1000))
	}
	history := func(url string, at uint64) []byte {
		return rawField(104, rawText(1, url), rawInt(2, at))
	}
	position := chapter("/ch/2", 8, 0, 0)
	tests := []struct {
		name     string
		flag     bool
		chapters [][]byte
		earlier  []byte
		marks    []restoredPositionOnlyMark
	}{
		{"official export", true, [][]byte{position}, nil, nil},
		{"Mihon page-only mark", false, [][]byte{position}, nil, []restoredPositionOnlyMark{{Page: 8}}},
		{"flag with read", true, [][]byte{chapter("/ch/2", 8, 1, 0)}, nil, []restoredPositionOnlyMark{{Page: 8, Read: true}}},
		{"flag with bookmark", true, [][]byte{chapter("/ch/2", 8, 0, 1)}, nil, []restoredPositionOnlyMark{{Page: 8, Bookmarked: true}}},
		{"flag with duplicate URL", true, [][]byte{position, chapter("/ch/2", 0, 0, 0)}, nil, []restoredPositionOnlyMark{{Page: 8}}},
		{"flag with earlier history chapter", true, [][]byte{chapter("/ch/1", 3, 0, 0), position}, history("/ch/1", positionOnlyTestTime-1000), []restoredPositionOnlyMark{{Page: 3}}},
		{"flag with normalized URL alias", true, [][]byte{chapter("https://moon.example/ch/2/", 3, 0, 0), position}, nil, []restoredPositionOnlyMark{{Page: 3}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build the v1 export wire layout independently of the kit encoder.
			fields := [][]byte{rawInt(1, 123), rawText(2, "/f"), rawText(3, "Ferry")}
			fields = append(fields, tt.chapters...)
			fields = append(fields, rawInt(100, 0), history("/ch/2", positionOnlyTestTime), tt.earlier)
			if tt.flag {
				fields = append(fields, rawField(9100, rawText(1, "https://moon.example"), rawInt(4, 1)))
			}
			backup := bytes.Join([][]byte{rawField(1, fields...), rawField(101, rawText(1, "Paper Moon"), rawInt(2, 123))}, nil)
			checkPositionOnlyRestore(t, exportDatabase(t), compressFixture(t, backup), tt.marks)
		})
	}
}

func TestExportPositionOnlyChapterRestoresWithoutMark(t *testing.T) {
	db := exportDatabase(t)
	exportRecord(t, db, 1, 1, positionOnlyTestTime, "position", "", "",
		`{"sourceSlug":"moon","chapterUrl":"/ch/2","pageIndex":8,"chapterNumber":2}`)
	var out bytes.Buffer
	if err := Export(t.Context(), db, 1, false, "https://kit.example", "Kit", &out); err != nil {
		t.Fatal(err)
	}
	t.Run("kit restore", func(t *testing.T) {
		checkPositionOnlyRestore(t, db, out.Bytes(), nil)
	})
	t.Run("Mihon model", func(t *testing.T) {
		items := readMihonBackup(t, out.Bytes())
		if len(items) != 1 {
			t.Fatalf("Mihon manga: %+v; want one entry", items)
		}
		m := items[0]
		wantChapter := mihonChapter{url: "/ch/2", name: "Chapter 2", page: 8}
		wantHistory := History{URL: "/ch/2", LastRead: positionOnlyTestTime}
		if m.source != 123 || m.url != "/f" || len(m.chapters) != 1 || m.chapters[0] != wantChapter ||
			len(m.history) != 1 || m.history[0] != wantHistory {
			t.Fatalf("Mihon manga: %+v; want source 123, /f, chapter %+v and history %+v", m, wantChapter, wantHistory)
		}
	})
}

func TestExportExistingPageOnlyMarkWithHistory(t *testing.T) {
	db := exportDatabase(t)
	exportRecord(t, db, 1, 1, positionOnlyTestTime, "position", "", "",
		`{"sourceSlug":"moon","chapterUrl":"/ch/2","pageIndex":8}`)
	exportRecord(t, db, 1, 1, positionOnlyTestTime, "chapterMark", "moon", "/ch/2",
		`{"chapterName":"Two","lastPageRead":8}`)
	var out bytes.Buffer
	if err := Export(t.Context(), db, 1, false, "https://kit.example", "Kit", &out); err != nil {
		t.Fatal(err)
	}
	checkPositionOnlyRestore(t, db, out.Bytes(), []restoredPositionOnlyMark{{Page: 8}})
}

type restoredPositionOnlyMark struct {
	Page       int64 `json:"lastPageRead"`
	Read       bool  `json:"read"`
	Bookmarked bool  `json:"bookmarked"`
}

func checkPositionOnlyRestore(t *testing.T, db *sql.DB, raw []byte, wantMarks []restoredPositionOnlyMark) {
	t.Helper()
	f, err := Decode(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	svc := usersync.New(db)
	report, err := Restore(t.Context(), db, svc, 2, f, false, "https://kit.example")
	if err != nil || report.Titles.Matched != 1 || report.ChapterMarks.Restored != len(wantMarks) || report.ChapterMarks.Skipped != 0 {
		t.Errorf("restore report: %+v, %v; want one matched title and %d restored marks", report, err, len(wantMarks))
	}
	pull, err := svc.Pull(t.Context(), 2, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	var pos struct {
		URL  string `json:"chapterUrl"`
		Page int64  `json:"pageIndex"`
	}
	var marks []restoredPositionOnlyMark
	for _, change := range pull.Changes {
		switch change.Kind {
		case usersync.Position:
			if err := json.Unmarshal(change.Data, &pos); err != nil {
				t.Fatal(err)
			}
		case usersync.ChapterMark:
			var mark restoredPositionOnlyMark
			if err := json.Unmarshal(change.Data, &mark); err != nil {
				t.Fatal(err)
			}
			marks = append(marks, mark)
		}
	}
	if pos.URL != "/ch/2" || pos.Page != 8 {
		t.Errorf("restored position: %+v; want /ch/2 at page 8", pos)
	}
	if !reflect.DeepEqual(marks, wantMarks) {
		t.Errorf("restored marks: %+v; want %+v", marks, wantMarks)
	}
}
