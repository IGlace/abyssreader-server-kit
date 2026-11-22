// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package catalog

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/covers"
	"github.com/IGlace/abyssreader-server-kit/internal/manifest"
	"github.com/IGlace/abyssreader-server-kit/internal/pages"
)

// pagePNG is a w×h page whose pixels depend on seed, so two seeds are two different files.
func pagePNG(t testing.TB, w, h int, seed uint8) []byte {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = seed + uint8(i/w/97) // rows differ, so no two slices of a strip are the same file
	}
	img.SetGray(0, 0, color.Gray{seed + 1})
	return pngOf(t, img)
}

func pngOf(t testing.TB, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

type hostedFixture struct {
	*fixture
	pages    *covers.Store
	prepared int // how many page files were read and checked (not reused)
}

func newHostedFixture(t *testing.T) *hostedFixture {
	t.Helper()
	f := newFixture(t, nil)
	ps, err := pages.OpenStore(f.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	h := &hostedFixture{fixture: f, pages: ps}
	f.cat.UsePages(ps).ShowHosted(true)
	prepare := f.cat.prepare
	f.cat.prepare = func(root *os.Root, path string, w, hh int, s *covers.Store) (pages.Source, error) {
		h.prepared++
		return prepare(root, path, w, hh, s)
	}
	return h
}

func hostedManifest() obj {
	return obj{
		"manifestVersion": 1,
		"titles": []obj{{
			"ref": "night-ferry", "name": "The Night Ferry", "genres": []string{"Fantasy"},
			"hosted": []obj{{
				"lang": "en", "readingMode": "long_strip",
				"rights": obj{"holder": "Ada Lind", "licence": "CC BY 4.0", "licenceUrl": "https://creativecommons.org/licenses/by/4.0/",
					"attribution": "\"The Night Ferry\" by Ada Lind, CC BY 4.0"},
				"chapters": []obj{
					{"ref": "1", "number": 1, "name": "Departure", "publishedAt": "2026-11-01", "credits": "Lettering by Bo",
						"pages": []any{"ch1/01.png", obj{"file": "ch1/02.png", "width": 400, "height": 600}}},
					{"ref": "2", "number": 2, "publishedAt": "2026-11-08", "pages": []any{"ch2/strip.png"}},
				},
			}},
		}},
	}
}

func (h *hostedFixture) files() map[string][]byte {
	return map[string][]byte{
		"ch1/01.png":    pagePNG(h.t, 400, 600, 10),
		"ch1/02.png":    pagePNG(h.t, 400, 600, 20),
		"ch2/strip.png": pagePNG(h.t, 300, 13000, 30),
	}
}

// chapterView is everything the read side serves of one hosted source title.
type chapterView struct {
	Chapter
	Pages []ChapterPage
}

func (h *hostedFixture) hostedSourceTitle(ref string) SourceTitle {
	h.t.Helper()
	for _, ti := range h.snapshot() {
		for _, st := range ti.Sources {
			if st.Hosted && ti.Slug == ref {
				return st
			}
		}
	}
	h.t.Fatalf("no hosted source title on %s", ref)
	return SourceTitle{}
}

func (h *hostedFixture) chapters(stID int64) []chapterView {
	h.t.Helper()
	chs, total, err := h.cat.Chapters(context.Background(), stID, 1, 100, true)
	if err != nil {
		h.t.Fatal(err)
	}
	if int(total) != len(chs) {
		h.t.Fatalf("total %d, %d chapters", total, len(chs))
	}
	out := make([]chapterView, len(chs))
	for i, c := range chs {
		ps, err := h.cat.ChapterPages(context.Background(), c.ID)
		if err != nil {
			h.t.Fatal(err)
		}
		out[i] = chapterView{c, ps}
		for _, p := range ps {
			if !h.pages.Has(p.Image) {
				h.t.Errorf("chapter %d: page %s is not stored", c.ID, p.Image)
			}
		}
	}
	return out
}

func storedPages(t *testing.T, dataDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, pages.Dir))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func TestImportAHostedTitleTwiceGivesTheSameChaptersAndPages(t *testing.T) {
	h := newHostedFixture(t)
	dir := folder(t, hostedManifest(), h.files())
	r := h.importDir(dir)
	if r.Titles.Added != 1 || r.Chapters.Added != 2 || r.Pages.Stored != 3 || len(r.ChapterFailures) != 0 || len(r.Failed) != 0 {
		t.Fatalf("first import: %+v", r)
	}
	st := h.hostedSourceTitle("night-ferry")
	if st.SourceSlug != manifest.HostedSourceSlug || st.URL != "/source-titles/"+itoa(st.ID) || st.Lang != "en" ||
		*st.ReadingMode != "long_strip" || *st.Rights.Holder != "Ada Lind" || st.Rights.Changes != nil {
		t.Fatalf("hosted source title: %+v", st)
	}
	first := h.chapters(st.ID)
	if len(first) != 2 || len(first[0].Pages) != 2 || len(first[1].Pages) != 3 || first[0].PageCount != 2 || first[1].PageCount != 3 {
		t.Fatalf("chapters: %+v", first)
	}
	if c := first[0]; *c.Number != 1 || *c.Name != "Departure" || *c.Credits != "Lettering by Bo" || c.Volume != nil ||
		c.PublishedAt.Format("2006-01-02") != "2026-11-01" || c.Pages[1].Width != 400 || c.Pages[1].Height != 600 {
		t.Fatalf("chapter 1: %+v", c)
	}
	if prepared := h.prepared; prepared != 3 {
		t.Fatalf("%d page files read on the first import", prepared)
	}
	stored := storedPages(t, h.dataDir)

	r = h.importDir(dir)
	if r.Titles.Unchanged != 1 || r.Chapters.Unchanged != 2 || r.Chapters.Added+r.Chapters.Updated != 0 || r.Pages.Stored != 0 {
		t.Fatalf("second import: %+v", r)
	}
	if h.prepared != 3 {
		t.Fatalf("the second import read %d page files again", h.prepared-3)
	}
	if again := h.chapters(st.ID); !reflect.DeepEqual(again, first) {
		t.Fatalf("second import changed the chapters:\n%+v\n%+v", first, again)
	}
	if again := storedPages(t, h.dataDir); !slices.Equal(again, stored) {
		t.Fatalf("stored files changed: %v → %v", stored, again)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestALongStripIsCutIntoPagesOfItsDeclaredSize(t *testing.T) {
	h := newHostedFixture(t)
	h.importDir(folder(t, hostedManifest(), h.files()))
	strip := h.chapters(h.hostedSourceTitle("night-ferry").ID)[1]
	total := 0
	for _, p := range strip.Pages {
		if p.Width != 300 || p.Height > pages.MaxHeight {
			t.Errorf("slice %+v", p)
		}
		total += int(p.Height)
		f, _, err := h.pages.Open(p.Image)
		if err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 64<<10)
		n, _ := f.Read(b)
		f.Close()
		_, cfg, err := covers.Sniff(b[:n])
		if err != nil || int64(cfg.Width) != p.Width || int64(cfg.Height) != p.Height {
			t.Errorf("stored %s is %dx%d (%v), served as %dx%d", p.Image, cfg.Width, cfg.Height, err, p.Width, p.Height)
		}
	}
	if total != 13000 {
		t.Fatalf("slices add up to %d px", total)
	}
}

func TestReplacingOnePageFileGivesThatPageANewImage(t *testing.T) {
	h := newHostedFixture(t)
	files := h.files()
	dir := folder(t, hostedManifest(), files)
	h.importDir(dir)
	st := h.hostedSourceTitle("night-ferry")
	before := h.chapters(st.ID)

	os.WriteFile(filepath.Join(dir, "ch1", "02.png"), pagePNG(t, 400, 600, 99), 0o644)
	r := h.importDir(dir)
	if r.Chapters.Updated != 1 || r.Chapters.Unchanged != 1 || r.Pages.Stored != 1 {
		t.Fatalf("report: %+v", r)
	}
	after := h.chapters(st.ID)
	if after[0].ID != before[0].ID || after[0].Pages[0] != before[0].Pages[0] || after[0].Pages[1].Image == before[0].Pages[1].Image {
		t.Fatalf("pages: %+v → %+v", before[0].Pages, after[0].Pages)
	}
	if !reflect.DeepEqual(after[1], before[1]) {
		t.Fatal("the other chapter changed")
	}
	if h.pages.Has(before[0].Pages[1].Image) {
		t.Fatal("the replaced page's old image is still stored")
	}
}

func TestPagesOutsideTheLimitsFailOnlyTheirChapter(t *testing.T) {
	h := newHostedFixture(t)
	files := h.files()
	dir := folder(t, hostedManifest(), files)
	h.importDir(dir)
	st := h.hostedSourceTitle("night-ferry")
	before := h.chapters(st.ID)

	// Chapter 1's first page becomes too wide; a new chapter 3 is fine.
	m := hostedManifest()
	hosted := m["titles"].([]obj)[0]["hosted"].([]obj)[0]
	hosted["chapters"] = append(hosted["chapters"].([]obj), obj{"ref": "3", "number": 3, "pages": []any{"ch3/01.png"}})
	files["ch1/01.png"] = pagePNG(t, 2400, 600, 10)
	files["ch3/01.png"] = pagePNG(t, 400, 600, 40)
	r := h.importDir(folder(t, m, files))
	if len(r.ChapterFailures) != 1 || r.Chapters.Added != 1 {
		t.Fatalf("report: %+v", r)
	}
	f := r.ChapterFailures[0]
	if f.Ref != "night-ferry/en/1" || !strings.Contains(f.Reason, "page 1, ch1/01.png: 2400×600 pixels: a page is at most 2000 px wide") {
		t.Fatalf("failure: %+v", f)
	}
	after := h.chapters(st.ID)
	if len(after) != 3 || !reflect.DeepEqual(after[0], before[0]) {
		t.Fatalf("a failed chapter keeps what it had: %+v", after)
	}
	// A declared size that does not match fails the chapter too.
	files["ch1/01.png"] = pagePNG(t, 400, 600, 10)
	files["ch1/02.png"] = pagePNG(t, 400, 601, 20)
	r = h.importDir(folder(t, m, files))
	if len(r.ChapterFailures) != 1 || !strings.Contains(r.ChapterFailures[0].Reason, "declared height 600, the file is 601 px tall") {
		t.Fatalf("declared size: %+v", r.ChapterFailures)
	}
}

func TestChaptersAndLanguagesTheManifestDropsAreRemoved(t *testing.T) {
	h := newHostedFixture(t)
	files := h.files()
	m := hostedManifest()
	ti := m["titles"].([]obj)[0]
	ti["hosted"] = append(ti["hosted"].([]obj), obj{"lang": "es", "chapters": []obj{{"ref": "1", "pages": []any{"ch1/01.png"}}}})
	h.importDir(folder(t, m, files))
	if n := len(storedPages(t, h.dataDir)); n != 5 {
		t.Fatalf("%d stored pages", n)
	}

	m = hostedManifest()
	hosted := m["titles"].([]obj)[0]["hosted"].([]obj)[0]
	hosted["chapters"] = hosted["chapters"].([]obj)[:1]
	h.cat.RemoveChapters(true)
	r := h.importDir(folder(t, m, files))
	if r.Titles.Updated != 1 || r.Chapters.Removed != 2 {
		t.Fatalf("report: %+v", r)
	}
	var hostedSTs []SourceTitle
	for _, st := range h.snapshot()[0].Sources {
		if st.Hosted {
			hostedSTs = append(hostedSTs, st)
		}
	}
	if len(hostedSTs) != 1 || hostedSTs[0].Lang != "en" || len(h.chapters(hostedSTs[0].ID)) != 1 {
		t.Fatalf("hosted source titles: %+v", hostedSTs)
	}
	// Only chapter 1's two pages are left: the strip's slices went with chapter 2.
	if n := len(storedPages(t, h.dataDir)); n != 2 {
		t.Fatalf("%d stored pages left", n)
	}
}

func TestRemovingAHostedTitleRemovesItsChaptersAndUnusedPageFiles(t *testing.T) {
	h := newHostedFixture(t)
	files := h.files()
	m := hostedManifest()
	// A second title uses one of the same page files.
	m["titles"] = append(m["titles"].([]obj), obj{
		"ref": "day-ferry", "name": "The Day Ferry",
		"hosted": []obj{{"lang": "en", "chapters": []obj{{"ref": "1", "pages": []any{"ch1/01.png"}}}}},
	})
	h.importDir(folder(t, m, files))
	shared := h.chapters(h.hostedSourceTitle("day-ferry").ID)[0].Pages[0].Image

	if _, err := h.cat.Remove(context.Background(), "night-ferry"); err != nil {
		t.Fatal(err)
	}
	if got := storedPages(t, h.dataDir); !slices.Equal(got, []string{shared}) {
		t.Fatalf("stored pages after the removal: %v, want only %s", got, shared)
	}
	var n int
	h.cat.db.QueryRow(`SELECT (SELECT count(*) FROM chapters) + (SELECT count(*) FROM chapter_pages) + (SELECT count(*) FROM page_files) + (SELECT count(*) FROM page_images)`).Scan(&n)
	if n != 4 { // day-ferry: 1 chapter, 1 chapter page, 1 page file, 1 image
		t.Fatalf("%d hosted rows left", n)
	}
}

func TestAnInterruptedImportKeepsThePagesItStored(t *testing.T) {
	h := newHostedFixture(t)
	dir := folder(t, hostedManifest(), h.files())
	m, err := manifest.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	prepare := h.cat.prepare
	h.cat.prepare = func(root *os.Root, path string, w, hh int, s *covers.Store) (pages.Source, error) {
		if h.prepared == 2 {
			cancel() // the import is stopped after two page files
			return pages.Source{}, ctx.Err()
		}
		return prepare(root, path, w, hh, s)
	}
	if _, err := h.cat.Import(ctx, m); !errors.Is(err, context.Canceled) {
		t.Fatalf("an interrupted import: %v", err)
	}
	if h.prepared != 2 {
		t.Fatalf("%d page files read before the stop", h.prepared)
	}
	h.cat.prepare = prepare
	r := h.importDir(dir)
	// The re-run reads only the page file it had not stored.
	if h.prepared != 3 || r.Chapters.Added+r.Chapters.Updated+r.Chapters.Unchanged != 2 {
		t.Fatalf("re-run read %d files in all: %+v", h.prepared, r)
	}
	if len(h.chapters(h.hostedSourceTitle("night-ferry").ID)) != 2 {
		t.Fatal("the re-run did not finish the title")
	}
}

func TestSwitchedOffHostedSourcesAreHiddenAndKept(t *testing.T) {
	h := newHostedFixture(t)
	m := hostedManifest()
	m["sources"] = sources()
	m["titles"].([]obj)[0]["sourceTitles"] = []obj{{"source": "paper-moon", "url": "/series/night-ferry/"}}
	h.importDir(folder(t, m, h.files()))

	ctx := context.Background()
	srcs, _ := h.cat.Sources(ctx)
	if len(srcs) != 2 || !srcs[0].Hosted && !srcs[1].Hosted {
		t.Fatalf("sources on: %+v", srcs)
	}
	if ti := h.snapshot()[0]; len(ti.Sources) != 2 || !ti.Sources[0].Hosted {
		t.Fatalf("the hosted source title comes first: %+v", ti.Sources)
	}
	if p, err := h.cat.Search(ctx, Query{Page: 1, Size: 10, Sources: []string{"hosted"}}); err != nil || p.Total != 1 {
		t.Fatalf("filter by the hosted source: %+v %v", p, err)
	}

	h.cat.ShowHosted(false)
	srcs, _ = h.cat.Sources(ctx)
	if len(srcs) != 1 || srcs[0].Hosted {
		t.Fatalf("sources off: %+v", srcs)
	}
	if ti := h.snapshot()[0]; len(ti.Sources) != 1 || ti.Sources[0].Hosted || !slices.Equal(ti.Langs, []string{"en"}) {
		t.Fatalf("title off: %+v", ti.Sources)
	}
	var fe *FilterError
	if _, err := h.cat.Search(ctx, Query{Page: 1, Size: 10, Sources: []string{"hosted"}}); !errors.As(err, &fe) {
		t.Fatalf("filter by a switched-off hosted source: %v", err)
	}

	h.cat.ShowHosted(true)
	if ti := h.snapshot()[0]; len(ti.Sources) != 2 || len(h.chapters(ti.Sources[0].ID)) != 2 {
		t.Fatal("switched on again, the hosted chapters are back")
	}
}
