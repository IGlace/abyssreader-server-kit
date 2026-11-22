// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/IGlace/abyssreader-server-kit/internal/covers"
	"github.com/IGlace/abyssreader-server-kit/internal/manifest"
	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

type fixture struct {
	t       *testing.T
	cat     *Catalog
	dataDir string
	clock   time.Time
}

func newFixture(t *testing.T, fetch Fetch) *fixture {
	t.Helper()
	dataDir := t.TempDir()
	db, err := store.Open(context.Background(), filepath.Join(dataDir, "abyssreader.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cs, err := covers.OpenStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if fetch == nil {
		fetch = func(ctx context.Context, url string) (covers.Image, error) {
			t.Errorf("unexpected cover download %s", url)
			return covers.Image{}, errors.New("no downloads in this test")
		}
	}
	f := &fixture{t: t, cat: New(db, cs, fetch), dataDir: dataDir, clock: time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)}
	f.cat.now = func() time.Time { f.clock = f.clock.Add(time.Second); return f.clock }
	return f
}

func pngBytes(t testing.TB, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// folder writes an import folder: manifest.json plus files.
func folder(t *testing.T, m any, files map[string][]byte) string {
	t.Helper()
	dir := t.TempDir()
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(dir, manifest.FileName), b, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func (f *fixture) importDir(dir string) Report {
	f.t.Helper()
	m, err := manifest.Load(dir)
	if err != nil {
		f.t.Fatal(err)
	}
	r, err := f.cat.Import(context.Background(), m)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

type obj = map[string]any

func sources() []obj {
	return []obj{
		{"slug": "paper-moon", "name": "Paper Moon Comics", "website": "https://papermoon.example.com", "lang": "en", "tachiyomiIds": []string{"4815162342"}},
		{"slug": "luna-es", "name": "Luna Cómics", "website": "https://luna.example.net", "lang": "es"},
	}
}

func sampleManifest() obj {
	return obj{
		"manifestVersion": 1,
		"sources":         sources(),
		"titles": []obj{
			{
				"ref": "lantern-keeper", "slug": "the-lantern-keeper", "name": "The Lantern Keeper",
				"altNames": []string{"El guardián del farol"}, "description": "A keeper's apprentice.",
				"status": "ongoing", "genres": []string{"Fantasy", "Slice of Life"},
				"authors": []obj{{"name": "Mira Okafor"}, {"name": "Jun Sato", "role": "artist"}},
				"rating":  8.4, "coverFile": "covers/lantern.png",
				"sourceTitles": []obj{
					{"source": "paper-moon", "url": "/series/the-lantern-keeper/"},
					{"source": "luna-es", "url": "/manga/el-guardian-del-farol"},
				},
			},
			{
				"ref": "tidewater", "name": "Tidewater", "genres": []string{"slice-of-life"},
				"sourceTitles": []obj{{"source": "paper-moon", "url": "/series/tidewater/", "lang": "en"}},
			},
		},
	}
}

// snapshot is every title as the read side returns it, for comparing two catalogs.
func (f *fixture) snapshot() []Title {
	f.t.Helper()
	p, err := f.cat.Search(context.Background(), Query{Page: 1, Size: 100, Sort: SortAlphabetical, Asc: true})
	if err != nil {
		f.t.Fatal(err)
	}
	return p.Titles
}

func TestImportTwiceGivesTheSameCatalog(t *testing.T) {
	f := newFixture(t, nil)
	dir := folder(t, sampleManifest(), map[string][]byte{"covers/lantern.png": pngBytes(t, 30, 45)})

	r := f.importDir(dir)
	if r.Titles.Added != 2 || r.Titles.Updated != 0 || len(r.Failed) != 0 || r.Sources.Added != 2 || r.Covers.Stored != 1 || len(r.CoverFailures) != 0 {
		t.Fatalf("first import: %+v", r)
	}
	first := f.snapshot()
	if len(first) != 2 {
		t.Fatalf("titles: %+v", first)
	}
	lantern := first[0]
	if lantern.Name != "The Lantern Keeper" || lantern.Slug != "the-lantern-keeper" || lantern.CoverImage == "" ||
		!reflect.DeepEqual(lantern.Langs, []string{"en", "es"}) || len(lantern.Sources) != 2 || lantern.Sources[1].Lang != "es" ||
		*lantern.Rating != 8.4 || lantern.Status != "ongoing" || lantern.Authors[0].Role != "author" || lantern.Authors[1].Role != "artist" {
		t.Fatalf("lantern: %+v", lantern)
	}
	// "Slice of Life" and "slice-of-life" are one genre.
	if lantern.Genres[1].ID != first[1].Genres[0].ID || first[1].Genres[0].Slug != "slice-of-life" || first[1].Slug != "tidewater" || first[1].Status != "unknown" {
		t.Fatalf("genres: %+v / %+v", lantern.Genres, first[1].Genres)
	}

	r = f.importDir(dir)
	if r.Titles.Added != 0 || r.Titles.Updated != 0 || r.Titles.Unchanged != 2 || r.Sources.Unchanged != 2 || r.Covers.Stored != 0 || len(r.Failed) != 0 {
		t.Fatalf("second import: %+v", r)
	}
	if second := f.snapshot(); !reflect.DeepEqual(first, second) {
		t.Fatalf("a second import changed the catalog:\n%+v\n%+v", first, second)
	}
	files, _ := os.ReadDir(filepath.Join(f.dataDir, covers.Dir))
	if len(files) != 1 {
		t.Fatalf("one stored cover: %v", files)
	}
}

func TestImportUpdatesByRefAndKeepsSourceTitleIDs(t *testing.T) {
	f := newFixture(t, nil)
	f.importDir(folder(t, sampleManifest(), map[string][]byte{"covers/lantern.png": pngBytes(t, 30, 45)}))
	before := f.snapshot()

	m := sampleManifest()
	titles := m["titles"].([]obj)
	titles[0]["name"] = "The Lantern Keeper (Remastered)"
	titles[0]["sourceTitles"] = []obj{{"source": "luna-es", "url": "/manga/el-guardian-del-farol"}} // paper-moon dropped
	titles[0]["coverFile"] = "covers/new.png"
	r := f.importDir(folder(t, m, map[string][]byte{"covers/new.png": pngBytes(t, 31, 45)}))
	if r.Titles.Updated != 1 || r.Titles.Unchanged != 1 || r.Covers.Stored != 1 {
		t.Fatalf("report: %+v", r)
	}
	after := f.snapshot()
	if len(after) != 2 {
		t.Fatalf("no duplicate: %+v", after)
	}
	var old, now Title
	for _, x := range before {
		if x.Slug == "the-lantern-keeper" {
			old = x
		}
	}
	for _, x := range after {
		if x.Slug == "the-lantern-keeper" {
			now = x
		}
	}
	if now.ID != old.ID || now.Name != "The Lantern Keeper (Remastered)" || !now.UpdatedAt.After(old.UpdatedAt) || !now.CreatedAt.Equal(old.CreatedAt) {
		t.Fatalf("updated in place: %+v → %+v", old, now)
	}
	if len(now.Sources) != 1 || now.Sources[0].ID != old.Sources[1].ID {
		t.Fatalf("the luna-es source title keeps its id: %+v → %+v", old.Sources, now.Sources)
	}
	if now.CoverImage == old.CoverImage {
		t.Fatal("a new cover gets a new name")
	}
	files, _ := os.ReadDir(filepath.Join(f.dataDir, covers.Dir))
	if len(files) != 1 {
		t.Fatalf("the replaced cover's file is deleted: %v", files)
	}
}

func TestImportReportsFailedTitlesAndImportsTheRest(t *testing.T) {
	f := newFixture(t, nil)
	f.importDir(folder(t, sampleManifest(), map[string][]byte{"covers/lantern.png": pngBytes(t, 30, 45)}))

	m := obj{
		"manifestVersion": 1,
		"titles": []obj{
			{"ref": "unknown-source", "name": "A", "sourceTitles": []obj{{"source": "nowhere", "url": "/a"}}},
			{"ref": "slug-taken", "slug": "tidewater", "name": "B", "sourceTitles": []obj{{"source": "paper-moon", "url": "/b"}}},
			{"ref": "steals-a-source-title", "name": "C", "sourceTitles": []obj{{"source": "paper-moon", "url": "/series/tidewater/"}}},
			{"ref": "twice-the-same", "name": "D", "sourceTitles": []obj{{"source": "paper-moon", "url": "/d"}, {"source": "paper-moon", "url": "/d"}}},
			{"ref": "bad-genre", "name": "E", "genres": []string{"!!!"}, "sourceTitles": []obj{{"source": "paper-moon", "url": "/e"}}},
			{"ref": "fine", "name": "Fine", "sourceTitles": []obj{{"source": "luna-es", "url": "/fine"}}}, // earlier sources need not be listed again
			{"ref": "fine", "name": "Fine again", "sourceTitles": []obj{{"source": "luna-es", "url": "/fine-2"}}},
		},
	}
	r := f.importDir(folder(t, m, nil))
	if r.Titles.Added != 1 || len(r.Failed) != 6 {
		t.Fatalf("report: %+v", r)
	}
	want := map[string]string{
		"unknown-source":        `source "nowhere"`,
		"slug-taken":            `slug "tidewater" belongs to the title "tidewater"`,
		"steals-a-source-title": `belongs to the title "tidewater"`,
		"twice-the-same":        "listed twice",
		"bad-genre":             `genre "!!!"`,
		"fine":                  "ref is listed twice",
	}
	for _, fl := range r.Failed {
		if w, ok := want[fl.Ref]; !ok || !strings.Contains(fl.Reason, w) {
			t.Errorf("%s: %q, want %q", fl.Ref, fl.Reason, w)
		}
	}
	if n := len(f.snapshot()); n != 3 {
		t.Fatalf("only the good title was added: %d", n)
	}
}

func TestRemoveTitle(t *testing.T) {
	f := newFixture(t, nil)
	f.importDir(folder(t, sampleManifest(), map[string][]byte{"covers/lantern.png": pngBytes(t, 30, 45)}))
	removed, err := f.cat.Remove(context.Background(), "lantern-keeper")
	if err != nil || removed.Name != "The Lantern Keeper" {
		t.Fatalf("remove: %+v %v", removed, err)
	}
	if titles := f.snapshot(); len(titles) != 1 || titles[0].Slug != "tidewater" {
		t.Fatalf("left: %+v", titles)
	}
	if _, err := f.cat.TitleBySlug(context.Background(), "the-lantern-keeper"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("by slug after remove: %v", err)
	}
	files, _ := os.ReadDir(filepath.Join(f.dataDir, covers.Dir))
	if len(files) != 0 {
		t.Fatalf("its cover is deleted: %v", files)
	}
	if _, err := f.cat.Remove(context.Background(), "lantern-keeper"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removing twice: %v", err)
	}
	// Its source titles are free again: another ref may take them.
	m := sampleManifest()
	m["titles"].([]obj)[0]["ref"] = "lantern-keeper-2"
	if r := f.importDir(folder(t, m, map[string][]byte{"covers/lantern.png": pngBytes(t, 30, 45)})); r.Titles.Added != 1 || len(r.Failed) != 0 {
		t.Fatalf("re-adding under a new ref: %+v", r)
	}
}

func TestACoverFileThatFailsLeavesTheTitleWithoutACover(t *testing.T) {
	f := newFixture(t, nil)
	m := sampleManifest()
	r := f.importDir(folder(t, m, map[string][]byte{"covers/lantern.png": []byte("<html>")}))
	if r.Titles.Added != 2 || len(r.CoverFailures) != 1 || r.CoverFailures[0].Ref != "lantern-keeper" ||
		!strings.Contains(r.CoverFailures[0].Reason, "not a JPEG, PNG or WebP") {
		t.Fatalf("report: %+v", r)
	}
	title, _ := f.cat.TitleBySlug(context.Background(), "the-lantern-keeper")
	if title.CoverImage != "" {
		t.Fatalf("cover: %q", title.CoverImage)
	}
	m["titles"].([]obj)[0]["coverFile"] = "covers/missing.png"
	r = f.importDir(folder(t, m, nil))
	if len(r.CoverFailures) != 1 || !strings.Contains(r.CoverFailures[0].Reason, "covers/missing.png") {
		t.Fatalf("missing file: %+v", r)
	}
}

func TestCoverFilesStayInsideTheFolder(t *testing.T) {
	f := newFixture(t, nil)
	outside := filepath.Join(t.TempDir(), "secret.png")
	os.WriteFile(outside, pngBytes(t, 5, 5), 0o644)
	m := sampleManifest()
	m["titles"].([]obj)[0]["coverFile"] = "covers/link.png"
	dir := folder(t, m, nil)
	os.MkdirAll(filepath.Join(dir, "covers"), 0o755)
	if err := os.Symlink(outside, filepath.Join(dir, "covers", "link.png")); err != nil {
		t.Skip("no symlinks here:", err)
	}
	r := f.importDir(dir)
	if len(r.CoverFailures) != 1 {
		t.Fatalf("a symlink out of the folder must not be read: %+v", r)
	}
}

// coverHost is a local HTTPS server standing in for a cover host. It answers 503 until ready
// is set, then the PNG.
func coverHost(t *testing.T, ready *atomic.Bool) (*httptest.Server, Fetch) {
	t.Helper()
	cover := pngBytes(t, 40, 60)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() || r.URL.Path != "/covers/tidewater.png" {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(cover)
	}))
	t.Cleanup(srv.Close)
	fetcher := covers.NewFetcher(covers.FetchOptions{
		AllowPrivateAddresses: true,
		RootCAs:               srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs,
	})
	return srv, fetcher.Fetch
}

func TestAFailedCoverURLIsReportedAndRetried(t *testing.T) {
	var ready atomic.Bool
	srv, fetch := coverHost(t, &ready)
	f := newFixture(t, fetch)
	m := sampleManifest()
	m["titles"].([]obj)[1]["coverUrl"] = srv.URL + "/covers/tidewater.png"
	dir := folder(t, m, map[string][]byte{"covers/lantern.png": pngBytes(t, 30, 45)})

	r := f.importDir(dir)
	if r.Titles.Added != 2 || r.Covers.Stored != 1 || len(r.CoverFailures) != 1 || r.CoverFailures[0].Ref != "tidewater" ||
		!strings.Contains(r.CoverFailures[0].Reason, "503") {
		t.Fatalf("report: %+v", r)
	}
	tide, _ := f.cat.TitleBySlug(context.Background(), "tidewater")
	if tide.CoverImage != "" {
		t.Fatal("no cover yet")
	}

	// Still failing: reported again, nothing changes.
	rr, err := f.cat.RetryCovers(context.Background())
	if err != nil || rr.Stored != 0 || len(rr.Failures) != 1 {
		t.Fatalf("retry while failing: %+v %v", rr, err)
	}

	ready.Store(true)
	rr, err = f.cat.RetryCovers(context.Background())
	if err != nil || rr.Stored != 1 || len(rr.Failures) != 0 {
		t.Fatalf("retry: %+v %v", rr, err)
	}
	after, _ := f.cat.TitleBySlug(context.Background(), "tidewater")
	if after.CoverImage == "" || !after.UpdatedAt.After(tide.UpdatedAt) {
		t.Fatalf("the cover is stored and the title changed: %+v", after)
	}
	if rr, _ = f.cat.RetryCovers(context.Background()); rr.Stored != 0 || len(rr.Failures) != 0 {
		t.Fatalf("nothing left to retry: %+v", rr)
	}
	// A re-import of the same manifest downloads nothing again.
	ready.Store(false)
	if r := f.importDir(dir); r.Covers.Stored != 0 || len(r.CoverFailures) != 0 {
		t.Fatalf("re-import: %+v", r)
	}
}

func TestImportStoresTachiyomiIDs(t *testing.T) {
	f := newFixture(t, nil)
	f.importDir(folder(t, sampleManifest(), map[string][]byte{"covers/lantern.png": pngBytes(t, 30, 45)}))
	ids, err := f.cat.TachiyomiIDs(context.Background(), "paper-moon")
	if err != nil || !reflect.DeepEqual(ids, []string{"4815162342"}) {
		t.Fatalf("ids: %v %v", ids, err)
	}
}

// Each cover is stored and recorded as soon as it is downloaded: a long batch keeps its
// progress when it is interrupted, and a re-run downloads only what is still missing.
func TestCoversAreStoredOneByOneAndARerunFetchesOnlyTheMissing(t *testing.T) {
	cover := pngBytes(t, 20, 30)
	var f *fixture
	var slowCalls, fastCalls atomic.Int32
	fetch := func(ctx context.Context, url string) (covers.Image, error) {
		if strings.HasSuffix(url, "/fast.png") {
			fastCalls.Add(1)
			return covers.Check(cover)
		}
		slowCalls.Add(1)
		// The slow cover waits until the fast one is in the catalog, then the batch is cut off.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if ti, err := f.cat.TitleBySlug(context.Background(), "fast"); err == nil && ti.CoverImage != "" {
				return covers.Image{}, errors.New("interrupted")
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Error("the fast cover was not recorded while the slow one was still downloading")
		return covers.Image{}, errors.New("timeout")
	}
	f = newFixture(t, fetch)
	m := obj{
		"manifestVersion": 1,
		"sources":         sources(),
		"titles": []obj{
			{"ref": "slow", "name": "Slow", "coverUrl": "https://covers.example.org/slow.png", "sourceTitles": []obj{{"source": "paper-moon", "url": "/slow"}}},
			{"ref": "fast", "name": "Fast", "coverUrl": "https://covers.example.org/fast.png", "sourceTitles": []obj{{"source": "paper-moon", "url": "/fast"}}},
		},
	}
	r := f.importDir(folder(t, m, nil))
	if r.Covers.Stored != 1 || len(r.CoverFailures) != 1 || r.CoverFailures[0].Ref != "slow" {
		t.Fatalf("report: %+v", r)
	}
	slowCalls.Store(0)
	fastCalls.Store(0)
	if _, err := f.cat.RetryCovers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fastCalls.Load() != 0 || slowCalls.Load() != 1 {
		t.Fatalf("a re-run downloads only the missing cover: fast %d, slow %d", fastCalls.Load(), slowCalls.Load())
	}
}

// A cancelled import stops taking new downloads and keeps what it stored.
func TestACancelledImportKeepsTheCoversItStored(t *testing.T) {
	cover := pngBytes(t, 20, 30)
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	fetch := func(c context.Context, url string) (covers.Image, error) {
		if calls.Add(1) == 1 {
			return covers.Check(cover)
		}
		cancel()
		<-c.Done()
		return covers.Image{}, c.Err()
	}
	f := newFixture(t, fetch)
	var titles []obj
	for i := range 20 {
		titles = append(titles, obj{"ref": fmt.Sprintf("t%02d", i), "name": fmt.Sprintf("T%02d", i),
			"coverUrl":     fmt.Sprintf("https://covers.example.org/%d.png", i),
			"sourceTitles": []obj{{"source": "paper-moon", "url": fmt.Sprintf("/t%d", i)}}})
	}
	m, err := manifest.Parse(mustJSON(obj{"manifestVersion": 1, "sources": sources(), "titles": titles}))
	if err != nil {
		t.Fatal(err)
	}
	m.Dir = t.TempDir()
	if _, err := f.cat.Import(ctx, m); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled import: %v", err)
	}
	if n := calls.Load(); n > 1+coverWorkers {
		t.Fatalf("%d downloads started after the cancel", n)
	}
	stored := 0
	for _, ti := range f.snapshot() {
		if ti.CoverImage != "" {
			stored++
		}
	}
	if stored != 1 {
		t.Fatalf("the cover downloaded before the cancel is kept: %d stored", stored)
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// A source title without its own lang follows its source's: changing only the source's lang
// updates the title.
func TestChangingASourcesLangUpdatesItsSourceTitles(t *testing.T) {
	f := newFixture(t, nil)
	files := map[string][]byte{"covers/lantern.png": pngBytes(t, 30, 45)}
	f.importDir(folder(t, sampleManifest(), files))
	m := sampleManifest()
	m["sources"].([]obj)[0]["lang"] = "fr" // paper-moon
	r := f.importDir(folder(t, m, files))
	if r.Sources.Updated != 1 || r.Titles.Updated != 1 || r.Titles.Unchanged != 1 {
		t.Fatalf("report: %+v", r)
	}
	tide, _ := f.cat.TitleBySlug(context.Background(), "tidewater") // its lang is given: unchanged
	lantern, _ := f.cat.TitleBySlug(context.Background(), "the-lantern-keeper")
	if tide.Sources[0].Lang != "en" || lantern.Sources[0].Lang != "fr" || !reflect.DeepEqual(lantern.Langs, []string{"fr", "es"}) {
		t.Fatalf("langs: tidewater %+v, lantern %+v", tide.Sources, lantern.Sources)
	}
}

// A genre slug never exceeds the contract's 255 characters: lower-casing maps one character to
// one (İ → i), and other characters become at most one dash each. Import keeps a guard anyway.
func TestGenreSlugsStayWithinTheLimit(t *testing.T) {
	for _, name := range []string{strings.Repeat("İ", 255), strings.Repeat("Ⱥ", 255), strings.Repeat("ẞ", 255), strings.Repeat("a ", 127) + "a"} {
		if n := utf8.RuneCountInString(Slugify(name)); n > maxSlug {
			t.Errorf("%q…: a slug of %d characters", shorten(name), n)
		}
	}
	f := newFixture(t, nil)
	m := sampleManifest()
	m["titles"].([]obj)[1]["genres"] = []string{strings.Repeat("İ", 255)}
	r := f.importDir(folder(t, m, map[string][]byte{"covers/lantern.png": pngBytes(t, 30, 45)}))
	if r.Titles.Added != 2 || len(r.Failed) != 0 {
		t.Fatalf("report: %+v", r)
	}
	tide, _ := f.cat.TitleBySlug(context.Background(), "tidewater")
	if utf8.RuneCountInString(tide.Genres[0].Slug) != 255 {
		t.Fatalf("slug: %d characters", utf8.RuneCountInString(tide.Genres[0].Slug))
	}
}
