// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"fmt"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/config"
	"github.com/IGlace/abyssreader-server-kit/internal/pages"
)

// The page list never grows past the import's cap, whatever the database holds.
func TestThePagesRouteNeverBuildsMoreThanTheCap(t *testing.T) {
	h := newHarness(t, hostedOn)
	h.hostedCatalog()
	var chapter int64
	var sha string
	h.db.QueryRow(`SELECT chapter_id, file_sha256 FROM chapter_pages LIMIT 1`).Scan(&chapter, &sha)
	for i := 100; i < 100+pages.MaxChapterImages+100; i++ {
		if _, err := h.db.Exec(`INSERT INTO chapter_pages (chapter_id, position, file_sha256) VALUES (?, ?, ?)`, chapter, i, sha); err != nil {
			t.Fatal(err)
		}
	}
	a := h.do("GET", fmt.Sprintf("/api/v1/chapters/%d/pages", chapter), req{})
	h.expect(a, 200, "")
	if n := len(a.body["items"].([]any)); n != pages.MaxChapterImages {
		t.Fatalf("%d pages served", n)
	}
}

// With hosted-chapters off, a title with only hosted source titles is gone from every route.
func TestHostedOnlyTitlesAreHiddenOverHTTPWhileOff(t *testing.T) {
	on := newHarness(t, hostedOn)
	on.hostedCatalog()
	on.importHosted(obj{"manifestVersion": 1, "titles": []obj{{
		"ref": "lamp-post", "name": "Lamp Post", "genres": []string{"Lamps"},
		"hosted": []obj{{"lang": "en", "chapters": []obj{{"ref": "1", "pages": []any{"p.png"}}}}},
	}}}, map[string][]byte{"p.png": pagePNG(t, 400, 600, 7)})
	lamp := on.do("GET", "/api/v1/titles/by-slug/lamp-post?countView=false", req{})
	on.expect(lamp, 200, "")
	id := int64(lamp.body["id"].(float64))
	total := func(h *harness, query string) float64 {
		a := h.do("GET", "/api/v1/titles"+query, req{})
		h.expect(a, 200, "")
		return a.body["totalItems"].(float64)
	}
	if total(on, "") != 3 || !strings.Contains(string(on.do("GET", "/api/v1/genres", req{}).raw), `"Lamps"`) {
		t.Fatal("on: the hosted-only title is missing")
	}

	off := on.reopen(func(c *config.Config) { c.Features = nil })
	off.expect(off.do("GET", "/api/v1/titles/by-slug/lamp-post", req{}), 404, "not_found")
	off.expect(off.do("GET", fmt.Sprintf("/api/v1/titles/%d", id), req{}), 404, "not_found")
	if n := total(off, ""); n != 2 {
		t.Fatalf("off: %v titles", n)
	}
	if n := total(off, "?q=lamp"); n != 0 {
		t.Fatalf("off: a search finds %v", n)
	}
	if g := off.do("GET", "/api/v1/genres", req{}); strings.Contains(string(g.raw), `"Lamps"`) {
		t.Fatalf("off: genres %s", g.raw)
	}

	again := on.reopen(hostedOn)
	again.expect(again.do("GET", "/api/v1/titles/by-slug/lamp-post?countView=false", req{}), 200, "")
	if total(again, "") != 3 {
		t.Fatal("on again: the title is not back")
	}
}
