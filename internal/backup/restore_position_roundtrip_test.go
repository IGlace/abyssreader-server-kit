// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

func TestRestoreOwnExportKeepsPositionAndMarkPages(t *testing.T) {
	db, svc := seedDistinctPositionAndMark(t)
	file := runExport(t, db, false)
	if _, err := Restore(t.Context(), db, svc, 1, file, false, "https://kit.example"); err != nil {
		t.Fatal(err)
	}
	checkRestorePages(t, svc, 6, 11)
}

func TestRestoreNewerMihonHistoryAdvancesPosition(t *testing.T) {
	db, svc := seedDistinctPositionAndMark(t)
	var wire bytes.Buffer
	entry := Entry{
		Source: 123,
		URL:    "/f",
		Title:  "Ferry",
		History: &History{
			URL:      "/chapter-54/",
			LastRead: time.Date(2026, 1, 2, 0, 0, 1, 123*int(time.Millisecond), time.UTC).UnixMilli(),
		},
		Chapters: []Chapter{{URL: "/chapter-54/", Name: "Chapter 54", Number: 54, Read: true, Page: 17, Modified: 1767312000}},
	}
	if err := WriteEntry(&wire, entry); err != nil {
		t.Fatal(err)
	}
	file, err := Decode(&wire, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Restore(t.Context(), db, svc, 1, file, false, "https://kit.example"); err != nil {
		t.Fatal(err)
	}
	// The newer history can advance the position. The older chapter entry
	// still cannot overwrite the existing mark.
	checkRestorePages(t, svc, 17, 11)
}

func seedDistinctPositionAndMark(t *testing.T) (*sql.DB, *usersync.Service) {
	t.Helper()
	db := exportDatabase(t)
	svc := usersync.New(db)
	items := []json.RawMessage{
		json.RawMessage(`{
			"opId":"3f2d5598-443b-4553-b79f-317b242402f8","kind":"position","key":{"mainMangaId":1},
			"data":{"sourceSlug":"moon","chapterUrl":"/chapter-54/","chapterNumber":54,"chapterName":"Chapter 54","pageIndex":6},
			"clientUpdatedAt":"2026-01-02T00:00:00.123Z","deleted":false
		}`),
		json.RawMessage(`{
			"opId":"6b76db17-8051-4f90-8c8e-fe14eac31213","kind":"chapterMark",
			"key":{"mainMangaId":1,"sourceSlug":"moon","chapterUrl":"/chapter-54/"},
			"data":{"chapterNumber":54,"chapterName":"Chapter 54","read":true,"bookmarked":false,"lastPageRead":11,"totalPages":30},
			"clientUpdatedAt":"2026-01-02T00:00:00.123Z","deleted":false
		}`),
	}
	pushed, err := svc.Push(t.Context(), 1, items)
	if err != nil || len(pushed.Results) != 2 || pushed.Results[0].Status != "applied" || pushed.Results[1].Status != "applied" {
		t.Fatalf("seed sync records: %+v, %v", pushed, err)
	}
	checkRestorePages(t, svc, 6, 11)
	return db, svc
}

func checkRestorePages(t *testing.T, svc *usersync.Service, wantPosition, wantMark int) {
	t.Helper()
	pull, err := svc.Pull(t.Context(), 1, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	var position, mark int
	for _, change := range pull.Changes {
		var data struct {
			PageIndex    int `json:"pageIndex"`
			LastPageRead int `json:"lastPageRead"`
		}
		if err := json.Unmarshal(change.Data, &data); err != nil {
			t.Fatal(err)
		}
		switch change.Kind {
		case usersync.Position:
			position = data.PageIndex
		case usersync.ChapterMark:
			mark = data.LastPageRead
		}
	}
	if position != wantPosition || mark != wantMark {
		t.Fatalf("position page %d and mark page %d, want %d and %d", position, mark, wantPosition, wantMark)
	}
}
