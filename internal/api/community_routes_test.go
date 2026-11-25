// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"fmt"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/config"
)

func TestCommunityRoutesMatchContractAndCascade(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Features = []string{"comments", "collections", "stats"}
	})
	h.importCatalog(obj{
		"manifestVersion": 1,
		"sources":         []obj{{"slug": "sample", "name": "Sample", "website": "https://sample.example", "lang": "en"}},
		"titles": []obj{{
			"ref": "paper", "name": "Paper", "sourceTitles": []obj{{"source": "sample", "url": "/paper", "lang": "en"}},
		}},
	}, nil)
	token, _ := h.signUp("Example", "community@example.com", "correct-horse-7")
	call := func(method, path string, body any, status int, code string) answer {
		t.Helper()
		a := h.do(method, BasePath+path, req{token: token, body: body})
		h.expect(a, status, code)
		return a
	}
	idPath := func(prefix string, a answer) string {
		return fmt.Sprintf("%s/%.0f", prefix, a.body["id"])
	}
	comment := call("POST", "/titles/1/comments", obj{"text": "A note"}, 201, "")
	commentPath := idPath("/comments", comment)
	reply := call("POST", commentPath+"/replies", obj{"text": "A reply", "spoiler": true}, 201, "")
	replyPath := idPath("/comments", reply)
	call("GET", "/titles/1/comments?sort=popular", nil, 200, "")
	call("GET", commentPath+"/replies?order=desc", nil, 200, "")
	call("POST", replyPath+"/replies", obj{"text": "Nested"}, 400, "validation_failed")
	call("PUT", commentPath, obj{"text": "An edit"}, 200, "")
	vote := call("PUT", commentPath+"/vote", obj{"vote": "up"}, 200, "")
	if vote.body["upvotes"] != float64(1) {
		t.Fatalf("vote did not count: %s", vote.raw)
	}

	collection := call("POST", "/collections", obj{"name": "An entry", "titleIds": []int{1}}, 201, "")
	collectionPath := idPath("/collections", collection)
	call("GET", "/collections?q=entry&sort=alphabetical", nil, 200, "")
	call("GET", "/me/collections", nil, 200, "")
	call("GET", collectionPath, nil, 200, "")
	call("GET", "/collections/by-slug/"+collection.body["slug"].(string), nil, 200, "")
	call("PUT", collectionPath, obj{"name": "New name", "titleIds": []int{1}}, 200, "")
	call("GET", collectionPath+"/titles?sort=added", nil, 200, "")
	call("PUT", collectionPath+"/vote", obj{"vote": "down"}, 200, "")
	collectionComment := call("POST", collectionPath+"/comments", obj{"text": "A note"}, 201, "")
	collectionCommentPath := idPath("/collection-comments", collectionComment)
	collectionReply := call("POST", collectionCommentPath+"/replies", obj{"text": "A reply"}, 201, "")
	call("GET", collectionCommentPath+"/replies", nil, 200, "")
	call("GET", collectionPath+"/comments", nil, 200, "")
	call("PUT", collectionCommentPath, obj{"text": "A new note"}, 200, "")
	call("PUT", collectionCommentPath+"/vote", obj{"vote": "up"}, 200, "")
	call("DELETE", idPath("/collection-comments", collectionReply), nil, 204, "")
	call("POST", collectionPath+"/import", nil, 204, "")
	call("POST", collectionPath+"/import", nil, 204, "")
	favorites := call("GET", "/me/library?list=favorite", nil, 200, "")
	if favorites.body["totalItems"] != float64(1) {
		t.Fatalf("collection wasn't imported into favorites: %s", favorites.raw)
	}
	for _, path := range []string{commentPath, replyPath, collectionPath, collectionCommentPath} {
		call("POST", path+"/reports", obj{"reason": "unknown"}, 400, "validation_failed")
		call("POST", path+"/reports", obj{"reason": "spam"}, 204, "")
		call("POST", path+"/reports", obj{"reason": "spam"}, 409, "already_exists")
	}
	other := call("POST", "/collections", obj{"name": "Delete me", "titleIds": []int{}}, 201, "")
	call("DELETE", idPath("/collections", other), nil, 204, "")
	other = call("POST", "/titles/1/comments", obj{"text": "Delete me"}, 201, "")
	call("DELETE", idPath("/comments", other), nil, 204, "")

	stats := call("GET", "/me/stats", nil, 200, "")
	for field, want := range map[string]float64{
		"commentsPosted": 2, "collectionsCreated": 1, "collectionCommentsPosted": 1, "reportsSubmitted": 4, "favoriteCount": 1,
	} {
		if stats.body[field] != want {
			t.Errorf("%s = %v, want %v", field, stats.body[field], want)
		}
	}
	secondToken, _ := h.signUp("Second", "another@example.com", "correct-horse-7")
	h.push(secondToken, item("rating", titleKey(1), obj{"opinion": "up"}, timestamp(time.Now().Add(-time.Second)), false))
	first := call("GET", "/rankings?period=overall&size=1", nil, 200, "")
	second := call("GET", "/rankings?period=overall&page=2&size=1", nil, 200, "")
	if first.body["hasMore"] != true || second.body["hasMore"] != false {
		t.Fatalf("wrong pagination: %s / %s", first.raw, second.raw)
	}
	call("POST", "/me/deletion", obj{"password": "correct-horse-7"}, 204, "")
	token = ""
	call("GET", commentPath+"/replies", nil, 404, "not_found")
	call("GET", collectionPath, nil, 404, "not_found")
	for _, table := range []string{"comments", "comment_votes", "collections", "collection_titles", "collection_votes", "content_reports"} {
		var count int
		if err := h.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Errorf("account deletion left %d rows in %s: %v", count, table, err)
		}
	}
}
