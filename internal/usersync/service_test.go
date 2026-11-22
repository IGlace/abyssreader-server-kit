// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package usersync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

var testOperationSequence atomic.Int64

func testOpID() string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", testOperationSequence.Add(1))
}

func fav(t *testing.T, title int64, ts string, deleted bool) json.RawMessage {
	return raw(t, map[string]any{"opId": testOpID(), "kind": "favorite",
		"key": map[string]any{"mainMangaId": title}, "data": nil, "clientUpdatedAt": ts, "deleted": deleted})
}

func markItem(t *testing.T, title int64, url, ts string) json.RawMessage {
	return raw(t, map[string]any{"opId": testOpID(), "kind": "chapterMark",
		"key":  map[string]any{"mainMangaId": title, "sourceSlug": "paper-moon", "chapterUrl": url},
		"data": map[string]any{"read": true, "bookmarked": false}, "clientUpdatedAt": ts, "deleted": false})
}

func pullAll(t *testing.T, s *Service, user int64, cursor *string, limit int) ([]Change, string) {
	t.Helper()
	var all []Change
	for {
		p, err := s.Pull(context.Background(), user, cursor, limit)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Changes) > limit {
			t.Fatalf("a page of %d changes over the limit %d", len(p.Changes), limit)
		}
		all = append(all, p.Changes...)
		c := p.Cursor
		cursor = &c
		if !p.HasMore {
			return all, c
		}
	}
}

// Two devices of one account push at once: no record is lost and no sequence number is given
// twice (SQLite has one writer; each push is one write transaction).
func TestConcurrentPushesLoseNothing(t *testing.T) {
	db := testDB(t)
	user := addUser(t, db, "two-devices@example.com")
	addTitle(t, db, 1)
	addSource(t, db, "paper-moon")
	s := New(db)
	const devices, batches, perBatch = 4, 10, 25
	var wg sync.WaitGroup
	errs := make(chan error, devices*batches)
	for d := 0; d < devices; d++ {
		wg.Add(1)
		go func(d int) {
			defer wg.Done()
			for b := 0; b < batches; b++ {
				var items []json.RawMessage
				for i := 0; i < perBatch; i++ {
					items = append(items, markItem(t, 1, fmt.Sprintf("/c/%d/%d/%d", d, b, i), "2026-10-10T10:00:00.000Z"))
				}
				out, err := s.Push(context.Background(), user, items)
				if err != nil {
					errs <- err
					return
				}
				for _, r := range out.Results {
					if r.Status != "applied" {
						errs <- fmt.Errorf("device %d: %+v", d, r)
					}
				}
			}
		}(d)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	const total = devices * batches * perBatch
	all, _ := pullAll(t, s, user, nil, 97)
	if len(all) != total {
		t.Fatalf("snapshot has %d records, want %d", len(all), total)
	}
	zero := "d.0"
	delta, cursor := pullAll(t, s, user, &zero, 1000)
	if len(delta) != total || cursor != fmt.Sprintf("d.%d", total) {
		t.Fatalf("delta has %d records up to %s, want %d", len(delta), cursor, total)
	}
	var n, max int
	db.QueryRow(`SELECT count(DISTINCT seq), max(seq) FROM sync_records WHERE user_id = ?`, user).Scan(&n, &max)
	if n != total || max != total {
		t.Fatalf("%d distinct sequence numbers up to %d, want 1..%d", n, max, total)
	}
}

func TestOneAccountNeverSeesAnothersRecords(t *testing.T) {
	db := testDB(t)
	a, b := addUser(t, db, "a@example.com"), addUser(t, db, "b@example.com")
	addTitle(t, db, 1)
	s := New(db)
	s.Push(context.Background(), a, []json.RawMessage{fav(t, 1, "2026-10-10T10:00:00.000Z", false)})
	// b removes the same key: its own tombstone, a's record stays.
	s.Push(context.Background(), b, []json.RawMessage{fav(t, 1, "2026-10-10T11:00:00.000Z", true)})
	if got, _ := pullAll(t, s, b, nil, 10); len(got) != 0 {
		t.Fatalf("b's snapshot: %+v", got)
	}
	got, _ := pullAll(t, s, a, nil, 10)
	if len(got) != 1 || got[0].Deleted {
		t.Fatalf("a's snapshot: %+v", got)
	}
	st, _ := s.States(context.Background(), b, []int64{1})
	if st[1].Favorite {
		t.Fatal("b sees a's favorite")
	}
	// b's cursor is b's own sequence: a's writes never show up in it.
	zero := "d.0"
	if got, _ := pullAll(t, s, b, &zero, 10); len(got) != 1 || !got[0].Deleted {
		t.Fatalf("b's delta: %+v", got)
	}
}

func TestBadCursors(t *testing.T) {
	db := testDB(t)
	user := addUser(t, db, "cursor@example.com")
	s := New(db)
	for _, c := range []string{"", "x", "d.", "d.-1", "d.01", "d.1.2", "s.0", "s.0.x", "s.-1.0", "D.1", "d.99999999999999999999", "d. 1"} {
		if _, err := s.Pull(context.Background(), user, &c, 10); !errors.Is(err, ErrBadCursor) {
			t.Errorf("cursor %q: %v, want ErrBadCursor", c, err)
		}
	}
	// A cursor ahead of the account's sequence (a server restored from a backup) can no longer
	// be served: the app starts over.
	for _, c := range []string{"d.5", "s.5.0"} {
		if _, err := s.Pull(context.Background(), user, &c, 10); !errors.Is(err, ErrFullResync) {
			t.Errorf("cursor %q: %v, want ErrFullResync", c, err)
		}
	}
}

func TestPurgedTombstonesRaiseTheFloor(t *testing.T) {
	db := testDB(t)
	user := addUser(t, db, "purge@example.com")
	addTitle(t, db, 1)
	addTitle(t, db, 2)
	now := mustTime(t, "2026-10-10T10:00:00.000Z")
	s := New(db).WithClock(func() time.Time { return now })
	s.Push(context.Background(), user, []json.RawMessage{fav(t, 1, "2026-10-10T09:00:00.000Z", false), fav(t, 2, "2026-10-10T09:00:00.000Z", false)})
	s.Push(context.Background(), user, []json.RawMessage{fav(t, 1, "2026-10-10T09:30:00.000Z", true)}) // seq 3
	if n, err := s.PurgeTombstones(context.Background(), now); err != nil || n != 0 {
		t.Fatalf("purged %d %v before their time", n, err)
	}
	n, err := s.PurgeTombstones(context.Background(), now.Add(time.Millisecond))
	if err != nil || n != 1 {
		t.Fatalf("purged %d %v", n, err)
	}
	for _, c := range []string{"d.0", "d.2"} {
		if _, err := s.Pull(context.Background(), user, &c, 10); !errors.Is(err, ErrFullResync) {
			t.Errorf("cursor %s below the floor: %v", c, err)
		}
	}
	c := "d.3"
	if _, err := s.Pull(context.Background(), user, &c, 10); err != nil {
		t.Fatal(err)
	}
	if got, _ := pullAll(t, s, user, nil, 10); len(got) != 1 {
		t.Fatalf("snapshot after the purge: %+v", got)
	}
}

func TestRemovingATitleTombstonesItsRecords(t *testing.T) {
	db := testDB(t)
	a, b := addUser(t, db, "a@example.com"), addUser(t, db, "b@example.com")
	addTitle(t, db, 1)
	addTitle(t, db, 2)
	addSource(t, db, "paper-moon")
	now := mustTime(t, "2026-10-10T10:00:00.000Z")
	s := New(db).WithClock(func() time.Time { return now })
	// a's favorite is stamped later than the server's clock: the tombstone must still be newer.
	s.Push(context.Background(), a, []json.RawMessage{fav(t, 1, "2026-10-10T10:03:00.000Z", false), fav(t, 2, "2026-10-10T09:00:00.000Z", false),
		markItem(t, 1, "/c/1", "2026-10-10T09:00:00.000Z")})
	s.Push(context.Background(), b, []json.RawMessage{fav(t, 1, "2026-10-10T09:00:00.000Z", false)})
	cursorA, cursorB := "d.3", "d.1"

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	n, err := RemoveTitle(context.Background(), tx, 1, now)
	if err != nil || n != 3 {
		t.Fatalf("tombstoned %d %v", n, err)
	}
	if _, err := tx.Exec(`DELETE FROM titles WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	got, _ := pullAll(t, s, a, &cursorA, 10)
	if len(got) != 2 || !got[0].Deleted || !got[1].Deleted {
		t.Fatalf("a's delta after the removal: %+v", got)
	}
	for _, c := range got {
		if c.Kind == Favorite && c.ClientUpdatedAt != "2026-10-10T10:03:00.001Z" {
			t.Errorf("the favorite's tombstone is %s, want just after the record", c.ClientUpdatedAt)
		}
		if c.Kind == ChapterMark && c.ClientUpdatedAt != "2026-10-10T10:00:00.000Z" {
			t.Errorf("the mark's tombstone is %s, want the server's time", c.ClientUpdatedAt)
		}
	}
	if got, _ := pullAll(t, s, b, &cursorB, 10); len(got) != 1 || !got[0].Deleted {
		t.Fatalf("b's delta after the removal: %+v", got)
	}
	// A push naming the removed title is unknown_title; the other title is untouched.
	out, _ := s.Push(context.Background(), a, []json.RawMessage{fav(t, 1, "2026-10-11T09:00:00.000Z", false)})
	if out.Results[0].Status != "rejected" || out.Results[0].Reason != "unknown_title" {
		t.Fatalf("%+v", out)
	}
	if snap, _ := pullAll(t, s, a, nil, 10); len(snap) != 1 || snap[0].Key.MainMangaID != 2 {
		t.Fatalf("a's snapshot: %+v", snap)
	}
}

func TestPushForAGoneAccount(t *testing.T) {
	db := testDB(t)
	user := addUser(t, db, "gone@example.com")
	addTitle(t, db, 1)
	exec(t, db, `DELETE FROM users WHERE id = ?`, user)
	if _, err := New(db).Push(context.Background(), user, []json.RawMessage{fav(t, 1, "2026-10-10T10:00:00.000Z", false)}); !errors.Is(err, ErrNoAccount) {
		t.Fatalf("%v, want ErrNoAccount", err)
	}
}

func TestStates(t *testing.T) {
	db := testDB(t)
	user := addUser(t, db, "me@example.com")
	for id := int64(1); id <= 3; id++ {
		addTitle(t, db, id)
	}
	s := New(db)
	vote := func(title int64, opinion string, deleted bool) json.RawMessage {
		var data any = map[string]any{"opinion": opinion}
		if deleted {
			data = nil
		}
		return raw(t, map[string]any{"opId": testOpID(), "kind": "rating",
			"key": map[string]any{"mainMangaId": title}, "data": data, "clientUpdatedAt": "2026-10-10T10:00:00.000Z", "deleted": deleted})
	}
	list := func(kind string, title int64, deleted bool) json.RawMessage {
		return raw(t, map[string]any{"opId": testOpID(), "kind": kind,
			"key": map[string]any{"mainMangaId": title}, "data": nil, "clientUpdatedAt": "2026-10-10T10:00:00.000Z", "deleted": deleted})
	}
	s.Push(context.Background(), user, []json.RawMessage{list("favorite", 1, false), list("planning", 1, false), vote(1, "up", false),
		list("blacklist", 2, false), vote(2, "down", false), list("favorite", 3, true), vote(3, "", true)})
	st, err := s.States(context.Background(), user, []int64{1, 2, 3, 4})
	if err != nil {
		t.Fatal(err)
	}
	show := func(x State) string {
		v := "null"
		if x.Vote != nil {
			v = *x.Vote
		}
		return fmt.Sprintf("%v %v %v %s", x.Favorite, x.Planning, x.Blacklist, v)
	}
	for id, want := range map[int64]string{1: "true true false up", 2: "false false true down", 3: "false false false null", 4: "false false false null"} {
		if got := show(st[id]); got != want {
			t.Errorf("title %d: %s, want %s", id, got, want)
		}
	}
}

// A write that waits too long for the one writer is "busy", not a failure of its own.
func TestABusyDatabaseIsBusy(t *testing.T) {
	db := testDB(t)
	user := addUser(t, db, "busy@example.com")
	addTitle(t, db, 1)
	hold, err := db.Begin() // BEGIN IMMEDIATE: holds the write lock; the push waits the busy timeout (5 s)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback()
	_, err = New(db).Push(context.Background(), user, []json.RawMessage{fav(t, 1, "2026-10-10T10:00:00.000Z", false)})
	if !store.IsBusy(err) {
		t.Fatalf("%v, want busy", err)
	}
	if store.IsBusy(errors.New("other")) || store.IsBusy(nil) {
		t.Fatal("not busy")
	}
}
