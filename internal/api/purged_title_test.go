// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"context"
	"fmt"
	"io"
	"slices"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/config"
	"github.com/IGlace/abyssreader-server-kit/internal/features"
)

func TestPurgedTitleIsHiddenUntilReimport(t *testing.T) {
	both := func(c *config.Config) { c.Features = []string{"reports", "hosted-chapters"} }
	on := newHarness(t, both)
	on.smallCatalog()
	m := obj{"manifestVersion": 1, "titles": []obj{{"ref": "lamp-post", "name": "Lamp Post", "genres": []string{"Fantasy", "Lamps"}, "hosted": []obj{{"lang": "en", "chapters": []obj{{"ref": "1", "pages": []any{"p.png"}}}}}}}}
	files := map[string][]byte{"p.png": pagePNG(t, 80, 100, 7)}
	on.importHosted(m, files)
	id := on.titleIDs()["Lamp Post"]
	token, _ := on.signUp("Reader", "reader@example.org", "correct-horse-7")
	on.push(token, item("favorite", titleKey(id), nil, "2026-10-10T10:00:00.000Z", false))
	for _, f := range features.Built {
		if f.Name == features.HostedChapters {
			if _, err := f.Purge(context.Background(), on.db, on.cfg.DataDir, io.Discard); err != nil {
				t.Fatal(err)
			}
		}
	}
	path := fmt.Sprintf("/api/v1/titles/%d", id)
	for _, settings := range []func(*config.Config){reportsOn, both} {
		h := on.reopen(settings)
		reader := req{token: token}
		for _, query := range []string{"/api/v1/titles", "/api/v1/titles?q=lamp", "/api/v1/me/library?list=favorite"} {
			a := h.do("GET", query, reader)
			h.expect(a, 200, "")
			want := 0
			if query == "/api/v1/titles" {
				want = 3
			}
			if len(names(a)) != want || a.body["totalItems"] != float64(want) {
				t.Errorf("%s lists purged title: %s", query, a.raw)
			}
		}
		genres := h.do("GET", "/api/v1/genres", reader)
		h.expect(genres, 200, "")
		counts := map[string]float64{}
		for _, it := range genres.body["items"].([]any) {
			g := it.(map[string]any)
			counts[g["name"].(string)] = g["titleCount"].(float64)
		}
		if _, hasLamps := counts["Lamps"]; counts["Fantasy"] != 2 || hasLamps {
			t.Errorf("genres after purge: %v", counts)
		}
		h.expect(h.do("GET", path, reader), 404, "not_found")
		h.expect(h.do("GET", "/api/v1/titles/by-slug/lamp-post", reader), 404, "not_found")
		h.expect(h.do("POST", path+"/reports", req{token: token, body: obj{"reason": "other"}}), 404, "not_found")
	}
	on.importHosted(m, files)
	title := on.do("GET", path+"?countView=false", req{token: token})
	on.expect(title, 200, "")
	if title.body["me"].(map[string]any)["favorite"] != true || !slices.Equal(names(on.library(token, "favorite", "")), []string{"Lamp Post"}) {
		t.Fatalf("reimport lost library state: %s", title.raw)
	}
}
