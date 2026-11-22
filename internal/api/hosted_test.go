// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/catalog"
	"github.com/IGlace/abyssreader-server-kit/internal/config"
	"github.com/IGlace/abyssreader-server-kit/internal/contracttest"
	"github.com/IGlace/abyssreader-server-kit/internal/covers"
	"github.com/IGlace/abyssreader-server-kit/internal/manifest"
)

func hostedOn(c *config.Config) { c.Features = []string{config.HostedChapters} }

func pagePNG(t testing.TB, w, h int, seed uint8) []byte {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = seed + uint8(i/w/97)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// importHosted imports a manifest with hosted titles, as `admin import` does.
func (h *harness) importHosted(m obj, files map[string][]byte) catalog.Report {
	h.t.Helper()
	dir := h.t.TempDir()
	b, _ := json.Marshal(m)
	os.WriteFile(filepath.Join(dir, manifest.FileName), b, 0o644)
	for name, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, data, 0o644)
	}
	mf, err := manifest.Load(dir)
	if err != nil {
		h.t.Fatal(err)
	}
	noDownloads := func(context.Context, string) (covers.Image, error) { return covers.Image{}, errors.New("offline") }
	r, err := catalog.New(h.db, h.covers, noDownloads).UsePages(h.pages).Import(context.Background(), mf)
	if err != nil {
		h.t.Fatal(err)
	}
	if len(r.Failed)+len(r.ChapterFailures) > 0 {
		h.t.Fatalf("import failures: %+v %+v", r.Failed, r.ChapterFailures)
	}
	return r
}

// hostedCatalog: one catalog title and one hosted title, "The Night Ferry", with five chapters
// (numbers 1, 2, 2.5, 3 and none) and a long strip in chapter 3.
func (h *harness) hostedCatalog() {
	h.t.Helper()
	files := map[string][]byte{"covers/ferry.png": coverPNG(h.t, 60), "strip.png": pagePNG(h.t, 300, 7000, 50)}
	var chapters []obj
	for i, n := range []any{1, 2, 2.5, 3, nil} {
		p := fmt.Sprintf("ch%d/01.png", i)
		files[p] = pagePNG(h.t, 400, 600, uint8(i*10))
		pageList := []any{p}
		if n == 3 {
			pageList = append(pageList, obj{"file": "strip.png", "width": 300, "height": 7000})
		}
		ch := obj{"ref": fmt.Sprint("c", i), "number": n, "publishedAt": fmt.Sprintf("2026-11-%02d", i+1), "pages": pageList}
		if n == nil {
			ch["name"] = "Extra: the crew"
			ch["credits"] = "Translated by Bo"
		}
		chapters = append(chapters, ch)
	}
	h.importHosted(obj{
		"manifestVersion": 1,
		"sources":         catalogSources,
		"titles": []obj{
			{"ref": "tidewater", "name": "Tidewater", "sourceTitles": []obj{{"source": "paper-moon", "url": "/series/tidewater/"}}},
			{
				"ref": "night-ferry", "name": "The Night Ferry", "coverFile": "covers/ferry.png",
				"sourceTitles": []obj{{"source": "paper-moon", "url": "/series/night-ferry/"}},
				"hosted": []obj{{
					"lang": "en", "readingMode": "long_strip",
					"rights":   obj{"holder": "Ada Lind", "licence": "CC BY 4.0", "licenceUrl": "https://creativecommons.org/licenses/by/4.0/", "attribution": "\"The Night Ferry\" by Ada Lind"},
					"chapters": chapters,
				}},
			},
		},
	}, files)
}

// ferry returns the hosted title's details and its hosted source title.
func (h *harness) ferry(r req) (answer, map[string]any) {
	h.t.Helper()
	a := h.do("GET", "/api/v1/titles/by-slug/night-ferry?countView=false", r)
	h.expect(a, 200, "")
	for _, st := range a.body["sources"].([]any) {
		if st := st.(map[string]any); st["sourceSlug"] == "hosted" {
			return a, st
		}
	}
	return a, nil
}

func chapterNumbers(a answer) []string {
	var out []string
	for _, it := range a.body["items"].([]any) {
		if n := it.(map[string]any)["number"]; n == nil {
			out = append(out, "-")
		} else {
			out = append(out, strconv.FormatFloat(n.(float64), 'f', -1, 64))
		}
	}
	return out
}

func TestTheServerListsItselfAsTheHostedSource(t *testing.T) {
	h := newHarness(t, hostedOn)
	h.hostedCatalog()
	a := h.do("GET", "/api/v1/sources", req{})
	h.expect(a, 200, "")
	want := `{"slug":"hosted","name":"Lantern Library","iconUrl":null,"lang":"en","website":"https://lantern.example.org","hosted":true}`
	if !strings.Contains(string(a.raw), want) {
		t.Fatalf("sources: %s", a.raw)
	}
	title, st := h.ferry(req{})
	if st == nil || title.body["sources"].([]any)[0].(map[string]any)["sourceSlug"] != "hosted" {
		t.Fatalf("the hosted source title comes first: %s", title.raw)
	}
	id := int64(st["id"].(float64))
	if st["url"] != fmt.Sprintf("/source-titles/%d", id) || st["hasDefinition"] != false || st["readingMode"] != "long_strip" || st["lang"] != "en" {
		t.Fatalf("hosted source title: %v", st)
	}
	rights := st["rights"].(map[string]any)
	if rights["holder"] != "Ada Lind" || rights["licence"] != "CC BY 4.0" || rights["changes"] != nil || len(rights) != 5 {
		t.Fatalf("rights: %v", rights)
	}
	other := title.body["sources"].([]any)[1].(map[string]any)
	if other["readingMode"] != nil || other["rights"] != nil {
		t.Fatalf("a source title that is not hosted has no reader hint or rights: %v", other)
	}
	// The catalog filter knows the hosted source.
	a = h.do("GET", "/api/v1/titles?source=hosted", req{})
	h.expect(a, 200, "")
	if a.body["totalItems"].(float64) != 1 {
		t.Fatalf("source=hosted: %s", a.raw)
	}
}

func TestChaptersPageSortAndOrder(t *testing.T) {
	h := newHarness(t, hostedOn)
	h.hostedCatalog()
	_, st := h.ferry(req{})
	base := fmt.Sprintf("/api/v1/source-titles/%d/chapters", int64(st["id"].(float64)))

	a := h.do("GET", base, req{})
	h.expect(a, 200, "")
	if got := chapterNumbers(a); !slices.Equal(got, []string{"3", "2.5", "2", "1", "-"}) || a.body["totalItems"].(float64) != 5 ||
		a.body["hasMore"] != false || a.body["size"].(float64) != 100 || a.body["page"].(float64) != 1 {
		t.Fatalf("default (number, desc): %v %s", got, a.raw)
	}
	first := a.body["items"].([]any)[0].(map[string]any)
	if first["url"] != fmt.Sprintf("/chapters/%d", int64(first["id"].(float64))) || first["pageCount"].(float64) != 3 ||
		first["publishedAt"] != "2026-11-04T00:00:00.000Z" || first["name"] != nil || first["volume"] != nil || first["credits"] != nil {
		t.Fatalf("chapter 3: %v", first)
	}
	extra := a.body["items"].([]any)[4].(map[string]any)
	if extra["name"] != "Extra: the crew" || extra["credits"] != "Translated by Bo" {
		t.Fatalf("the extra: %v", extra)
	}
	a = h.do("GET", base+"?sort=number&order=asc", req{})
	if got := chapterNumbers(a); !slices.Equal(got, []string{"1", "2", "2.5", "3", "-"}) {
		t.Fatalf("asc: %v", got)
	}
	// Paging: no duplicates, no gaps, hasMore until the last page.
	var seen []string
	for page := 1; page <= 3; page++ {
		a = h.do("GET", fmt.Sprintf("%s?size=2&page=%d", base, page), req{})
		h.expect(a, 200, "")
		seen = append(seen, chapterNumbers(a)...)
		if a.body["hasMore"] != (page < 3) || a.body["totalItems"].(float64) != 5 || a.body["size"].(float64) != 2 {
			t.Fatalf("page %d: %s", page, a.raw)
		}
	}
	if !slices.Equal(seen, []string{"3", "2.5", "2", "1", "-"}) {
		t.Fatalf("paged: %v", seen)
	}
	a = h.do("GET", base+"?size=2&page=9", req{})
	if len(a.body["items"].([]any)) != 0 || a.body["hasMore"] != false {
		t.Fatalf("past the end: %s", a.raw)
	}

	for query, field := range map[string]string{
		"size=101": "size", "size=0": "size", "page=0": "page", "sort=published": "sort", "order=up": "order",
		"size=2&size=3": "size", "page=x": "page", "sort=number&sort=number": "sort",
	} {
		a := h.do("GET", base+"?"+query, req{})
		h.expect(a, 400, "validation_failed")
		if !strings.Contains(fieldCodes(a), field+":") {
			t.Errorf("%s: %s", query, a.raw)
		}
	}
	h.expect(h.do("GET", "/api/v1/source-titles/abc/chapters", req{}), 400, "validation_failed")
	h.expect(h.do("GET", "/api/v1/source-titles/0/chapters", req{}), 400, "validation_failed")
	h.expect(h.do("GET", "/api/v1/source-titles/99999/chapters", req{}), 404, "not_found")
	// A source title on another source has no chapters here.
	tide := h.do("GET", "/api/v1/titles/by-slug/tidewater?countView=false", req{})
	other := int64(tide.body["sources"].([]any)[0].(map[string]any)["id"].(float64))
	h.expect(h.do("GET", fmt.Sprintf("/api/v1/source-titles/%d/chapters", other), req{}), 404, "not_found")
}

// imageAnswer fetches an image URL of the server with no headers at all.
func (h *harness) image(method, url string) (*http.Response, []byte) {
	h.t.Helper()
	path := strings.TrimPrefix(url, h.cfg.PublicURL)
	hr, _ := http.NewRequest(method, h.srv.URL+path, nil)
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		h.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, b
}

func TestChapterPagesAndTheirImages(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { hostedOn(c); c.AccessKey = "lantern-access-key-1" })
	h.hostedCatalog()
	key := req{accessKey: "lantern-access-key-1"}
	_, st := h.ferry(key)
	chs := h.do("GET", fmt.Sprintf("/api/v1/source-titles/%d/chapters", int64(st["id"].(float64))), key)
	strip := int64(chs.body["items"].([]any)[0].(map[string]any)["id"].(float64)) // number 3: a page and a 7000 px strip

	a := h.do("GET", fmt.Sprintf("/api/v1/chapters/%d/pages", strip), key)
	h.expect(a, 200, "")
	items := a.body["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("pages: %s", a.raw)
	}
	total := 0
	for i, it := range items {
		p := it.(map[string]any)
		url := p["imageUrl"].(string)
		if p["index"].(float64) != float64(i) || !strings.HasPrefix(url, "https://lantern.example.org/pages/") {
			t.Fatalf("page %d: %v", i, p)
		}
		resp, body := h.image("GET", url)
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" || resp.Header.Get("X-Content-Type-Options") != "nosniff" ||
			resp.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Fatalf("image %s: %d %v", url, resp.StatusCode, resp.Header)
		}
		_, cfg, err := covers.Sniff(body)
		if err != nil || float64(cfg.Width) != p["width"].(float64) || float64(cfg.Height) != p["height"].(float64) {
			t.Fatalf("page %d is %dx%d (%v), declared %v×%v", i, cfg.Width, cfg.Height, err, p["width"], p["height"])
		}
		if i > 0 {
			total += cfg.Height
		}
		if resp, body := h.image("HEAD", url); resp.StatusCode != 200 || len(body) != 0 {
			t.Fatalf("HEAD %s: %d", url, resp.StatusCode)
		}
	}
	if total != 7000 {
		t.Fatalf("the strip's slices add up to %d px", total)
	}
	h.expect(h.do("GET", "/api/v1/chapters/99999/pages", key), 404, "not_found")
	h.expect(h.do("GET", "/api/v1/chapters/x/pages", key), 400, "validation_failed")
	for _, bad := range []string{"/pages/" + strings.Repeat("0", 64) + ".png", "/pages/..%2fabyssreader.db", "/pages/abyssreader.db", "/pages/"} {
		if resp, _ := h.image("GET", h.cfg.PublicURL+bad); resp.StatusCode != 404 {
			t.Errorf("%s: %d", bad, resp.StatusCode)
		}
	}
}

func TestHostedRoutesNeedTheAccessKeyAndAGoodToken(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { hostedOn(c); c.AccessKey = "lantern-access-key-1" })
	h.hostedCatalog()
	_, st := h.ferry(req{accessKey: "lantern-access-key-1"})
	for _, path := range []string{fmt.Sprintf("/api/v1/source-titles/%d/chapters", int64(st["id"].(float64))), "/api/v1/chapters/1/pages"} {
		h.expect(h.do("GET", path, req{}), 403, "access_key_required")
		h.expect(h.do("GET", path, req{accessKey: "wrong-access-key-12"}), 403, "access_key_invalid")
		h.expect(h.do("GET", path, req{accessKey: "lantern-access-key-1", token: "not-a-session"}), 401, "session_invalid")
		h.expect(h.do("GET", path, req{accessKey: "lantern-access-key-1"}), 200, "")
	}
}

// reopen serves the harness's data again with other settings: the same database and storage,
// as after an operator changes .env and restarts.
func (h *harness) reopen(change func(*config.Config)) *harness {
	h.t.Helper()
	cfg := h.cfg
	change(&cfg)
	srv := httptest.NewServer(New(Options{Config: cfg, DB: h.db, Accounts: h.accounts, Covers: h.covers, Pages: h.pages, Logger: h.logger}))
	h.t.Cleanup(srv.Close)
	again := *h
	again.srv, again.cfg = srv, cfg
	return &again
}

func TestSwitchedOffHostedChaptersAnswerFeatureDisabledAndKeepTheirData(t *testing.T) {
	on := newHarness(t, hostedOn)
	on.hostedCatalog()
	_, st := on.ferry(req{})
	chapters := fmt.Sprintf("/api/v1/source-titles/%d/chapters", int64(st["id"].(float64)))
	ch := on.do("GET", chapters, req{})
	pagesPath := fmt.Sprintf("/api/v1/chapters/%d/pages", int64(ch.body["items"].([]any)[0].(map[string]any)["id"].(float64)))
	pg := on.do("GET", pagesPath, req{})
	image := pg.body["items"].([]any)[0].(map[string]any)["imageUrl"].(string)
	status := on.do("GET", "/api/status", req{})
	if !slices.Equal(status.body["contracts"].([]any)[0].(map[string]any)["features"].([]any), []any{"hosted-chapters"}) {
		t.Fatalf("status with the feature on: %s", status.raw)
	}

	off := on.reopen(func(c *config.Config) { c.Features = nil })
	for _, path := range []string{chapters, pagesPath, "/api/v1/source-titles/99999/chapters", "/api/v1/chapters/abc/pages", chapters + "?size=1000"} {
		off.expect(off.do("GET", path, req{}), 404, "feature_disabled")
	}
	resp, body := off.image("GET", image)
	if resp.StatusCode != 404 || !strings.Contains(string(body), `"feature_disabled"`) {
		t.Fatalf("an image with the feature off: %d %s", resp.StatusCode, body)
	}
	status = off.do("GET", "/api/status", req{})
	if len(status.body["contracts"].([]any)[0].(map[string]any)["features"].([]any)) != 0 {
		t.Fatalf("status with the feature off: %s", status.raw)
	}
	if a := off.do("GET", "/api/v1/sources", req{}); strings.Contains(string(a.raw), `"hosted":true`) {
		t.Fatalf("sources with the feature off: %s", a.raw)
	}
	if _, st := off.ferry(req{}); st != nil {
		t.Fatal("the hosted source title is shown with the feature off")
	}
	off.expect(off.do("GET", "/api/v1/titles?source=hosted", req{}), 400, "validation_failed")

	again := on.reopen(func(c *config.Config) { hostedOn(c) })
	if a := again.do("GET", chapters, req{}); a.status != 200 || !bytes.Equal(a.raw, ch.raw) {
		t.Fatalf("chapters on again: %s", a.raw)
	}
	if a := again.do("GET", pagesPath, req{}); !bytes.Equal(a.raw, pg.raw) {
		t.Fatalf("pages on again: %s", a.raw)
	}
	if resp, _ := again.image("GET", image); resp.StatusCode != 200 {
		t.Fatalf("the image on again: %d", resp.StatusCode)
	}
}

func TestHostedRateLimitsAnswer429WithRetryAfter(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { hostedOn(c); c.HostedRequestsPerMinute = 2; c.PageImagesPerMinute = 1 })
	h.hostedCatalog()
	_, st := h.ferry(req{})
	chapters := fmt.Sprintf("/api/v1/source-titles/%d/chapters", int64(st["id"].(float64)))
	ch := h.do("GET", chapters, req{})
	pagesPath := fmt.Sprintf("/api/v1/chapters/%d/pages", int64(ch.body["items"].([]any)[0].(map[string]any)["id"].(float64)))
	a := h.do("GET", pagesPath, req{})
	h.expect(a, 200, "")
	image := a.body["items"].([]any)[0].(map[string]any)["imageUrl"].(string)

	for _, path := range []string{chapters, pagesPath} {
		a := h.do("GET", path, req{})
		h.expect(a, 429, "rate_limited")
		if s, err := strconv.Atoi(a.header.Get("Retry-After")); err != nil || s < 1 || s > 60 {
			t.Fatalf("%s: Retry-After %q", path, a.header.Get("Retry-After"))
		}
	}
	// Another client has its own budget.
	h.expect(h.do("GET", pagesPath, req{xff: "203.0.113.7"}), 200, "")

	if resp, _ := h.image("GET", image); resp.StatusCode != 200 {
		t.Fatalf("first image: %d", resp.StatusCode)
	}
	resp, body := h.image("GET", image)
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" || !strings.Contains(string(body), `"rate_limited"`) {
		t.Fatalf("second image: %d %v %s", resp.StatusCode, resp.Header, body)
	}
	// Catalog routes have no such limit.
	h.expect(h.do("GET", "/api/v1/sources", req{}), 200, "")
}

// Every hosted route answered every status this server gives, each answer held to the contract.
func TestEveryHostedRouteMatchesTheContract(t *testing.T) {
	c := contracttest.Load(t)
	for _, test := range []func(*testing.T){
		TestTheServerListsItselfAsTheHostedSource, TestChaptersPageSortAndOrder, TestChapterPagesAndTheirImages,
		TestHostedRoutesNeedTheAccessKeyAndAGoodToken, TestSwitchedOffHostedChaptersAnswerFeatureDisabledAndKeepTheirData,
		TestHostedRateLimitsAnswer429WithRetryAfter,
	} {
		test(t)
	}
	all := []string{"200", "400 validation_failed", "401 session_invalid", "403 access_key_required", "403 access_key_invalid",
		"404 not_found", "404 feature_disabled", "429 rate_limited"}
	for _, op := range []string{"listChapters", "listChapterPages"} {
		for _, cs := range all {
			status, code, _ := strings.Cut(cs, " ")
			n, _ := strconv.Atoi(status)
			if !c.Seen(op, n, code) {
				t.Errorf("%s never answered %s", op, cs)
			}
		}
	}
	if got := c.OperationIDs("/api/v1/source-titles"); !slices.Equal(got, []string{"listChapters"}) {
		t.Errorf("hosted operations under /source-titles: %v", got)
	}
	if t.Failed() {
		t.Logf("seen:\n%s", c.Summary())
	}
}
