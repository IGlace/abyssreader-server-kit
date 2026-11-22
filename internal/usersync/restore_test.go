// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package usersync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

const early = "2026-01-01T09:00:00.000Z"
const late = "2026-01-01T10:00:00.000Z"

func restoreFixture(t *testing.T) (*Service, int64, *sql.DB) {
	t.Helper()
	db := testDB(t)
	user := addUser(t, db, "restore@example.com")
	addSource(t, db, "paper-moon")
	for id := int64(1); id <= 3; id++ {
		addTitle(t, db, id)
		exec(t, db, `INSERT INTO source_titles (title_id,source_id,url,lang,position)
			SELECT ?,id,?,'en',0 FROM sources WHERE slug='paper-moon'`, id, fmt.Sprint("/title/", id))
	}
	s := New(db).WithClock(func() time.Time { return mustTime(t, late) })
	return s, user, db
}

func checkRestore(t *testing.T, s *Service, user int64, hosted bool, changes []Change, want ...string) {
	t.Helper()
	results, err := s.RestoreBatch(context.Background(), user, changes, hosted)
	if err != nil || len(results) != len(want) {
		t.Fatalf("restore results %v, err %v", results, err)
	}
	for i, r := range results {
		got := r.Status
		if r.Reason != "" {
			got += ":" + r.Reason
		}
		if got != want[i] {
			t.Errorf("result %d = %s, want %s", i, got, want[i])
		}
	}
}

func TestRestoreMergesChapterFlagsUnderEitherURLSpelling(t *testing.T) {
	s, user, _ := restoreFixture(t)
	key := Key{MainMangaID: 1, SourceSlug: "paper-moon", ChapterURL: "/c/1/?p=1"}
	if got := push1(t, s, user, markItem(t, 1, key.ChapterURL, late)); got != "applied" {
		t.Fatal(got)
	}
	key.ChapterURL = "/c/1?p=1"
	mark := Change{Kind: ChapterMark, Key: key, Data: json.RawMessage(`{"read":false,"bookmarked":true,"lastPageRead":7}`), ClientUpdatedAt: early}
	checkRestore(t, s, user, false, []Change{mark, mark}, "applied", "stale")
	rows, cursor := pullAll(t, s, user, nil, 10)
	if len(rows) != 1 || rows[0].Key.ChapterURL != "/c/1/?p=1" || rows[0].ClientUpdatedAt != "2026-01-01T10:00:00.001Z" || cursor != "d.2" {
		t.Fatalf("old file merged as %+v at %s", rows, cursor)
	}
	var d map[string]any
	if err := json.Unmarshal(rows[0].Data, &d); err != nil || d["read"] != true || d["bookmarked"] != true || d["lastPageRead"] != nil {
		t.Fatalf("merged fields: %s", rows[0].Data)
	}
	mark.ClientUpdatedAt = "2026-01-01T10:01:00.000Z"
	checkRestore(t, s, user, false, []Change{mark, mark}, "applied", "stale")
	rows, _ = pullAll(t, s, user, nil, 10)
	if err := json.Unmarshal(rows[0].Data, &d); err != nil || d["read"] != true || d["bookmarked"] != true || d["lastPageRead"] != 7.0 {
		t.Fatalf("newer file: %s", rows[0].Data)
	}
}

func TestRestoreTombstonesCapsAndReplays(t *testing.T) {
	s, user, _ := restoreFixture(t)
	s.caps[Favorite] = 1
	s.maxStored = 2
	list := Change{Kind: Favorite, Key: Key{MainMangaID: 1}, ClientUpdatedAt: early}
	if got := push1(t, s, user, fav(t, 1, late, true)); got != "applied" {
		t.Fatal(got)
	}
	checkRestore(t, s, user, false, []Change{list}, "stale")
	list.ClientUpdatedAt = "2026-01-01T10:01:00Z"
	capped := list
	capped.Key.MainMangaID = 2
	vote := Change{Kind: Rating, Key: Key{MainMangaID: 2}, Data: json.RawMessage(`{"opinion":"up"}`)}
	extra := Change{Kind: Planning, Key: Key{MainMangaID: 3}}
	checkRestore(t, s, user, false, []Change{list, capped, vote, extra}, "applied", "rejected:cap", "applied", "rejected:cap")
	s.WithClock(func() time.Time { return mustTime(t, late).Add(time.Hour) })
	list.ClientUpdatedAt = ""
	checkRestore(t, s, user, false, []Change{list, vote}, "stale", "stale")
	vote.Data = json.RawMessage(`{"opinion":"down"}`)
	vote.ClientUpdatedAt = early
	checkRestore(t, s, user, false, []Change{vote}, "stale")
	vote.ClientUpdatedAt = "2099-01-01T00:00:00.000Z"
	checkRestore(t, s, user, false, []Change{vote}, "applied")
	rows, cursor := pullAll(t, s, user, nil, 10)
	if len(rows) != 2 || cursor != "d.4" {
		t.Fatalf("records %v cursor %s", rows, cursor)
	}
	for _, row := range rows {
		if row.Kind == Rating && row.ClientUpdatedAt != "2026-01-01T11:00:00.000Z" {
			t.Fatalf("future time %s", row.ClientUpdatedAt)
		}
	}
}

func TestRestoreRechecksCatalogUnderWriterLock(t *testing.T) {
	s, user, db := restoreFixture(t)
	list := Change{Kind: Favorite, Key: Key{MainMangaID: 1}}
	mark := Change{Kind: ChapterMark, Key: Key{MainMangaID: 1, SourceSlug: "paper-moon", ChapterURL: "/c/1"}, Data: json.RawMessage(`{"read":true,"bookmarked":false}`)}
	exec(t, db, `UPDATE sources SET hosted=1 WHERE slug='paper-moon'`)
	checkRestore(t, s, user, false, []Change{list, mark}, "rejected:unknown_title", "rejected:unknown_title")
	checkRestore(t, s, user, true, []Change{list}, "applied")
	addSource(t, db, "sibling")
	exec(t, db, `INSERT INTO source_titles(title_id,source_id,url,lang,position) SELECT 1,id,'/t','en',0 FROM sources WHERE slug='sibling'`)
	checkRestore(t, s, user, false, []Change{mark}, "rejected:unknown_source")
	exec(t, db, `DELETE FROM source_titles WHERE source_id=(SELECT id FROM sources WHERE slug='paper-moon')`)
	checkRestore(t, s, user, true, []Change{mark}, "rejected:unknown_source")
	exec(t, db, `DELETE FROM titles WHERE id=1`)
	checkRestore(t, s, user, true, []Change{list}, "rejected:unknown_title")
}

func TestRestoreRejectsRemovalsAndOversizeBatches(t *testing.T) {
	s, user, _ := restoreFixture(t)
	list := Change{Kind: Favorite, Key: Key{MainMangaID: 1}, Deleted: true}
	checkRestore(t, s, user, true, []Change{list, {Kind: Inbox, Key: list.Key}}, "rejected:invalid", "rejected:invalid")
	if _, err := s.RestoreBatch(context.Background(), user, make([]Change, MaxBatch+1), false); err == nil {
		t.Fatal("oversize batch accepted")
	}
}

func TestRestoreFollowsAbsoluteAliasesAndConcurrentPushes(t *testing.T) {
	s, user, _ := restoreFixture(t)
	abs1 := "https://old.mirror.example/c/1/?lang=en#page"
	abs2 := "https://new.example/c/2?lang=en"
	if got := push1(t, s, user, markItem(t, 1, abs1, late)); got != "applied" {
		t.Fatal(got)
	}
	writer := s.NewRestoreWriter(user, false)
	mark := Change{Kind: ChapterMark, Key: Key{MainMangaID: 1, SourceSlug: "paper-moon", ChapterURL: "/c/1?lang=en"}, Data: json.RawMessage(`{"read":true,"bookmarked":false}`), ClientUpdatedAt: early}
	run := func(want string) {
		t.Helper()
		out, err := writer.RestoreBatch(context.Background(), []Change{mark})
		if err != nil || len(out) != 1 || out[0].Status != want {
			t.Fatalf("restoring %q = %v %v", mark.Key.ChapterURL, out, err)
		}
	}
	run("stale")
	mark.Key.ChapterURL = "/c/2/?lang=en"
	if got := push1(t, s, user, removal(t, "chapterMark", markKey(abs2), late)); got != "applied" {
		t.Fatal(got)
	}
	run("stale")
	mark.ClientUpdatedAt = "2026-01-01T10:01:00.000Z"
	run("applied")
	rows, cursor := pullAll(t, s, user, nil, 10)
	if len(rows) != 2 || cursor != "d.3" {
		t.Fatalf("spellings duplicated: %v %s", rows, cursor)
	}
	for _, row := range rows {
		if row.Key.ChapterURL != abs1 && row.Key.ChapterURL != abs2 {
			t.Fatalf("new key %q", row.Key.ChapterURL)
		}
	}
}

func TestRestoreEqualDataWithDifferentJSONEscapingKeepsCursor(t *testing.T) {
	s, user, _ := restoreFixture(t)
	item := json.RawMessage(`{"opId":"00000000-0000-4000-8000-000000000010","kind":"position","key":{"mainMangaId":1},"clientUpdatedAt":"2026-01-01T09:00:00.000Z","deleted":false,"data":{"sourceSlug":"paper-moon","chapterUrl":"/c/1","chapterName":"Vol. \u0031"}}`)
	if got := push1(t, s, user, item); got != "applied" {
		t.Fatal(got)
	}
	pos := Change{Kind: Position, Key: Key{MainMangaID: 1}, Data: json.RawMessage(`{"sourceSlug":"paper-moon","chapterUrl":"/c/1","chapterName":"Vol. 1"}`), ClientUpdatedAt: late}
	checkRestore(t, s, user, false, []Change{pos}, "stale")
	_, cursor := pullAll(t, s, user, nil, 10)
	if cursor != "d.1" {
		t.Fatalf("cursor %s after replay of the same position", cursor)
	}
}
