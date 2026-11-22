// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package usersync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

// The sync contract's own fixtures (contract/sync, shared with the official server and the app)
// run against this implementation: the same pushes give the same answers.

func syncDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "contract", "sync")
}

func fixtures(t *testing.T, folder string) map[string]map[string]any {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(syncDir(), folder, "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures in %s: %v", folder, err)
	}
	out := map[string]map[string]any{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		out[strings.TrimSuffix(filepath.Base(p), ".json")] = m
	}
	return out
}

// testDB is an empty, migrated database.
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "abyssreader.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func exec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// addUser makes an account row and returns its id.
func addUser(t *testing.T, db *sql.DB, email string) int64 {
	t.Helper()
	r, err := db.Exec(`INSERT INTO users (email, email_key, display_name, created_at, updated_at) VALUES (?, ?, 'reader', 0, 0)`, email, email)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r.LastInsertId()
	return id
}

func addTitle(t *testing.T, db *sql.DB, id int64) {
	t.Helper()
	exec(t, db, `INSERT INTO titles (id, ref, slug, name, name_key, status, manifest_hash, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'unknown', '', 0, 0)`, id, fmt.Sprint("t", id), fmt.Sprint("t-", id), fmt.Sprint("T", id), fmt.Sprint("t", id))
}

func addSource(t *testing.T, db *sql.DB, slug string) {
	t.Helper()
	exec(t, db, `INSERT INTO sources (slug, name, website, lang, created_at, updated_at) VALUES (?, ?, 'https://src.example.org', 'en', 0, 0)`, slug, slug)
}

// seedFor adds every title and source a fixture item names.
func seedFor(t *testing.T, db *sql.DB, withTitles bool, items ...any) {
	t.Helper()
	titles, sources := map[int64]bool{}, map[string]bool{}
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if k, ok := m["key"].(map[string]any); ok {
			if id, ok := k["mainMangaId"].(float64); ok && id >= 1 && id <= 2147483647 {
				titles[int64(id)] = true
			}
			if s, ok := k["sourceSlug"].(string); ok && s != "no-such-source" {
				sources[s] = true
			}
		}
		if d, ok := m["data"].(map[string]any); ok {
			if s, ok := d["sourceSlug"].(string); ok && s != "no-such-source" {
				sources[s] = true
			}
		}
	}
	for id := range titles {
		if withTitles {
			addTitle(t, db, id)
		}
	}
	for s := range sources {
		addSource(t, db, s)
	}
}

func raw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(timestampLayout, s)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// record finds the stored record (live or tombstone) for a key, as a pull change, from a full
// delta pull; nil when there is none.
func storedChange(t *testing.T, s *Service, userID int64, key any, kind string) map[string]any {
	t.Helper()
	cursor := "d.0"
	var found map[string]any
	for {
		p, err := s.Pull(context.Background(), userID, &cursor, 1000)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range p.Changes {
			var m map[string]any
			json.Unmarshal(raw(t, c), &m)
			if m["kind"] == kind && reflect.DeepEqual(m["key"], key) {
				found = m
			}
		}
		cursor = p.Cursor
		if !p.HasMore {
			return found
		}
	}
}

func TestRuleFixtures(t *testing.T) {
	for name, fx := range fixtures(t, "rules") {
		t.Run(name, func(t *testing.T) {
			db := testDB(t)
			user := addUser(t, db, "rules@example.com")
			incoming := fx["incoming"].(map[string]any)
			seedFor(t, db, true, incoming, fx["stored"])
			now := mustTime(t, fx["serverNow"].(string))
			s := New(db).WithClock(func() time.Time { return now })
			kind := incoming["kind"].(string)

			// The stored record, pushed first (a tombstone as a removal).
			if st, ok := fx["stored"].(map[string]any); ok {
				item := map[string]any{"opId": testOpID()}
				for k, v := range st {
					item[k] = v
				}
				out, err := s.Push(context.Background(), user, []json.RawMessage{raw(t, item)})
				if err != nil || out.Results[0].Status != "applied" {
					t.Fatalf("seeding the stored record: %+v %v", out, err)
				}
			}
			// userCounts: the account already holds that many records of a kind. The cap is
			// lowered by the same amount, over the records actually stored (2).
			if counts, ok := fx["userCounts"].(map[string]any); ok {
				for k, n := range counts {
					have := 0
					if st, ok := fx["stored"].(map[string]any); ok && st["kind"] == k && st["deleted"] == false {
						have = 1
					}
					for i := have; i < 2; i++ {
						filler := map[string]any{"opId": testOpID(), "kind": k,
							"clientUpdatedAt": "2026-01-01T00:00:00.000Z", "deleted": false}
						switch k {
						case "chapterMark":
							filler["key"] = map[string]any{"mainMangaId": incoming["key"].(map[string]any)["mainMangaId"],
								"sourceSlug": "filler", "chapterUrl": fmt.Sprintf("/filler/%d", i)}
							filler["data"] = map[string]any{"read": true, "bookmarked": false}
						default:
							addTitle(t, db, int64(900000+i))
							filler["key"] = map[string]any{"mainMangaId": 900000 + i}
							filler["data"] = nil
						}
						if k == "chapterMark" && i == have {
							addSource(t, db, "filler")
						}
						out, err := s.Push(context.Background(), user, []json.RawMessage{raw(t, filler)})
						if err != nil || out.Results[0].Status != "applied" {
							t.Fatalf("filler: %+v %v", out, err)
						}
					}
					s.caps[Kind(k)] = defaultCaps[Kind(k)] - int(n.(float64)) + 2
				}
			}

			out, err := s.Push(context.Background(), user, []json.RawMessage{raw(t, incoming)})
			if err != nil {
				t.Fatal(err)
			}
			want := fx["expected"].(map[string]any)
			got := out.Results[0]
			if got.Status != want["status"] || got.Reason != fmt.Sprint(orEmpty(want["reason"])) {
				t.Fatalf("got %+v, want %v", got, want)
			}
			after := storedChange(t, s, user, incoming["key"], kind)
			if want["storedAfter"] == nil {
				if after != nil {
					t.Fatalf("stored after: %v, want nothing", after)
				}
				return
			}
			if !reflect.DeepEqual(after, want["storedAfter"]) {
				t.Fatalf("stored after:\n got %v\nwant %v", after, want["storedAfter"])
			}
		})
	}
}

func orEmpty(v any) any {
	if v == nil {
		return ""
	}
	return v
}

func TestInvalidFixturesAreRejectedWithTheirReason(t *testing.T) {
	for name, fx := range fixtures(t, "invalid") {
		t.Run(name, func(t *testing.T) {
			db := testDB(t)
			user := addUser(t, db, "invalid@example.com")
			seedFor(t, db, fx["reason"] != "unknown_title", fx["item"])
			// A good item beside it: one bad item never fails the batch.
			addTitle(t, db, 77)
			good := map[string]any{"opId": testOpID(), "kind": "favorite", "key": map[string]any{"mainMangaId": 77},
				"data": nil, "clientUpdatedAt": "2026-10-10T14:00:00.000Z", "deleted": false}
			out, err := New(db).Push(context.Background(), user, []json.RawMessage{raw(t, fx["item"]), raw(t, good)})
			if err != nil {
				t.Fatal(err)
			}
			if r := out.Results[0]; r.Status != "rejected" || r.Reason != fx["reason"] {
				t.Fatalf("got %+v, want rejected %v", r, fx["reason"])
			}
			if r := out.Results[1]; r.Status != "applied" {
				t.Fatalf("the good item: %+v", r)
			}
		})
	}
}

func TestPushRequestFixturesApplyEveryItem(t *testing.T) {
	for name, fx := range fixtures(t, "push") {
		items, ok := fx["items"].([]any)
		if !ok {
			continue // the response fixture
		}
		t.Run(name, func(t *testing.T) {
			db := testDB(t)
			user := addUser(t, db, "push@example.com")
			seedFor(t, db, true, items...)
			var in []json.RawMessage
			for _, it := range items {
				in = append(in, raw(t, it))
			}
			s := New(db).WithClock(func() time.Time { return mustTime(t, "2026-11-01T00:00:00.000Z") })
			out, err := s.Push(context.Background(), user, in)
			if err != nil {
				t.Fatal(err)
			}
			keys := map[string]int{}
			for i, r := range out.Results {
				it := items[i].(map[string]any)
				if r.OpID != it["opId"] {
					t.Errorf("result %d answers %q, want %q", i, r.OpID, it["opId"])
				}
				// A later item on the same key may be stale against an earlier one.
				k := it["kind"].(string) + fmt.Sprint(it["key"])
				if keys[k]++; keys[k] == 1 && r.Status != "applied" {
					t.Errorf("item %d (%s): %+v", i, it["kind"], r)
				}
			}
			// Every item round-trips: the pulled record equals the pushed one (data normalised).
			for i, it := range items {
				m := it.(map[string]any)
				k := m["kind"].(string) + fmt.Sprint(m["key"])
				if keys[k] > 1 || out.Results[i].Status != "applied" {
					continue
				}
				got := storedChange(t, s, user, m["key"], m["kind"].(string))
				want := map[string]any{"kind": m["kind"], "key": m["key"], "data": normalised(m), "clientUpdatedAt": m["clientUpdatedAt"], "deleted": m["deleted"]}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("item %d:\n got %v\nwant %v", i, got, want)
				}
			}
		})
	}
}

// normalised is a pushed item's data as a pull gives it back: every field of its kind, null
// when it was absent.
func normalised(item map[string]any) any {
	if item["deleted"] == true || item["data"] == nil {
		return nil
	}
	d := item["data"].(map[string]any)
	out := map[string]any{}
	for _, f := range dataFields[Kind(item["kind"].(string))] {
		out[f] = d[f]
	}
	return out
}

// A key stays the same record whatever else changes: a chapter mark's title is not part of it.
func TestAChapterMarkIsKeyedBySourceAndURL(t *testing.T) {
	db := testDB(t)
	user := addUser(t, db, "mark@example.com")
	addTitle(t, db, 1)
	addTitle(t, db, 2)
	addSource(t, db, "paper-moon")
	s := New(db)
	mark := func(title int, ts string) json.RawMessage {
		return raw(t, map[string]any{"opId": testOpID(), "kind": "chapterMark",
			"key":  map[string]any{"mainMangaId": title, "sourceSlug": "paper-moon", "chapterUrl": "/c/1"},
			"data": map[string]any{"read": true, "bookmarked": false}, "clientUpdatedAt": ts, "deleted": false})
	}
	s.Push(context.Background(), user, []json.RawMessage{mark(1, "2026-10-10T10:00:00.000Z")})
	out, _ := s.Push(context.Background(), user, []json.RawMessage{mark(2, "2026-10-10T11:00:00.000Z")})
	if out.Results[0].Status != "applied" {
		t.Fatalf("%+v", out)
	}
	var n int
	db.QueryRow(`SELECT count(*) FROM sync_records WHERE user_id = ? AND kind = 'chapterMark'`, user).Scan(&n)
	got := storedChange(t, s, user, map[string]any{"mainMangaId": 2.0, "sourceSlug": "paper-moon", "chapterUrl": "/c/1"}, "chapterMark")
	if n != 1 || got == nil {
		t.Fatalf("%d marks, re-pointed %v", n, got)
	}
}

func TestDefaultCapsAreTheContractsCaps(t *testing.T) {
	for _, k := range kinds {
		want := 20000
		if k == ChapterMark {
			want = 100000
		}
		if defaultCaps[k] != want {
			t.Errorf("%s cap %d, want %d", k, defaultCaps[k], want)
		}
	}
}

// contract/sync/merge/* (spec §6.3, a title merged into another) is deliberately not run: the
// kit never merges titles (the operator puts sources under one ref), so it has no merge and no
// merge redirects. A kit merge would have to run these fixtures first.
func TestMergeFixturesAreNotRun(t *testing.T) {
	if len(fixtures(t, "merge")) == 0 {
		t.Fatal("the merge fixtures are gone: update this note")
	}
	t.Skip("the kit has no title merges (contract/sync/merge/* covers a merge the kit never does)")
}
