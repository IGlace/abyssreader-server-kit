// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

// The conflicting votes and pages straddle a transaction boundary. The second
// restore must neither flip the stored values nor advance the sync cursor.
func TestRestoreConflictingUntimedDuplicatesTwice(t *testing.T) {
	db := matchCatalog(t)
	_, err := db.Exec(`INSERT INTO users(id,email,email_key,display_name,created_at,updated_at) VALUES(1,'dups@x.test','dups@x.test','reader',0,0)`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc := usersync.New(db).WithClock(func() time.Time { return now })
	file := func() File {
		first := Entry{Source: 77, URL: "/story?a=1", Extra: &Extra{Vote: "up"}, Chapters: []Chapter{{URL: "/conflict/?q=1", Read: true, Page: 3}}}
		for i := 0; i < usersync.MaxBatch; i++ {
			first.Chapters = append(first.Chapters, Chapter{URL: fmt.Sprintf("/filler/%d", i), Read: true})
		}
		second := Entry{Source: 77, URL: "/story?a=1", Extra: &Extra{Vote: "down"}, Chapters: []Chapter{{URL: "https://mirror.test/conflict?q=1#page", Bookmark: true, Page: 9}}}
		return File{Entries: []Entry{first, second}}
	}
	var cursor string
	for run := 0; run < 2; run++ {
		f := file()
		r, err := restore(t.Context(), db, &f, false, "", now, 2000, svc.NewRestoreWriter(1, false).RestoreBatch)
		if err != nil {
			t.Fatal(err)
		}
		if r.Titles.Matched != 2 {
			t.Fatalf("restore %d title report %+v", run, r)
		}
		if run == 1 && r.ChapterMarks.Restored != 0 {
			t.Errorf("replay wrote %d chapter marks", r.ChapterMarks.Restored)
		}
		page, err := svc.Pull(t.Context(), 1, nil, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if run == 1 && page.Cursor != cursor {
			t.Errorf("replay moved cursor from %s to %s", cursor, page.Cursor)
		}
		cursor = page.Cursor
		checked := 0
		for _, c := range page.Changes {
			var d map[string]any
			if c.Kind == usersync.Rating || usersync.ChapterPathKey(c.Key.ChapterURL) == "/conflict?q=1" {
				if err := json.Unmarshal(c.Data, &d); err != nil {
					t.Fatal(err)
				}
				checked++
				if c.Kind == usersync.Rating && d["opinion"] != "up" {
					t.Errorf("restore %d vote %s", run, c.Data)
				}
				if c.Kind == usersync.ChapterMark && (d["lastPageRead"] != 3.0 || d["read"] != true || d["bookmarked"] != true) {
					t.Errorf("restore %d mark %s", run, c.Data)
				}
			}
		}
		if checked != 2 {
			t.Fatalf("checked %d conflicting records", checked)
		}
		now = now.Add(24 * time.Hour)
	}
}

func TestDuplicateTimesAndPositionTiesUseSyncRules(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	entries := []Entry{
		{Extra: &Extra{Vote: "down"}, Chapters: []Chapter{{URL: "/1", Page: 3, Number: 1}}, History: &History{URL: "/1", LastRead: now.UnixMilli()}},
		{Extra: &Extra{Vote: "up"}, Modified: now.Add(time.Minute).Unix(), Chapters: []Chapter{{URL: "/2", Page: 1, Number: 2}}, History: &History{URL: "/2", LastRead: now.UnixMilli()}},
		{Extra: &Extra{Vote: "down"}, Modified: now.Add(24 * time.Hour).Unix(), Chapters: []Chapter{{URL: "/3", Page: 4, Number: 2}}, History: &History{URL: "/3", LastRead: now.UnixMilli()}},
	}
	matches := []match{{title: 1, slug: "paper"}, {title: 1, slug: "paper"}, {title: 1, slug: "paper"}}
	p := duplicatePlanner(t.Context(), entries, matches, now)
	for i := range entries {
		err := p(&entries[i], matches[i], now, func(c usersync.Change) error {
			var d map[string]any
			if err := json.Unmarshal(c.Data, &d); err != nil {
				t.Fatal(err)
			}
			switch c.Kind {
			case usersync.Rating:
				if d["opinion"] != "up" || c.ClientUpdatedAt != "2026-01-01T00:01:00.000Z" {
					t.Errorf("vote %+v", c)
				}
			case usersync.Position:
				if d["chapterUrl"] != "/3" || d["pageIndex"] != 4.0 {
					t.Errorf("position %+v", c)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
