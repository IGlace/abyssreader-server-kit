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
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/catalog"
	"github.com/IGlace/abyssreader-server-kit/internal/config"
	"github.com/IGlace/abyssreader-server-kit/internal/contracttest"
	"github.com/IGlace/abyssreader-server-kit/internal/covers"
	"github.com/IGlace/abyssreader-server-kit/internal/manifest"
)

type obj = map[string]any

func coverPNG(t testing.TB, w int) []byte {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, w, w*3/2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// importCatalog imports a manifest (and its cover files) into the harness's server, as
// `admin import` does.
func (h *harness) importCatalog(m obj, files map[string][]byte) catalog.Report {
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
	r, err := catalog.New(h.db, h.covers, noDownloads).Import(context.Background(), mf)
	if err != nil {
		h.t.Fatal(err)
	}
	if len(r.Failed) > 0 {
		h.t.Fatalf("import failures: %+v", r.Failed)
	}
	return r
}

var catalogSources = []obj{
	{"slug": "paper-moon", "name": "Paper Moon Comics", "website": "https://papermoon.example.com", "lang": "en", "iconUrl": "https://cdn.example.net/icons/paper-moon.png"},
	{"slug": "luna-es", "name": "Luna Cómics", "website": "https://luna.example.net", "lang": "es"},
	{"slug": "unused", "name": "Nobody's source", "website": "https://unused.example.net", "lang": "en"},
}

// smallCatalog has three titles: two with covers, one unrated.
func (h *harness) smallCatalog() {
	h.t.Helper()
	h.importCatalog(obj{
		"manifestVersion": 1,
		"sources":         catalogSources,
		"titles": []obj{
			{
				"ref": "lantern-keeper", "slug": "the-lantern-keeper", "name": "The Lantern Keeper",
				"altNames": []string{"El guardián del farol"}, "description": "A keeper's apprentice finds the lamp answers back.",
				"status": "ongoing", "genres": []string{"Fantasy", "Adventure"},
				"authors": []obj{{"name": "Mira Okafor"}, {"name": "Jun Sato", "role": "artist"}},
				"rating":  8.4, "coverFile": "covers/lantern.png",
				"sourceTitles": []obj{
					{"source": "paper-moon", "url": "/series/the-lantern-keeper/"},
					{"source": "luna-es", "url": "/manga/el-guardian-del-farol"},
				},
			},
			{
				"ref": "tidewater", "name": "Tidewater", "genres": []string{"Fantasy"}, "rating": 6, "coverFile": "covers/tide.png",
				"sourceTitles": []obj{{"source": "paper-moon", "url": "/series/tidewater/"}},
			},
			{
				"ref": "quiet-orbit", "name": "Quiet Orbit", "status": "completed",
				"sourceTitles": []obj{{"source": "luna-es", "url": "/manga/orbita"}},
			},
		},
	}, map[string][]byte{"covers/lantern.png": coverPNG(h.t, 40), "covers/tide.png": coverPNG(h.t, 41)})
}

func names(a answer) []string {
	var out []string
	for _, it := range a.body["items"].([]any) {
		out = append(out, it.(map[string]any)["name"].(string))
	}
	return out
}

func TestTitleDetails(t *testing.T) {
	h := newHarness(t, nil)
	h.smallCatalog()

	a := h.do("GET", "/api/v1/titles/by-slug/the-lantern-keeper", req{})
	h.expect(a, 200, "")
	if a.body["me"] != nil {
		t.Fatalf("anonymous me must be null: %s", a.raw)
	}
	id := int64(a.body["id"].(float64))
	cover, _ := a.body["coverUrl"].(string)
	if !strings.HasPrefix(cover, "https://lantern.example.org/covers/") || !strings.HasSuffix(cover, ".png") {
		t.Fatalf("coverUrl: %q", cover)
	}
	if a.body["rating"] != 8.4 || a.body["status"] != "ongoing" || fmt.Sprint(a.body["langs"]) != "[en es]" ||
		fmt.Sprint(a.body["altNames"]) != "[El guardián del farol]" {
		t.Fatalf("title: %s", a.raw)
	}
	src := a.body["sources"].([]any)
	first := src[0].(map[string]any)
	if len(src) != 2 || first["sourceSlug"] != "paper-moon" || first["url"] != "/series/the-lantern-keeper/" ||
		first["hasDefinition"] != false || first["readingMode"] != nil || first["rights"] != nil {
		t.Fatalf("sources: %s", a.raw)
	}
	authors := a.body["authors"].([]any)
	if authors[0].(map[string]any)["role"] != "author" || authors[1].(map[string]any)["role"] != "artist" {
		t.Fatalf("authors: %s", a.raw)
	}

	// By id: the same title. A read counts a view unless countView=false, once per client and
	// title in viewWindow (the read by slug above counted it).
	b := h.do("GET", fmt.Sprintf("/api/v1/titles/%d", id), req{})
	h.expect(b, 200, "")
	if b.body["slug"] != "the-lantern-keeper" || b.body["views"] != float64(1) {
		t.Fatalf("by id (views %v): %s", b.body["views"], b.raw)
	}
	c := h.do("GET", fmt.Sprintf("/api/v1/titles/%d?countView=false", id), req{xff: "198.51.100.7"})
	if c.body["views"] != float64(1) {
		t.Fatalf("countView=false counted a view: %v", c.body["views"])
	}

	// Signed in, me is the caller's own state (empty until the reader has library state).
	token, _ := h.signUp("quietreader", "reader@example.com", "correct-horse-7")
	d := h.do("GET", fmt.Sprintf("/api/v1/titles/%d", id), req{token: token})
	h.expect(d, 200, "")
	if fmt.Sprint(d.body["me"]) != "map[blacklist:false favorite:false planning:false vote:<nil>]" {
		t.Fatalf("me: %s", d.raw)
	}

	// The unrated title without a cover.
	q := h.do("GET", "/api/v1/titles/by-slug/quiet-orbit", req{})
	if q.body["rating"] != nil || q.body["coverUrl"] != nil || q.body["description"] != nil || fmt.Sprint(q.body["genres"]) != "[]" {
		t.Fatalf("quiet-orbit: %s", q.raw)
	}

	h.expect(h.do("GET", "/api/v1/titles/999", req{}), 404, "not_found")
	h.expect(h.do("GET", "/api/v1/titles/by-slug/nothing-here", req{}), 404, "not_found")
	for _, bad := range []string{"0", "-1", "abc", "2147483648", "1.5"} {
		h.expect(h.do("GET", "/api/v1/titles/"+bad, req{}), 400, "validation_failed")
	}
	h.expect(h.do("GET", fmt.Sprintf("/api/v1/titles/%d?countView=maybe", id), req{}), 400, "validation_failed")
	h.expect(h.do("GET", "/api/v1/titles/by-slug/"+strings.Repeat("x", 256), req{}), 400, "validation_failed")
}

func TestGenresAndSources(t *testing.T) {
	h := newHarness(t, nil)
	a := h.do("GET", "/api/v1/genres", req{})
	h.expect(a, 200, "")
	if string(bytes.TrimSpace(a.raw)) != `{"items":[]}` {
		t.Fatalf("an empty catalog has no genres: %s", a.raw)
	}
	h.expect(h.do("GET", "/api/v1/sources", req{}), 200, "")

	h.smallCatalog()
	a = h.do("GET", "/api/v1/genres", req{})
	h.expect(a, 200, "")
	var genres struct {
		Items []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Slug       string `json:"slug"`
			TitleCount int64  `json:"titleCount"`
		} `json:"items"`
	}
	json.Unmarshal(a.raw, &genres)
	if len(genres.Items) != 2 || genres.Items[0].Name != "Adventure" || genres.Items[0].TitleCount != 1 ||
		genres.Items[1].Slug != "fantasy" || genres.Items[1].TitleCount != 2 {
		t.Fatalf("genres: %s", a.raw)
	}

	a = h.do("GET", "/api/v1/sources", req{})
	h.expect(a, 200, "")
	want := `{"items":[` +
		`{"slug":"luna-es","name":"Luna Cómics","iconUrl":null,"lang":"es","website":"https://luna.example.net","hosted":false},` +
		`{"slug":"paper-moon","name":"Paper Moon Comics","iconUrl":"https://cdn.example.net/icons/paper-moon.png","lang":"en","website":"https://papermoon.example.com","hosted":false}]}`
	if string(bytes.TrimSpace(a.raw)) != want {
		t.Fatalf("sources (a source no title uses is left out):\n got %s\nwant %s", a.raw, want)
	}
}

// bigCatalog has 25 titles with every filterable field varied.
func (h *harness) bigCatalog() {
	h.t.Helper()
	var titles []obj
	statuses := []string{"ongoing", "completed", "on_hiatus"}
	for i := 1; i <= 25; i++ {
		t := obj{
			"ref":          fmt.Sprintf("title-%02d", i),
			"name":         fmt.Sprintf("Title %02d", 26-i), // names run against the import order
			"status":       statuses[i%3],
			"genres":       []string{[]string{"Fantasy", "Drama", "Comedy"}[i%3]},
			"sourceTitles": []obj{{"source": []string{"paper-moon", "luna-es"}[i%2], "url": fmt.Sprintf("/t/%d", i)}},
		}
		if i%2 == 0 {
			t["genres"] = append(t["genres"].([]string), "Romance")
		}
		if i%5 != 0 { // every fifth title is unrated
			t["rating"] = float64(i%10) + 0.5
		}
		if i == 7 {
			t["altNames"] = []string{"The Hidden NAME_7%"}
		}
		titles = append(titles, t)
	}
	h.importCatalog(obj{"manifestVersion": 1, "sources": catalogSources, "titles": titles}, nil)
}

func (h *harness) search(query string) answer {
	h.t.Helper()
	a := h.do("GET", "/api/v1/titles?"+query, req{})
	h.expect(a, 200, "")
	return a
}

func TestSearchPaging(t *testing.T) {
	h := newHarness(t, nil)
	h.bigCatalog()

	a := h.search("")
	if a.body["page"] != float64(1) || a.body["size"] != float64(20) || a.body["totalItems"] != float64(25) || a.body["hasMore"] != true || len(names(a)) != 20 {
		t.Fatalf("defaults: page %v size %v total %v more %v", a.body["page"], a.body["size"], a.body["totalItems"], a.body["hasMore"])
	}
	// Pages of 10 cover every title once, for every sort.
	for _, sort := range []string{"latest", "popular", "trending", "alphabetical"} {
		var all []string
		for page := 1; page <= 3; page++ {
			p := h.search(fmt.Sprintf("size=10&page=%d&sort=%s", page, sort))
			all = append(all, names(p)...)
			if more := p.body["hasMore"].(bool); more != (page < 3) {
				t.Errorf("%s page %d hasMore=%v", sort, page, more)
			}
		}
		if len(all) != 25 || len(slices.Compact(slices.Sorted(slices.Values(all)))) != 25 {
			t.Errorf("%s: %d titles over three pages, with duplicates: %v", sort, len(all), all)
		}
	}
	past := h.search("page=9&size=10")
	if len(names(past)) != 0 || past.body["hasMore"] != false || past.body["totalItems"] != float64(25) || past.body["page"] != float64(9) {
		t.Fatalf("a page past the end: %s", past.raw)
	}
	h.expect(h.do("GET", "/api/v1/titles?size=101", req{}), 400, "validation_failed")
}

func TestSearchSorts(t *testing.T) {
	h := newHarness(t, nil)
	h.bigCatalog()

	alpha := names(h.search("sort=alphabetical&size=3"))
	if fmt.Sprint(alpha) != "[Title 01 Title 02 Title 03]" {
		t.Errorf("alphabetical defaults to ascending: %v", alpha)
	}
	if desc := names(h.search("sort=alphabetical&order=desc&size=2")); fmt.Sprint(desc) != "[Title 25 Title 24]" {
		t.Errorf("alphabetical desc: %v", desc)
	}
	// Latest: by updatedAt, newest first by default; ties by id, ascending, in both directions.
	for _, q := range []string{"size=25", "size=25&order=asc"} {
		items := h.search(q).body["items"].([]any)
		for i := 1; i < len(items); i++ {
			prev, cur := items[i-1].(map[string]any), items[i].(map[string]any)
			pu, cu := prev["updatedAt"].(string), cur["updatedAt"].(string)
			inOrder := pu > cu
			if strings.Contains(q, "asc") {
				inOrder = pu < cu
			}
			if !inOrder && !(pu == cu && prev["id"].(float64) < cur["id"].(float64)) {
				t.Errorf("latest %s: %v (%s) before %v (%s)", q, prev["id"], pu, cur["id"], cu)
			}
		}
	}
	// Popular: by rating, unrated last in both directions.
	pop := h.search("sort=popular&size=25")
	var ratings []any
	for _, it := range pop.body["items"].([]any) {
		ratings = append(ratings, it.(map[string]any)["rating"])
	}
	if ratings[0] != 9.5 || ratings[19] == nil || ratings[20] != nil || ratings[24] != nil {
		t.Errorf("popular: %v", ratings)
	}
	asc := h.search("sort=popular&order=asc&size=25")
	if r := asc.body["items"].([]any); r[0].(map[string]any)["rating"] != 1.5 || r[24].(map[string]any)["rating"] != nil {
		t.Errorf("popular asc keeps unrated last")
	}
	// Trending: by views.
	id := int64(h.search("sort=alphabetical&size=1").body["items"].([]any)[0].(map[string]any)["id"].(float64))
	for range 3 {
		h.do("GET", fmt.Sprintf("/api/v1/titles/%d", id), req{})
	}
	if top := names(h.search("sort=trending&size=1")); fmt.Sprint(top) != "[Title 01]" {
		t.Errorf("trending: %v", top)
	}
}

func TestSearchFilters(t *testing.T) {
	h := newHarness(t, nil)
	h.bigCatalog()
	genreID := map[string]int64{}
	for _, g := range h.do("GET", "/api/v1/genres", req{}).body["items"].([]any) {
		m := g.(map[string]any)
		genreID[m["slug"].(string)] = int64(m["id"].(float64))
	}
	total := func(q string) int { return int(h.search(q).body["totalItems"].(float64)) }

	// genre: every one listed (AND); excludeGenre: none of them.
	if n := total(fmt.Sprintf("genre=%d", genreID["romance"])); n != 12 {
		t.Errorf("romance: %d", n)
	}
	if n := total(fmt.Sprintf("genre=%d&genre=%d", genreID["romance"], genreID["fantasy"])); n != 4 {
		t.Errorf("romance AND fantasy: %d", n) // i%3==0 and even: 6, 12, 18, 24
	}
	if n := total(fmt.Sprintf("excludeGenre=%d&excludeGenre=%d", genreID["romance"], genreID["drama"])); n != 8 {
		t.Errorf("neither romance nor drama: %d", n)
	}
	// status, lang, source: any of them (OR).
	if n := total("status=ongoing&status=completed"); n != 17 {
		t.Errorf("ongoing or completed: %d", n)
	}
	if n := total("lang=es"); n != 13 {
		t.Errorf("lang es: %d", n)
	}
	if n := total("source=paper-moon&source=luna-es"); n != 25 {
		t.Errorf("both sources: %d", n)
	}
	if n := total("source=unused"); n != 0 {
		t.Errorf("a known source no title uses is an empty page, not an error: %d", n)
	}
	if n := total("lang=fr"); n != 0 {
		t.Errorf("lang fr: %d", n)
	}
	// q: name and other names, case-insensitive, anywhere; % and _ are plain characters.
	if got := names(h.search("q=" + url.QueryEscape("title 1"))); len(got) != 10 {
		t.Errorf("q=title 1: %v", got)
	}
	if got := names(h.search("q=" + url.QueryEscape("name_7%"))); fmt.Sprint(got) != "[Title 19]" {
		t.Errorf("q matches other names: %v", got)
	}
	if n := total("q=" + url.QueryEscape("%")); n != 1 {
		t.Errorf("q=%% is a plain character: %d", n)
	}
	// Ratings: an unrated title matches only the whole 0–10 range.
	if n := total("minRating=0&maxRating=10"); n != 25 {
		t.Errorf("whole range: %d", n)
	}
	if n := total("minRating=0&maxRating=9.9"); n != 20 {
		t.Errorf("not the whole range leaves the unrated out: %d", n)
	}
	if n := total("minRating=8"); n != 4 {
		t.Errorf("minRating=8: %d", n) // 8.5 ×2, 9.5 ×2
	}

	for _, bad := range []string{
		"genre=9999", "excludeGenre=9999", "genre=abc", "source=nowhere", "source=Bad_Slug", "status=finished", "lang=english",
		"sort=newest", "order=up", "q=", "q=" + strings.Repeat("x", 201), "minRating=-1", "maxRating=11", "minRating=7&maxRating=3",
		"page=0", "page=x", "size=0", "page=1&page=2",
	} {
		a := h.do("GET", "/api/v1/titles?"+bad, req{})
		h.expect(a, 400, "validation_failed")
		if len(a.body["fields"].([]any)) == 0 {
			t.Errorf("%s: no fields", bad)
		}
	}
}

func TestDefinitionRoutesOfAServerWithoutDefinitions(t *testing.T) {
	h := newHarness(t, nil)
	a := h.do("GET", "/api/v1/definitions/keys", req{})
	h.expect(a, 200, "")
	if string(bytes.TrimSpace(a.raw)) != `{"items":[]}` {
		t.Fatalf("keys: %s", a.raw)
	}

	m := h.do("GET", "/api/v1/definitions/manifest", req{})
	h.expect(m, 200, "")
	etag := m.header.Get("ETag")
	if string(bytes.TrimSpace(m.raw)) != `{"items":[]}` || !strings.HasPrefix(etag, `"`) {
		t.Fatalf("manifest: %s, ETag %q", m.raw, etag)
	}
	for _, inm := range []string{etag, "W/" + etag, `"other", ` + etag, "*"} {
		nm := h.do("GET", "/api/v1/definitions/manifest", req{header: map[string]string{"If-None-Match": inm}})
		if nm.status != 304 || nm.header.Get("ETag") != etag || len(nm.raw) != 0 {
			t.Errorf("If-None-Match %s: %d %q %s", inm, nm.status, nm.header.Get("ETag"), nm.raw)
		}
	}
	h.expect(h.do("GET", "/api/v1/definitions/manifest", req{header: map[string]string{"If-None-Match": `"stale"`}}), 200, "")

	h.expect(h.do("GET", "/api/v1/definitions/paper-moon", req{}), 404, "not_found")
	h.expect(h.do("GET", "/api/v1/definitions/Not_A_Slug", req{}), 400, "validation_failed")
	report := obj{"appVersion": "1.0.0", "items": []obj{{"definitionVersion": 1, "errorKind": "parse_failed", "count": 2}}}
	h.expect(h.do("POST", "/api/v1/definitions/paper-moon/health", req{body: report}), 404, "not_found")
	h.expect(h.do("POST", "/api/v1/definitions/Not_A_Slug/health", req{body: report}), 400, "validation_failed")
}

func TestCoversLoadWithNoHeaders(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.AccessKey = "lantern-access-key-1" })
	h.smallCatalog()
	h.expect(h.do("GET", "/api/v1/titles", req{}), 403, "access_key_required")

	a := h.do("GET", "/api/v1/titles/by-slug/the-lantern-keeper", req{accessKey: "lantern-access-key-1"})
	cover := a.body["coverUrl"].(string)
	path := strings.TrimPrefix(cover, "https://lantern.example.org")
	for _, method := range []string{"GET", "HEAD"} {
		hr, _ := http.NewRequest(method, h.srv.URL+path, nil) // no access key, no token
		resp, err := http.DefaultClient.Do(hr)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" ||
			resp.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s %s: %d %v", method, path, resp.StatusCode, resp.Header)
		}
		if method == "GET" {
			if _, err := covers.Check(body); err != nil {
				t.Fatalf("the served cover: %v", err)
			}
		}
	}
	// Even a bad token does not matter on an image.
	hr, _ := http.NewRequest("GET", h.srv.URL+path, nil)
	hr.Header.Set("Authorization", "Bearer not-a-session")
	if resp, _ := http.DefaultClient.Do(hr); resp.StatusCode != 200 {
		t.Fatalf("an image with a bad token: %d", resp.StatusCode)
	}
	for _, bad := range []string{"/covers/" + strings.Repeat("0", 64) + ".png", "/covers/..%2fabyssreader.db", "/covers/abyssreader.db", "/covers/"} {
		resp, err := http.Get(h.srv.URL + bad)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 {
			t.Errorf("%s: %d", bad, resp.StatusCode)
		}
	}
}

// Every catalog and "sources and definitions" route answered every status this server gives,
// each answer held to the contract.
func TestEveryCatalogRouteMatchesTheContract(t *testing.T) {
	c := contracttest.Load(t)
	for _, test := range []func(*testing.T){
		TestTitleDetails, TestGenresAndSources, TestSearchPaging, TestSearchSorts, TestSearchFilters,
		TestDefinitionRoutesOfAServerWithoutDefinitions, TestCoversLoadWithNoHeaders, TestCatalogRoutesNeedTheAccessKeyAndAGoodToken,
	} {
		test(t)
	}
	want := map[string][]string{
		"searchTitles":           {"200", "400 validation_failed", "401 session_invalid", "403 access_key_required", "403 access_key_invalid"},
		"getTitle":               {"200", "400 validation_failed", "401 session_invalid", "403 access_key_invalid", "404 not_found"},
		"getTitleBySlug":         {"200", "400 validation_failed", "404 not_found"},
		"listGenres":             {"200", "401 session_invalid", "403 access_key_required"},
		"listSources":            {"200", "401 session_invalid", "403 access_key_required"},
		"listDefinitionKeys":     {"200", "401 session_invalid", "403 access_key_required"},
		"getDefinitionManifest":  {"200", "304", "401 session_invalid", "403 access_key_required"},
		"getDefinition":          {"400 validation_failed", "404 not_found"},
		"reportDefinitionHealth": {"400 validation_failed", "404 not_found"},
	}
	for _, op := range c.OperationIDs("/api/v1/titles") {
		if _, ok := want[op]; !ok && !strings.Contains(op, "omment") && op != "reportTitle" {
			t.Errorf("catalog operation %s is not in the coverage list", op)
		}
	}
	for op, cases := range want {
		for _, cs := range cases {
			status, code, _ := strings.Cut(cs, " ")
			n := 0
			for _, ch := range status {
				n = n*10 + int(ch-'0')
			}
			if !c.Seen(op, n, code) {
				t.Errorf("%s never answered %s", op, cs)
			}
		}
	}
	if t.Failed() {
		t.Logf("seen:\n%s", c.Summary())
	}
}

func TestCatalogRoutesNeedTheAccessKeyAndAGoodToken(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.AccessKey = "lantern-access-key-1" })
	h.smallCatalog()
	for _, path := range []string{"/api/v1/titles", "/api/v1/titles/1", "/api/v1/genres", "/api/v1/sources", "/api/v1/definitions/keys", "/api/v1/definitions/manifest"} {
		h.expect(h.do("GET", path, req{}), 403, "access_key_required")
		h.expect(h.do("GET", path, req{accessKey: "wrong-access-key-12"}), 403, "access_key_invalid")
		h.expect(h.do("GET", path, req{accessKey: "lantern-access-key-1", token: "not-a-session"}), 401, "session_invalid")
		h.expect(h.do("GET", path, req{accessKey: "lantern-access-key-1"}), 200, "")
	}
}
