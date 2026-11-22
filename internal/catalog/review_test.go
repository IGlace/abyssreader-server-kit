// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package catalog

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/covers"
	"github.com/IGlace/abyssreader-server-kit/internal/pages"
)

// ── 1. A chapter has at most pages.MaxChapterImages images ──

func TestAChapterHasAtMostFiveHundredImages(t *testing.T) {
	h := newHostedFixture(t)
	m := hostedManifest()
	hosted := m["titles"].([]obj)[0]["hosted"].([]obj)[0]
	// The 13000 px strip is 3 slices: listed 167 times it would be 501 images.
	var list []any
	for range 167 {
		list = append(list, "ch2/strip.png")
	}
	hosted["chapters"] = []obj{
		{"ref": "big", "number": 1, "pages": list},
		{"ref": "fine", "number": 2, "pages": []any{"ch1/01.png"}},
	}
	r := h.importDir(folder(t, m, h.files()))
	if len(r.ChapterFailures) != 1 || r.ChapterFailures[0].Ref != "night-ferry/en/big" ||
		!strings.Contains(r.ChapterFailures[0].Reason, "more than 500 page images") || r.Chapters.Added != 1 {
		t.Fatalf("report: %+v", r)
	}
	if h.prepared != 2 {
		t.Fatalf("the strip was read %d times", h.prepared)
	}
}

// ── 3. Files of the page store no row names are removed ──

func TestPageFilesNoRowNamesAreRemovedAtTheEndOfAnImport(t *testing.T) {
	h := newHostedFixture(t)
	dir := folder(t, hostedManifest(), h.files())
	h.importDir(dir)
	kept := storedPages(t, h.dataDir)
	pagesDir := filepath.Join(h.dataDir, pages.Dir)
	stray := strings.Repeat("a", 64) + ".png"
	os.WriteFile(filepath.Join(pagesDir, stray), []byte("left by a crash"), 0o644)
	os.WriteFile(filepath.Join(pagesDir, ".incoming-123"), []byte("half written"), 0o644)
	os.WriteFile(filepath.Join(pagesDir, "notes.txt"), []byte("not the kit's"), 0o644)

	h.importDir(dir)
	got := storedPages(t, h.dataDir)
	if !slices.Equal(got, append(slices.Clone(kept), "notes.txt")) {
		t.Fatalf("page store after the import: %v (had %v)", got, kept)
	}
	// A removal also sweeps the page store, without pruning unrelated covers.
	os.WriteFile(filepath.Join(pagesDir, stray), []byte("left by a crash"), 0o644)
	coverStray := filepath.Join(h.dataDir, covers.Dir, strings.Repeat("b", 64)+".png")
	if err := os.WriteFile(coverStray, []byte("a cover file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.cat.Remove(context.Background(), "night-ferry"); err != nil {
		t.Fatal(err)
	}
	if got := storedPages(t, h.dataDir); !slices.Equal(got, []string{"notes.txt"}) {
		t.Fatalf("page store after the removal: %v", got)
	}
	if _, err := os.Stat(coverStray); err != nil {
		t.Fatalf("unrelated cover after the removal: %v", err)
	}
}

// ── 4. With the feature off, a title with nothing visible to read is hidden ──

func TestHostedOnlyTitlesAreHiddenWhileTheFeatureIsOff(t *testing.T) {
	h := newHostedFixture(t)
	m := hostedManifest()
	m["sources"] = sources()
	ferry := m["titles"].([]obj)[0]
	ferry["genres"] = []string{"Fantasy", "Ferries"}
	m["titles"] = append(m["titles"].([]obj),
		obj{"ref": "tidewater", "name": "Tidewater", "genres": []string{"Fantasy"},
			"sourceTitles": []obj{{"source": "paper-moon", "url": "/series/tidewater/"}}},
		obj{"ref": "both", "name": "Both Ways", "genres": []string{"Fantasy"},
			"sourceTitles": []obj{{"source": "paper-moon", "url": "/series/both/"}},
			"hosted":       []obj{{"lang": "en", "chapters": []obj{{"ref": "1", "pages": []any{"ch1/01.png"}}}}}},
	)
	h.importDir(folder(t, m, h.files()))
	ctx := context.Background()
	byName := func() ([]string, int64) {
		p, err := h.cat.Search(ctx, Query{Page: 1, Size: 100, Sort: SortAlphabetical, Asc: true})
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, ti := range p.Titles {
			names = append(names, ti.Name)
		}
		return names, p.Total
	}
	genres := func() map[string]int64 {
		gs, err := h.cat.Genres(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int64{}
		for _, g := range gs {
			out[g.Name] = g.TitleCount
		}
		return out
	}
	on, total := byName()
	if total != 3 || genres()["Fantasy"] != 3 || genres()["Ferries"] != 1 {
		t.Fatalf("on: %v %d %v", on, total, genres())
	}
	ferryTitle, err := h.cat.TitleBySlug(ctx, "night-ferry")
	if err != nil {
		t.Fatal(err)
	}

	h.cat.ShowHosted(false)
	names, total := byName()
	if total != 2 || !slices.Equal(names, []string{"Both Ways", "Tidewater"}) {
		t.Fatalf("off: %v %d", names, total)
	}
	if g := genres(); g["Fantasy"] != 2 || g["Ferries"] != 0 {
		t.Fatalf("genres off: %v", g)
	}
	if _, err := h.cat.Title(ctx, ferryTitle.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("by id off: %v", err)
	}
	if _, err := h.cat.TitleBySlug(ctx, "night-ferry"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("by slug off: %v", err)
	}
	if p, err := h.cat.Search(ctx, Query{Page: 1, Size: 10, Genres: []int64{genreID(t, h, "ferries")}}); err != nil || p.Total != 0 {
		t.Fatalf("a filter off: %+v %v", p, err)
	}

	h.cat.ShowHosted(true)
	if names, total := byName(); total != 3 || !slices.Equal(names, on) {
		t.Fatalf("on again: %v", names)
	}
	if _, err := h.cat.Title(ctx, ferryTitle.ID); err != nil {
		t.Fatal(err)
	}
}

func genreID(t *testing.T, h *hostedFixture, slug string) int64 {
	var id int64
	if err := h.cat.db.QueryRow(`SELECT id FROM genres WHERE slug = ?`, slug).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// ── 5. Chapters are removed only on purpose, and named ──

func TestAnEntryWithoutHostedLeavesTheHostedChaptersAlone(t *testing.T) {
	h := newHostedFixture(t)
	h.importDir(folder(t, hostedManifest(), h.files()))
	before := h.chapters(h.hostedSourceTitle("night-ferry").ID)

	// A catalog-only folder with the same ref.
	r := h.importDir(folder(t, obj{"manifestVersion": 1, "sources": sources(), "titles": []obj{{
		"ref": "night-ferry", "name": "The Night Ferry",
		"sourceTitles": []obj{{"source": "paper-moon", "url": "/series/night-ferry/"}},
	}}}, nil))
	if r.Titles.Updated != 1 || r.Chapters.Removed != 0 || len(r.KeptChapters) != 0 {
		t.Fatalf("report: %+v", r)
	}
	ti := h.snapshot()[0]
	if len(ti.Sources) != 2 || !ti.Sources[0].Hosted || ti.Sources[1].SourceSlug != "paper-moon" {
		t.Fatalf("sources: %+v", ti.Sources)
	}
	if after := h.chapters(ti.Sources[0].ID); len(after) != len(before) || after[0].ID != before[0].ID {
		t.Fatalf("chapters: %+v", after)
	}
}

func TestRemovingChaptersNeedsTheFlagAndIsReported(t *testing.T) {
	h := newHostedFixture(t)
	files := h.files()
	m := hostedManifest()
	ti := m["titles"].([]obj)[0]
	ti["hosted"] = append(ti["hosted"].([]obj), obj{"lang": "es", "chapters": []obj{{"ref": "1", "pages": []any{"ch1/01.png"}}}})
	h.importDir(folder(t, m, files))

	// Chapter 2 and the Spanish source title are no longer listed.
	hosted := func() obj { return hostedManifest()["titles"].([]obj)[0]["hosted"].([]obj)[0] }
	short := hostedManifest()
	h0 := hosted()
	h0["chapters"] = h0["chapters"].([]obj)[:1]
	short["titles"].([]obj)[0]["hosted"] = []obj{h0}
	dropped := folder(t, short, files)

	for run := range 2 { // refused every time until the flag is given
		r := h.importDir(dropped)
		if r.Chapters.Removed != 0 || !slices.Equal(r.KeptChapters, []string{"night-ferry/en/2", "night-ferry/es/1"}) {
			t.Fatalf("run %d without the flag: %+v", run, r)
		}
		var n int
		h.cat.db.QueryRow(`SELECT count(*) FROM chapters`).Scan(&n)
		if n != 3 {
			t.Fatalf("run %d: %d chapters left", run, n)
		}
	}
	h.cat.RemoveChapters(true)
	rep := h.importDir(dropped)
	if rep.Chapters.Removed != 2 || !slices.Equal(rep.RemovedChapters, []string{"night-ferry/en/2", "night-ferry/es/1"}) || len(rep.KeptChapters) != 0 {
		t.Fatalf("with the flag: %+v", rep)
	}
	if again := h.importDir(dropped); again.Titles.Unchanged != 1 || again.Chapters.Removed != 0 {
		t.Fatalf("after the removal the entry is unchanged: %+v", again)
	}
}

// ── 9. Descending order keeps the manifest's order for equal numbers ──

func TestEqualNumbersKeepTheManifestOrderBothWays(t *testing.T) {
	h := newHostedFixture(t)
	m := hostedManifest()
	m["titles"].([]obj)[0]["hosted"].([]obj)[0]["chapters"] = []obj{
		{"ref": "a", "number": 2, "pages": []any{"ch1/01.png"}},
		{"ref": "b", "number": 2, "pages": []any{"ch1/02.png"}},
		{"ref": "c", "number": 3, "pages": []any{"ch1/01.png"}},
	}
	h.importDir(folder(t, m, h.files()))
	st := h.hostedSourceTitle("night-ferry").ID
	refsOf := func(asc bool) []string {
		chs, _, err := h.cat.Chapters(context.Background(), st, 1, 10, asc)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range chs {
			var ref string
			h.cat.db.QueryRow(`SELECT ref FROM chapters WHERE id = ?`, c.ID).Scan(&ref)
			out = append(out, ref)
		}
		return out
	}
	if got := refsOf(false); !slices.Equal(got, []string{"c", "a", "b"}) {
		t.Fatalf("desc: %v", got)
	}
	if got := refsOf(true); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("asc: %v", got)
	}
}
