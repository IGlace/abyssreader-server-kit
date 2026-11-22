// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

// libraryFixture imports the sample catalog and makes one account; it returns the account id
// and the titles' ids by name.
func libraryFixture(t *testing.T) (*fixture, int64, map[string]int64) {
	t.Helper()
	f := newFixture(t, nil)
	f.importDir(folder(t, sampleManifest(), map[string][]byte{"covers/lantern.png": pngBytes(t, 40, 60)}))
	r, err := f.cat.db.Exec(`INSERT INTO users (email, email_key, display_name, created_at, updated_at) VALUES ('r@example.com', 'r@example.com', 'r', 0, 0)`)
	if err != nil {
		t.Fatal(err)
	}
	user, _ := r.LastInsertId()
	ids := map[string]int64{}
	for _, ti := range f.snapshot() {
		ids[ti.Name] = ti.ID
	}
	return f, user, ids
}

var libraryOperationSequence atomic.Int64

func pushList(t *testing.T, f *fixture, user int64, kind string, title int64, ts string, deleted bool) {
	t.Helper()
	item, _ := json.Marshal(map[string]any{"opId": fmt.Sprintf("00000000-0000-4000-8000-%012d", libraryOperationSequence.Add(1)), "kind": kind,
		"key": map[string]any{"mainMangaId": title}, "data": nil, "clientUpdatedAt": ts, "deleted": deleted})
	out, err := usersync.New(f.cat.db).Push(context.Background(), user, []json.RawMessage{item})
	if err != nil || out.Results[0].Status != "applied" {
		t.Fatalf("%+v %v", out, err)
	}
}

func (f *fixture) names(q Query) []string {
	f.t.Helper()
	q.Page, q.Size = 1, 100
	p, err := f.cat.Search(context.Background(), q)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, ti := range p.Titles {
		out = append(out, ti.Name)
	}
	if int64(len(out)) != p.Total {
		f.t.Fatalf("total %d for %d titles", p.Total, len(out))
	}
	return out
}

func TestSearchLeavesOutTheCallersBlacklistAndListsALibrary(t *testing.T) {
	f, user, ids := libraryFixture(t)
	pushList(t, f, user, "blacklist", ids["Tidewater"], "2026-10-10T10:00:00.000Z", false)
	pushList(t, f, user, "favorite", ids["Tidewater"], "2026-10-10T10:00:00.000Z", false)
	pushList(t, f, user, "favorite", ids["The Lantern Keeper"], "2026-10-10T10:00:00.000Z", false)

	if got := f.names(Query{Sort: SortAlphabetical, Asc: true}); !slices.Equal(got, []string{"The Lantern Keeper", "Tidewater"}) {
		t.Errorf("anonymous: %v", got)
	}
	if got := f.names(Query{Sort: SortAlphabetical, Asc: true, HideBlacklistOf: user}); !slices.Equal(got, []string{"The Lantern Keeper"}) {
		t.Errorf("signed in: %v", got)
	}
	// A library list is not filtered by the blacklist: a title can be on both.
	if got := f.names(Query{Sort: SortAlphabetical, Asc: true, Library: &Library{UserID: user, Kind: "favorite"}}); !slices.Equal(got, []string{"The Lantern Keeper", "Tidewater"}) {
		t.Errorf("favorites: %v", got)
	}
	if got := f.names(Query{Sort: SortAlphabetical, Asc: true, Library: &Library{UserID: user, Kind: "blacklist"}}); !slices.Equal(got, []string{"Tidewater"}) {
		t.Errorf("blacklist: %v", got)
	}
	if got := f.names(Query{Sort: SortAlphabetical, Asc: true, Library: &Library{UserID: user, Kind: "planning"}}); len(got) != 0 {
		t.Errorf("planning: %v", got)
	}
	// A removal takes the title off the list; another account's list is its own.
	pushList(t, f, user, "favorite", ids["Tidewater"], "2026-10-10T11:00:00.000Z", true)
	if got := f.names(Query{Sort: SortAlphabetical, Asc: true, Library: &Library{UserID: user, Kind: "favorite"}}); !slices.Equal(got, []string{"The Lantern Keeper"}) {
		t.Errorf("favorites after the removal: %v", got)
	}
	if got := f.names(Query{Library: &Library{UserID: user + 1, Kind: "favorite"}}); len(got) != 0 {
		t.Errorf("another account's favorites: %v", got)
	}
}

func TestRemovingATitleTombstonesTheRecordsOnIt(t *testing.T) {
	f, user, ids := libraryFixture(t)
	pushList(t, f, user, "favorite", ids["Tidewater"], "2026-10-10T10:00:00.000Z", false)
	pushList(t, f, user, "favorite", ids["The Lantern Keeper"], "2026-10-10T10:00:00.000Z", false)
	if _, err := f.cat.Remove(context.Background(), "tidewater"); err != nil {
		t.Fatal(err)
	}
	cursor := "d.2"
	p, err := usersync.New(f.cat.db).Pull(context.Background(), user, &cursor, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Changes) != 1 || !p.Changes[0].Deleted || p.Changes[0].Key.MainMangaID != ids["Tidewater"] {
		t.Fatalf("after the removal: %+v", p.Changes)
	}
	if got := f.names(Query{Library: &Library{UserID: user, Kind: "favorite"}}); !slices.Equal(got, []string{"The Lantern Keeper"}) {
		t.Fatalf("favorites: %v", got)
	}
}
