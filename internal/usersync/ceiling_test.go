// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package usersync

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func removal(t *testing.T, kind string, key map[string]any, ts string) json.RawMessage {
	return raw(t, map[string]any{"opId": testOpID(), "kind": kind, "key": key,
		"data": nil, "clientUpdatedAt": ts, "deleted": true})
}

func markKey(url string) map[string]any {
	return map[string]any{"mainMangaId": 1, "sourceSlug": "paper-moon", "chapterUrl": url}
}

func push1(t *testing.T, s *Service, user int64, item json.RawMessage) string {
	t.Helper()
	out, err := s.Push(context.Background(), user, []json.RawMessage{item})
	if err != nil {
		t.Fatal(err)
	}
	r := out.Results[0]
	if r.Reason != "" {
		return r.Status + ":" + r.Reason
	}
	return r.Status
}

// Every stored row counts toward the account's ceiling, removals of keys it never had included:
// one account cannot grow the table without end. What does not grow it always passes.
func TestTheCeilingCountsRemovals(t *testing.T) {
	db := testDB(t)
	user := addUser(t, db, "ceiling@example.com")
	other := addUser(t, db, "other@example.com")
	addTitle(t, db, 1)
	addTitle(t, db, 2)
	addSource(t, db, "paper-moon")
	s := New(db)
	s.maxStored = 3
	if got := push1(t, s, user, markItem(t, 1, "/c/live", "2026-10-10T10:00:00.000Z")); got != "applied" {
		t.Fatal(got)
	}
	for i := range 2 {
		if got := push1(t, s, user, removal(t, "chapterMark", markKey(fmt.Sprint("/c/gone/", i)), "2026-10-10T10:00:00.000Z")); got != "applied" {
			t.Fatalf("removal %d: %s", i, got)
		}
	}
	// At the ceiling: neither a removal of an unknown key nor a new record is stored.
	if got := push1(t, s, user, removal(t, "chapterMark", markKey("/c/gone/3"), "2026-10-10T10:00:00.000Z")); got != "rejected:cap" {
		t.Fatalf("a removal over the ceiling: %s", got)
	}
	if got := push1(t, s, user, fav(t, 2, "2026-10-10T10:00:00.000Z", false)); got != "rejected:cap" {
		t.Fatalf("a new record over the ceiling: %s", got)
	}
	// What replaces a stored row passes: an update, a removal of a stored record, a re-add.
	if got := push1(t, s, user, markItem(t, 1, "/c/live", "2026-10-10T11:00:00.000Z")); got != "applied" {
		t.Fatalf("an update at the ceiling: %s", got)
	}
	if got := push1(t, s, user, removal(t, "chapterMark", markKey("/c/live"), "2026-10-10T12:00:00.000Z")); got != "applied" {
		t.Fatalf("a removal of a stored record at the ceiling: %s", got)
	}
	if got := push1(t, s, user, markItem(t, 1, "/c/gone/0", "2026-10-10T12:00:00.000Z")); got != "applied" {
		t.Fatalf("a re-add over a tombstone at the ceiling: %s", got)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM sync_records WHERE user_id = ?`, user).Scan(&n)
	if n != 3 {
		t.Fatalf("%d rows stored, want the ceiling 3", n)
	}
	// The ceiling is per account.
	if got := push1(t, s, other, fav(t, 2, "2026-10-10T10:00:00.000Z", false)); got != "applied" {
		t.Fatalf("another account: %s", got)
	}
}

func TestTheDefaultCeiling(t *testing.T) {
	sum := 0
	for _, n := range defaultCaps {
		sum += n
	}
	if got := New(testDB(t)).maxStored; got != maxStoredRows || maxStoredRows < sum {
		t.Fatalf("ceiling %d (default %d), the live caps add up to %d", got, maxStoredRows, sum)
	}
}

func TestThePurgeRunsInShortBatches(t *testing.T) {
	db := testDB(t)
	a, b := addUser(t, db, "a@example.com"), addUser(t, db, "b@example.com")
	addTitle(t, db, 1)
	addSource(t, db, "paper-moon")
	now := mustTime(t, "2026-10-10T10:00:00.000Z")
	s := New(db).WithClock(func() time.Time { return now })
	for i := range 3 {
		push1(t, s, a, removal(t, "chapterMark", markKey(fmt.Sprint("/a/", i)), "2026-10-10T09:00:00.000Z"))
	}
	for i := range 2 {
		push1(t, s, b, removal(t, "chapterMark", markKey(fmt.Sprint("/b/", i)), "2026-10-10T09:00:00.000Z"))
	}
	s.purgeBatch = 2
	var batches []int64
	s.purged = func(n int64) { batches = append(batches, n) }
	n, err := s.PurgeTombstones(context.Background(), now.Add(time.Millisecond))
	if err != nil || n != 5 || !slices.Equal(batches, []int64{2, 2, 1}) {
		t.Fatalf("purged %d %v in batches %v", n, err, batches)
	}
	for user, floor := range map[int64]int64{a: 3, b: 2} {
		var got int64
		db.QueryRow(`SELECT floor_seq FROM sync_accounts WHERE user_id = ?`, user).Scan(&got)
		if got != floor {
			t.Errorf("account %d floor %d, want %d", user, got, floor)
		}
	}
}

// The unique key holds a 32-byte hash of a chapter URL, never the URL itself (up to 2048
// characters, stored once in its own column).
func TestTheChapterURLIsHashedInTheKey(t *testing.T) {
	db := testDB(t)
	rows, err := db.Query(`SELECT il.name, ii.name FROM pragma_index_list('sync_records') il JOIN pragma_index_info(il.name) ii`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var index, column string
		rows.Scan(&index, &column)
		if column == "chapter_url" {
			t.Errorf("index %s holds the full chapter_url", index)
		}
	}
	rows.Close()
	user := addUser(t, db, "hash@example.com")
	addTitle(t, db, 1)
	addSource(t, db, "paper-moon")
	s := New(db)
	long := "/" + strings.Repeat("x", 2046)
	for _, u := range []string{long, long[:2047] + "y"} {
		if got := push1(t, s, user, markItem(t, 1, u, "2026-10-10T10:00:00.000Z")); got != "applied" {
			t.Fatal(got)
		}
	}
	var n, hashLen int
	db.QueryRow(`SELECT count(*), max(length(url_hash)) FROM sync_records WHERE user_id = ?`, user).Scan(&n, &hashLen)
	if n != 2 || hashLen != 32 {
		t.Fatalf("%d marks, hash of %d bytes", n, hashLen)
	}
	if got := storedChange(t, s, user, map[string]any{"mainMangaId": 1.0, "sourceSlug": "paper-moon", "chapterUrl": long}, "chapterMark"); got == nil {
		t.Fatal("the long URL does not round-trip")
	}
}
