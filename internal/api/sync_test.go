// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/catalog"
	"github.com/IGlace/abyssreader-server-kit/internal/config"
	"github.com/IGlace/abyssreader-server-kit/internal/contracttest"
)

var opSeq atomic.Int64

// item is one push item; data nil for the lists and removals.
func item(kind string, key obj, data any, ts string, deleted bool) obj {
	return obj{"opId": fmt.Sprintf("00000000-0000-4000-8000-%012d", opSeq.Add(1)), "kind": kind, "key": key,
		"data": data, "clientUpdatedAt": ts, "deleted": deleted}
}

func titleKey(id int64) obj { return obj{"mainMangaId": id} }

func (h *harness) push(token string, items ...obj) answer {
	h.t.Helper()
	a := h.do("POST", "/api/v1/sync/push", req{token: token, body: obj{"items": items}})
	h.expect(a, 200, "")
	return a
}

func statuses(a answer) []string {
	var out []string
	for _, r := range a.body["results"].([]any) {
		m := r.(map[string]any)
		s := m["status"].(string)
		if reason, ok := m["reason"].(string); ok {
			s += ":" + reason
		}
		out = append(out, s)
	}
	return out
}

// pull reads every page after cursor ("" = the first pull) and returns the changes and the
// last cursor.
func (h *harness) pull(token, cursor string, limit int) ([]map[string]any, string) {
	h.t.Helper()
	var all []map[string]any
	for {
		q := url.Values{}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		if limit > 0 {
			q.Set("limit", strconv.Itoa(limit))
		}
		a := h.do("GET", "/api/v1/sync/pull?"+q.Encode(), req{token: token})
		h.expect(a, 200, "")
		for _, c := range a.body["changes"].([]any) {
			all = append(all, c.(map[string]any))
		}
		if limit > 0 && len(a.body["changes"].([]any)) > limit {
			h.t.Fatalf("a page over the limit: %s", a.raw)
		}
		cursor = a.body["cursor"].(string)
		if a.body["hasMore"] != true {
			return all, cursor
		}
	}
}

// titleIDs maps the small catalog's names to ids.
func (h *harness) titleIDs() map[string]int64 {
	h.t.Helper()
	a := h.do("GET", "/api/v1/titles?size=100", req{})
	h.expect(a, 200, "")
	out := map[string]int64{}
	for _, it := range a.body["items"].([]any) {
		m := it.(map[string]any)
		out[m["name"].(string)] = int64(m["id"].(float64))
	}
	return out
}

func (h *harness) library(token, list string, extra string) answer {
	h.t.Helper()
	a := h.do("GET", "/api/v1/me/library?list="+list+extra, req{token: token})
	h.expect(a, 200, "")
	return a
}

func meOf(a answer) string { return fmt.Sprint(a.body["me"]) }

// allKinds is one item of every kind on title id (sources of the small catalog).
func allKinds(id int64, ts string) []obj {
	return []obj{
		item("favorite", titleKey(id), nil, ts, false),
		item("planning", titleKey(id), nil, ts, false),
		item("blacklist", titleKey(id), nil, ts, false),
		item("rating", titleKey(id), obj{"opinion": "up"}, ts, false),
		item("position", titleKey(id), obj{"sourceSlug": "paper-moon", "chapterUrl": "/series/tidewater/12", "chapterNumber": 12, "pageIndex": 4}, ts, false),
		item("chapterMark", obj{"mainMangaId": id, "sourceSlug": "paper-moon", "chapterUrl": "/series/tidewater/12"},
			obj{"chapterNumber": 12.5, "chapterName": "Twelve and a half", "read": true, "lastPageRead": 20, "totalPages": 20, "bookmarked": true}, ts, false),
		item("inbox", titleKey(id), obj{"sourceSlug": "luna-es", "latestChapterNumber": 13, "latestChapterUrl": "https://luna.example.net/c/13",
			"detectedAt": "2026-10-10T09:00:00.000Z", "seen": false}, ts, false),
	}
}

// asChange is a pushed item as a pull gives it back.
func asChange(it obj) map[string]any {
	var data any
	if d, ok := it["data"].(obj); ok && it["deleted"] == false {
		full := map[string]any{}
		fields := map[string][]string{
			"rating":      {"opinion"},
			"position":    {"sourceSlug", "chapterUrl", "chapterNumber", "chapterName", "pageIndex"},
			"chapterMark": {"chapterNumber", "chapterName", "read", "lastPageRead", "totalPages", "bookmarked"},
			"inbox":       {"sourceSlug", "latestChapterNumber", "latestChapterName", "latestChapterUrl", "detectedAt", "seen"},
		}[it["kind"].(string)]
		for _, f := range fields {
			full[f] = d[f]
		}
		data = full
	}
	b, _ := json.Marshal(obj{"kind": it["kind"], "key": it["key"], "data": data, "clientUpdatedAt": it["clientUpdatedAt"], "deleted": it["deleted"]})
	var m map[string]any
	json.Unmarshal(b, &m)
	return m
}

func TestSyncRoundTripsEveryKindAcrossSessions(t *testing.T) {
	h := newHarness(t, nil)
	h.smallCatalog()
	id := h.titleIDs()["Tidewater"]
	device1, _ := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	items := allKinds(id, "2026-10-10T10:00:00.000Z")
	a := h.push(device1, items...)
	if got := statuses(a); !slices.Equal(got, slices.Repeat([]string{"applied"}, 7)) || a.body["cursor"] != "d.7" {
		t.Fatalf("push: %s", a.raw)
	}
	snap, cursor1 := h.pull(device1, "", 0)
	if len(snap) != 7 || cursor1 != "d.7" {
		t.Fatalf("snapshot %d changes, cursor %s", len(snap), cursor1)
	}
	for i, it := range items {
		if !reflect.DeepEqual(snap[i], asChange(it)) {
			t.Errorf("%s:\n got %v\nwant %v", it["kind"], snap[i], asChange(it))
		}
	}

	// A second session of the same account sees the same, and its change reaches the first.
	a = h.do("POST", "/api/v1/auth/sign-in", req{body: obj{"email": "reader@example.com", "password": "correct-horse-7"}})
	h.expect(a, 200, "")
	device2 := a.body["token"].(string)
	if snap2, _ := h.pull(device2, "", 3); !reflect.DeepEqual(snap2, snap) {
		t.Fatalf("the second session's snapshot differs:\n%v\n%v", snap2, snap)
	}
	unvote := item("rating", titleKey(id), nil, "2026-10-10T11:00:00.000Z", true)
	h.push(device2, unvote)
	delta, cursor := h.pull(device1, cursor1, 0)
	if len(delta) != 1 || !reflect.DeepEqual(delta[0], asChange(unvote)) || cursor != "d.8" {
		t.Fatalf("the first session's delta: %v up to %s", delta, cursor)
	}
	// Nothing new: the same cursor comes back.
	if delta, again := h.pull(device1, cursor, 0); len(delta) != 0 || again != cursor {
		t.Fatalf("an empty delta: %v %s", delta, again)
	}
	// A pushed tombstone is no snapshot record.
	if snap, _ := h.pull(device1, "", 0); len(snap) != 6 {
		t.Fatalf("snapshot after the removal: %d", len(snap))
	}
}

func TestStaleWritesLoseAndNewerOnesWin(t *testing.T) {
	h := newHarness(t, nil)
	h.smallCatalog()
	id := h.titleIDs()["Tidewater"]
	tok, _ := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	h.push(tok, item("rating", titleKey(id), obj{"opinion": "up"}, "2026-10-10T10:00:00.000Z", false))
	if got := statuses(h.push(tok, item("rating", titleKey(id), obj{"opinion": "down"}, "2026-10-10T09:59:59.999Z", false))); got[0] != "stale" {
		t.Fatalf("older: %v", got)
	}
	if got := statuses(h.push(tok, item("rating", titleKey(id), obj{"opinion": "down"}, "2026-10-10T10:00:00.000Z", false))); got[0] != "stale" {
		t.Fatalf("equal: %v", got)
	}
	title := h.do("GET", fmt.Sprintf("/api/v1/titles/%d?countView=false", id), req{token: tok})
	if meOf(title) != "map[blacklist:false favorite:false planning:false vote:up]" {
		t.Fatalf("me after the stale writes: %s", meOf(title))
	}
	if got := statuses(h.push(tok, item("rating", titleKey(id), obj{"opinion": "down"}, "2026-10-10T10:00:00.001Z", false))); got[0] != "applied" {
		t.Fatalf("newer: %v", got)
	}
	title = h.do("GET", fmt.Sprintf("/api/v1/titles/%d?countView=false", id), req{token: tok})
	if meOf(title) != "map[blacklist:false favorite:false planning:false vote:down]" {
		t.Fatalf("me after the newer write: %s", meOf(title))
	}
}

func TestLibraryAndMeFollowPushesAndRemovals(t *testing.T) {
	h := newHarness(t, nil)
	h.smallCatalog()
	ids := h.titleIDs()
	lantern, tide, orbit := ids["The Lantern Keeper"], ids["Tidewater"], ids["Quiet Orbit"]
	tok, _ := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	h.push(tok,
		item("favorite", titleKey(lantern), nil, "2026-10-10T10:00:00.000Z", false),
		item("favorite", titleKey(orbit), nil, "2026-10-10T10:00:00.000Z", false),
		item("planning", titleKey(lantern), nil, "2026-10-10T10:00:00.000Z", false),
		item("rating", titleKey(lantern), obj{"opinion": "up"}, "2026-10-10T10:00:00.000Z", false),
		item("blacklist", titleKey(tide), nil, "2026-10-10T10:00:00.000Z", false),
	)
	fav := h.library(tok, "favorite", "&sort=alphabetical")
	if got := names(fav); !slices.Equal(got, []string{"Quiet Orbit", "The Lantern Keeper"}) || fav.body["totalItems"] != 2.0 {
		t.Fatalf("favorites: %v %s", got, fav.raw)
	}
	if me := fmt.Sprint(fav.body["items"].([]any)[1].(map[string]any)["me"]); me != "map[blacklist:false favorite:true planning:true vote:up]" {
		t.Fatalf("me in the library: %s", me)
	}
	// Paging, q, the sort and the order work as in the search.
	p := h.library(tok, "favorite", "&sort=alphabetical&order=desc&size=1&page=1")
	if got := names(p); !slices.Equal(got, []string{"The Lantern Keeper"}) || p.body["hasMore"] != true {
		t.Fatalf("page 1: %v %s", got, p.raw)
	}
	if p := h.library(tok, "favorite", "&q=orbit"); !slices.Equal(names(p), []string{"Quiet Orbit"}) {
		t.Fatalf("q: %s", p.raw)
	}
	if got := names(h.library(tok, "planning", "")); !slices.Equal(got, []string{"The Lantern Keeper"}) {
		t.Fatalf("planning: %v", got)
	}
	if got := names(h.library(tok, "blacklist", "")); !slices.Equal(got, []string{"Tidewater"}) {
		t.Fatalf("blacklist: %v", got)
	}
	// The search leaves the caller's blacklist out; an anonymous search has every title, me null.
	signedIn := h.do("GET", "/api/v1/titles?sort=alphabetical", req{token: tok})
	if got := names(signedIn); !slices.Equal(got, []string{"Quiet Orbit", "The Lantern Keeper"}) || signedIn.body["totalItems"] != 2.0 {
		t.Fatalf("signed-in search: %v", got)
	}
	anon := h.do("GET", "/api/v1/titles?sort=alphabetical", req{})
	if got := names(anon); len(got) != 3 || anon.body["items"].([]any)[0].(map[string]any)["me"] != nil {
		t.Fatalf("anonymous search: %s", anon.raw)
	}
	// By id and by slug, signed in: the blacklisted title still has its details and its me.
	tideDetails := h.do("GET", fmt.Sprintf("/api/v1/titles/%d?countView=false", tide), req{token: tok})
	h.expect(tideDetails, 200, "")
	if meOf(tideDetails) != "map[blacklist:true favorite:false planning:false vote:<nil>]" {
		t.Fatalf("tidewater me: %s", meOf(tideDetails))
	}
	if a := h.do("GET", "/api/v1/titles/by-slug/the-lantern-keeper?countView=false", req{token: tok}); meOf(a) != "map[blacklist:false favorite:true planning:true vote:up]" {
		t.Fatalf("lantern me: %s", meOf(a))
	}

	// Removal and re-add.
	h.push(tok, item("favorite", titleKey(lantern), nil, "2026-10-10T11:00:00.000Z", true))
	if got := names(h.library(tok, "favorite", "")); !slices.Equal(got, []string{"Quiet Orbit"}) {
		t.Fatalf("after the removal: %v", got)
	}
	if a := h.do("GET", fmt.Sprintf("/api/v1/titles/%d?countView=false", lantern), req{token: tok}); meOf(a) != "map[blacklist:false favorite:false planning:true vote:up]" {
		t.Fatalf("me after the removal: %s", meOf(a))
	}
	if got := statuses(h.push(tok, item("favorite", titleKey(lantern), nil, "2026-10-10T12:00:00.000Z", false))); got[0] != "applied" {
		t.Fatalf("re-add: %v", got)
	}
	if got := names(h.library(tok, "favorite", "&sort=alphabetical")); !slices.Equal(got, []string{"Quiet Orbit", "The Lantern Keeper"}) {
		t.Fatalf("after the re-add: %v", got)
	}
}

func TestLibraryParameters(t *testing.T) {
	h := newHarness(t, nil)
	tok, _ := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	for query, want := range map[string]string{
		"":                             "list:required",
		"?list=reading":                "list:unknown_value",
		"?list=favorite&list=planning": "list:invalid",
		"?list=favorite&size=101":      "size:too_large",
		"?list=favorite&size=0":        "size:too_small",
		"?list=favorite&page=0":        "page:too_small",
		"?list=favorite&sort=views":    "sort:unknown_value",
		"?list=favorite&order=up":      "order:unknown_value",
	} {
		a := h.do("GET", "/api/v1/me/library"+query, req{token: tok})
		h.expect(a, 400, "validation_failed")
		if got := fieldCodes(a); got != want {
			t.Errorf("%q: %s, want %s", query, got, want)
		}
	}
	h.expect(h.do("GET", "/api/v1/me/library?list=favorite", req{}), 401, "session_invalid")
	empty := h.library(tok, "favorite", "")
	if string(empty.raw) != `{"items":[],"page":1,"size":20,"totalItems":0,"hasMore":false}`+"\n" {
		t.Fatalf("empty: %s", empty.raw)
	}
}

func TestPushEnvelopeAndBatchCaps(t *testing.T) {
	h := newHarness(t, nil)
	h.smallCatalog()
	id := h.titleIDs()["Tidewater"]
	tok, _ := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	many := func(n int) []obj {
		out := make([]obj, n)
		for i := range out {
			out[i] = item("chapterMark", obj{"mainMangaId": id, "sourceSlug": "paper-moon", "chapterUrl": fmt.Sprintf("/c/%d", i)},
				obj{"read": true, "bookmarked": false}, "2026-10-10T10:00:00.000Z", false)
		}
		return out
	}
	for name, body := range map[string]any{
		"not JSON":              "{",
		"an array":              "[]",
		"no items":              obj{},
		"items null":            obj{"items": nil},
		"items not a list":      obj{"items": obj{}},
		"no item":               obj{"items": []obj{}},
		"501 items":             obj{"items": many(501)},
		"an item not an object": obj{"items": []any{1}},
		"another field":         obj{"items": many(1), "device": "phone"},
		"two objects":           `{"items":[{}]}{"items":[{}]}`,
		"a body over 1 MiB":     obj{"items": []obj{{"opId": strings.Repeat("x", maxPushBody)}}},
	} {
		a := h.do("POST", "/api/v1/sync/push", req{token: tok, body: body})
		if a.status != 400 || a.code() != "validation_failed" {
			t.Errorf("%s: %d %s", name, a.status, a.raw)
		}
	}
	// 500 items is the most a push takes; a bad item is rejected alone.
	items := many(500)
	items[3] = obj{"opId": "not-a-uuid"}
	a := h.push(tok, items...)
	got := statuses(a)
	if len(got) != 500 || got[3] != "rejected:invalid" || got[0] != "applied" || a.body["cursor"] != "d.499" {
		t.Fatalf("500 items: %d results, %v, %v", len(got), got[:5], a.body["cursor"])
	}
	h.expect(h.do("POST", "/api/v1/sync/push", req{body: obj{"items": many(1)}}), 401, "session_invalid")
}

func TestPullCursorLimitsAndPages(t *testing.T) {
	h := newHarness(t, nil)
	h.smallCatalog()
	id := h.titleIDs()["Tidewater"]
	tok, _ := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	var items []obj
	for i := 0; i < 30; i++ {
		items = append(items, item("chapterMark", obj{"mainMangaId": id, "sourceSlug": "paper-moon", "chapterUrl": fmt.Sprintf("/c/%d", i)},
			obj{"read": true, "bookmarked": false}, "2026-10-10T10:00:00.000Z", false))
	}
	h.push(tok, items...)
	snap, cursor := h.pull(tok, "", 7)
	if len(snap) != 30 || cursor != "d.30" {
		t.Fatalf("paged snapshot: %d up to %s", len(snap), cursor)
	}
	if delta, c := h.pull(tok, "d.0", 7); len(delta) != 30 || c != "d.30" {
		t.Fatalf("paged delta: %d up to %s", len(delta), c)
	}
	first := h.do("GET", "/api/v1/sync/pull?limit=30", req{token: tok})
	if first.body["hasMore"] != false || len(first.body["changes"].([]any)) != 30 {
		t.Fatalf("one page of exactly the limit: %s", first.raw[:200])
	}

	for query, field := range map[string]string{
		"cursor=nonsense":                    "cursor",
		"cursor=" + strings.Repeat("d", 201): "cursor",
		"cursor=":                            "cursor",
		"cursor=d.1&cursor=d.2":              "cursor",
		"limit=0":                            "limit",
		"limit=1001":                         "limit",
		"limit=ten":                          "limit",
	} {
		a := h.do("GET", "/api/v1/sync/pull?"+query, req{token: tok})
		h.expect(a, 400, "validation_failed")
		if !strings.HasPrefix(fieldCodes(a), field+":") {
			t.Errorf("%s: %s", query, fieldCodes(a))
		}
	}
	// A cursor ahead of the account: the app pulls again without one.
	a := h.do("GET", "/api/v1/sync/pull?cursor=d.31", req{token: tok})
	if a.status != 409 || strings.TrimSpace(string(a.raw)) != `{"fullResyncRequired":true}` {
		t.Fatalf("409: %d %s", a.status, a.raw)
	}
	h.expect(h.do("GET", "/api/v1/sync/pull", req{}), 401, "session_invalid")
}

func TestTwoAccountsNeverSeeEachOthersRecords(t *testing.T) {
	h := newHarness(t, nil)
	h.smallCatalog()
	id := h.titleIDs()["Tidewater"]
	alice, _ := h.signUp("Alice", "alice@example.com", "correct-horse-7")
	bob, _ := h.signUp("Bob", "bob@example.com", "correct-horse-8")
	h.push(alice, allKinds(id, "2026-10-10T10:00:00.000Z")...)
	if snap, c := h.pull(bob, "", 0); len(snap) != 0 || c != "d.0" {
		t.Fatalf("bob's snapshot: %v %s", snap, c)
	}
	if delta, _ := h.pull(bob, "d.0", 0); len(delta) != 0 {
		t.Fatalf("bob's delta: %v", delta)
	}
	if got := names(h.library(bob, "favorite", "")); len(got) != 0 {
		t.Fatalf("bob's favorites: %v", got)
	}
	if a := h.do("GET", fmt.Sprintf("/api/v1/titles/%d?countView=false", id), req{token: bob}); meOf(a) != "map[blacklist:false favorite:false planning:false vote:<nil>]" {
		t.Fatalf("bob's me: %s", meOf(a))
	}
	// Bob removing the same keys writes his own tombstones; Alice keeps hers.
	var removals []obj
	for _, it := range allKinds(id, "2026-10-10T11:00:00.000Z") {
		it["deleted"], it["data"] = true, nil
		removals = append(removals, it)
	}
	h.push(bob, removals...)
	if snap, _ := h.pull(alice, "", 0); len(snap) != 7 {
		t.Fatalf("alice's snapshot after bob's removals: %d", len(snap))
	}
}

func TestAccountDeletionRemovesTheLibraryAndSyncRecords(t *testing.T) {
	h := newHarness(t, nil)
	h.smallCatalog()
	id := h.titleIDs()["Tidewater"]
	tok, user := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	h.push(tok, allKinds(id, "2026-10-10T10:00:00.000Z")...)
	h.push(tok, item("favorite", titleKey(id), nil, "2026-10-10T11:00:00.000Z", true)) // a tombstone too
	if n := h.rowsOf(user); n < 9 {
		t.Fatalf("%d rows before the deletion", n)
	}
	h.expect(h.do("POST", "/api/v1/me/deletion", req{token: tok, body: obj{"password": "correct-horse-7"}}), 204, "")
	if n := h.rowsOf(user); n != 0 {
		t.Fatalf("%d rows left after the deletion", n)
	}
	var left int
	h.db.QueryRow(`SELECT (SELECT count(*) FROM sync_records) + (SELECT count(*) FROM sync_accounts)`).Scan(&left)
	if left != 0 {
		t.Fatalf("%d sync rows left", left)
	}
}

// A title the server hides (hosted-only while hosted-chapters is off) keeps its records: a pull
// still gives them, a push is still applied, the library leaves it out until the feature is on.
func TestHiddenTitlesKeepTheirRecords(t *testing.T) {
	on := newHarness(t, hostedOn)
	on.importHosted(obj{"manifestVersion": 1, "titles": []obj{{
		"ref": "lamp-post", "name": "Lamp Post",
		"hosted": []obj{{"lang": "en", "chapters": []obj{{"ref": "1", "pages": []any{"p.png"}}}}},
	}}}, map[string][]byte{"p.png": pagePNG(t, 400, 600, 7)})
	lamp := on.do("GET", "/api/v1/titles/by-slug/lamp-post?countView=false", req{})
	on.expect(lamp, 200, "")
	id := int64(lamp.body["id"].(float64))
	chapters := on.do("GET", "/api/v1/source-titles/"+fmt.Sprint(int64(lamp.body["sources"].([]any)[0].(map[string]any)["id"].(float64)))+"/chapters", req{})
	on.expect(chapters, 200, "")
	chapterURL := chapters.body["items"].([]any)[0].(map[string]any)["url"].(string)
	tok, _ := on.signUp("Reader", "reader@example.com", "correct-horse-7")
	on.push(tok,
		item("favorite", titleKey(id), nil, "2026-10-10T10:00:00.000Z", false),
		item("chapterMark", obj{"mainMangaId": id, "sourceSlug": "hosted", "chapterUrl": chapterURL}, obj{"read": true, "bookmarked": false}, "2026-10-10T10:00:00.000Z", false),
	)
	if got := names(on.library(tok, "favorite", "")); !slices.Equal(got, []string{"Lamp Post"}) {
		t.Fatalf("on: %v", got)
	}

	off := on.reopen(func(c *config.Config) { c.Features = nil })
	if a := off.library(tok, "favorite", ""); len(names(a)) != 0 || a.body["totalItems"] != 0.0 {
		t.Fatalf("off: the library shows %s", a.raw)
	}
	if snap, _ := off.pull(tok, "", 0); len(snap) != 2 {
		t.Fatalf("off: the snapshot has %d records", len(snap))
	}
	got := statuses(off.push(tok,
		item("planning", titleKey(id), nil, "2026-10-10T11:00:00.000Z", false),
		item("chapterMark", obj{"mainMangaId": id, "sourceSlug": "hosted", "chapterUrl": chapterURL}, obj{"read": false, "bookmarked": true}, "2026-10-10T11:00:00.000Z", false),
	))
	if !slices.Equal(got, []string{"applied", "applied"}) {
		t.Fatalf("off: pushes %v", got)
	}

	again := on.reopen(hostedOn)
	if got := names(again.library(tok, "planning", "")); !slices.Equal(got, []string{"Lamp Post"}) {
		t.Fatalf("on again: planning %v", got)
	}
	if a := again.do("GET", fmt.Sprintf("/api/v1/titles/%d?countView=false", id), req{token: tok}); meOf(a) != "map[blacklist:false favorite:true planning:true vote:<nil>]" {
		t.Fatalf("on again: me %s", meOf(a))
	}
}

// A title the operator removed: every record on it becomes a tombstone, which the next pull
// gives; the library no longer lists it; a later push naming it is unknown_title.
func TestRemovedTitlesTombstoneTheirRecords(t *testing.T) {
	h := newHarness(t, nil)
	h.smallCatalog()
	ids := h.titleIDs()
	tide := ids["Tidewater"]
	tok, _ := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	h.push(tok, append(allKinds(tide, "2026-10-10T10:00:00.000Z"), item("favorite", titleKey(ids["Quiet Orbit"]), nil, "2026-10-10T10:00:00.000Z", false))...)
	if _, err := catalog.New(h.db, h.covers, nil).Remove(context.Background(), "tidewater"); err != nil {
		t.Fatal(err)
	}
	delta, cursor := h.pull(tok, "d.8", 0)
	if len(delta) != 7 || cursor != "d.15" {
		t.Fatalf("after the removal: %d changes up to %s", len(delta), cursor)
	}
	for _, c := range delta {
		if c["deleted"] != true || c["data"] != nil {
			t.Fatalf("not a tombstone: %v", c)
		}
	}
	if got := names(h.library(tok, "favorite", "")); !slices.Equal(got, []string{"Quiet Orbit"}) {
		t.Fatalf("favorites: %v", got)
	}
	if got := statuses(h.push(tok, item("favorite", titleKey(tide), nil, "2026-10-11T10:00:00.000Z", false))); got[0] != "rejected:unknown_title" {
		t.Fatalf("push to the removed title: %v", got)
	}
}

func TestSyncRateLimitsPerAccount(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.SyncPushesPerMinute, c.SyncPullsPerMinute = 2, 2 })
	h.smallCatalog()
	id := h.titleIDs()["Tidewater"]
	alice, _ := h.signUp("Alice", "alice@example.com", "correct-horse-7")
	bob, _ := h.signUp("Bob", "bob@example.com", "correct-horse-8")
	fav := func() obj { return item("favorite", titleKey(id), nil, "2026-10-10T10:00:00.000Z", false) }
	h.push(alice, fav())
	h.push(alice, fav())
	a := h.do("POST", "/api/v1/sync/push", req{token: alice, body: obj{"items": []obj{fav()}}})
	h.expect(a, 429, "rate_limited")
	h.push(bob, fav()) // another account has its own budget
	h.pull(alice, "", 0)
	h.pull(alice, "", 0)
	h.expect(h.do("GET", "/api/v1/sync/pull", req{token: alice}), 429, "rate_limited")
}

func TestSyncAndLibraryNeedTheAccessKey(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.AccessKey = "lantern-access-key-1" })
	for _, r := range []struct{ method, path string }{{"GET", "/api/v1/me/library?list=favorite"}, {"POST", "/api/v1/sync/push"}, {"GET", "/api/v1/sync/pull"}} {
		h.expect(h.do(r.method, r.path, req{body: obj{"items": []obj{}}}), 403, "access_key_required")
		h.expect(h.do(r.method, r.path, req{accessKey: "wrong", body: obj{"items": []obj{}}}), 403, "access_key_invalid")
	}
}

func TestEverySyncAndLibraryRouteMatchesTheContract(t *testing.T) {
	c := contracttest.Load(t)
	for _, test := range []func(*testing.T){
		TestSyncRoundTripsEveryKindAcrossSessions, TestLibraryAndMeFollowPushesAndRemovals, TestLibraryParameters,
		TestPushEnvelopeAndBatchCaps, TestPullCursorLimitsAndPages, TestSyncRateLimitsPerAccount, TestSyncAndLibraryNeedTheAccessKey,
	} {
		test(t)
	}
	want := map[string][]string{
		"getLibrary": {"200", "400 validation_failed", "401 session_invalid", "403 access_key_required", "403 access_key_invalid"},
		"syncPush":   {"200", "400 validation_failed", "401 session_invalid", "403 access_key_required", "403 access_key_invalid", "429 rate_limited"},
		"syncPull":   {"200", "400 validation_failed", "401 session_invalid", "403 access_key_required", "403 access_key_invalid", "409 ", "429 rate_limited"},
	}
	for op, cases := range want {
		for _, cs := range cases {
			status, code, _ := strings.Cut(cs, " ")
			n, _ := strconv.Atoi(status)
			if !c.Seen(op, n, code) {
				t.Errorf("%s never answered %s", op, cs)
			}
		}
	}
	if t.Failed() {
		t.Logf("seen:\n%s", c.Summary())
	}
}
