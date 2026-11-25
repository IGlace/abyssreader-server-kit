// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"fmt"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/config"
)

func TestCommunityWritesRequireOwnership(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Features = []string{"comments", "collections"}
	})
	h.smallCatalog()
	author, _ := h.signUp("Author", "owner@example.org", "correct-horse-7")
	other, _ := h.signUp("Reader", "reader@example.org", "correct-horse-7")
	post := func(path string, data obj) int64 {
		t.Helper()
		a := h.do("POST", BasePath+path, req{token: author, body: data})
		h.expect(a, 201, "")
		return int64(a.body["id"].(float64))
	}
	comment := post("/titles/1/comments", obj{"text": "Original title comment"})
	collection := post("/collections", obj{"name": "Original collection", "titleIds": []int{1}})
	collectionComment := post(fmt.Sprintf("/collections/%d/comments", collection), obj{"text": "Original collection comment"})
	items := []struct {
		name, path, readPath, field, original string
		edit                                  obj
	}{
		{"title-comment", fmt.Sprintf("/comments/%d", comment), "/titles/1/comments", "text", "Original title comment", obj{"text": "Changed"}},
		{"collection", fmt.Sprintf("/collections/%d", collection), fmt.Sprintf("/collections/%d", collection), "name", "Original collection", obj{"name": "Changed", "titleIds": []int{1}}},
		{"collection-comment", fmt.Sprintf("/collection-comments/%d", collectionComment), fmt.Sprintf("/collections/%d/comments", collection), "text", "Original collection comment", obj{"text": "Changed"}},
	}
	for _, item := range items {
		for _, method := range []string{"PUT", "DELETE"} {
			t.Run(item.name+"/"+method, func(t *testing.T) {
				sub := *h
				sub.t = t
				var data any
				if method == "PUT" {
					data = item.edit
				}
				a := sub.do(method, BasePath+item.path, req{token: other, body: data})
				sub.expect(a, 403, "forbidden")
				read := sub.do("GET", BasePath+item.readPath, req{})
				sub.expect(read, 200, "")
				body := read.body
				if item.field == "text" {
					list := body["items"].([]any)
					if len(list) != 1 {
						t.Fatalf("wanted one unchanged comment, got %s", read.raw)
					}
					body = list[0].(map[string]any)
				}
				if body[item.field] != item.original {
					t.Fatalf("content changed: %s", read.raw)
				}
			})
		}
	}
}

func TestCommentImagesRequirePublicHTTPSHost(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Features = []string{"comments", "collections"}
	})
	h.smallCatalog()
	token, _ := h.signUp("Author", "owner@example.org", "correct-horse-7")
	for _, address := range []string{
		"http://images.example.org/panel.jpg",
		"https://127.0.0.1/panel.jpg",
		"https://8.8.8.8/panel.jpg",
		"https://[::1]/panel.jpg",
		"https://localhost/panel.jpg",
		"https://printer/panel.jpg",
		"https://reader.local/panel.jpg",
		"https://reader.internal/panel.jpg",
	} {
		t.Run(address, func(t *testing.T) {
			sub := *h
			sub.t = t
			a := sub.do("POST", BasePath+"/titles/1/comments", req{token: token, body: obj{"text": "A note", "imageUrl": address}})
			sub.expect(a, 400, "validation_failed")
			fields := a.body["fields"].([]any)
			if len(fields) != 1 || fields[0].(map[string]any)["field"] != "imageUrl" {
				t.Fatalf("expected imageUrl validation, got %s", a.raw)
			}
		})
	}
	const public = "https://Images.example.org/panel.jpg"
	a := h.do("POST", BasePath+"/titles/1/comments", req{token: token, body: obj{"text": "A note", "imageUrl": public}})
	h.expect(a, 201, "")
	if a.body["imageUrl"] != public {
		t.Fatalf("approved image URL changed: %s", a.raw)
	}
}
