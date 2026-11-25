// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/IGlace/abyssreader-server-kit/internal/accounts"
	"github.com/IGlace/abyssreader-server-kit/internal/community"
	"github.com/IGlace/abyssreader-server-kit/internal/definitions"
)

func (f featureRoutes) readCommunity(h http.HandlerFunc) http.HandlerFunc {
	return f.s.feature(f.name, f.s.limited(newLimiter(180, time.Minute), h))
}

func (f featureRoutes) writeCommunity(h func(http.ResponseWriter, *http.Request, caller)) http.HandlerFunc {
	return f.s.feature(f.name, f.s.signedIn(f.s.perAccount(newLimiter(60, time.Minute), h)))
}

func (f featureRoutes) CommentsRoutes() {
	f.titleActions["comments"] = f.commentListMethods(community.TitleComments, false)
	f.commentRoutes("/comments", community.TitleComments)
}

func (f featureRoutes) CollectionsRoutes() {
	s := f.s
	route(f.v1, BasePath+"/collections", map[string]http.HandlerFunc{
		"GET": f.readCommunity(s.listCollections(false)), "POST": f.writeCommunity(s.saveCollection(false)),
	})
	route(f.v1, BasePath+"/me/collections", map[string]http.HandlerFunc{
		"GET": f.readCommunity(s.signedIn(func(w http.ResponseWriter, r *http.Request, _ caller) { s.listCollections(true)(w, r) })),
	})
	route(f.v1, BasePath+"/collections/{id}", map[string]http.HandlerFunc{
		"GET": f.readCommunity(s.readCollection(false)), "PUT": f.writeCommunity(s.saveCollection(true)), "DELETE": f.writeCommunity(s.deleteCollection),
	})
	route(f.v1, BasePath+"/collections/by-slug/{slug}", map[string]http.HandlerFunc{"GET": f.readCommunity(s.readCollection(true))})
	routeActions(f.v1, BasePath+"/collections/{id}/{action}", map[string]http.HandlerFunc{
		"titles":   methods(map[string]http.HandlerFunc{"GET": f.readCommunity(s.listCollectionTitles)}),
		"vote":     methods(map[string]http.HandlerFunc{"PUT": f.writeCommunity(s.voteCollection)}),
		"reports":  methods(map[string]http.HandlerFunc{"POST": f.writeCommunity(s.reportCollection)}),
		"import":   methods(map[string]http.HandlerFunc{"POST": f.writeCommunity(s.importCollection)}),
		"comments": f.commentListMethods(community.CollectionComments, false),
	})
	f.commentRoutes("/collection-comments", community.CollectionComments)
}

func (f featureRoutes) StatsRoutes() {
	s := f.s
	route(f.v1, BasePath+"/me/stats", map[string]http.HandlerFunc{"GET": f.readCommunity(s.signedIn(s.getStats))})
	route(f.v1, BasePath+"/rankings", map[string]http.HandlerFunc{"GET": f.readCommunity(s.listRankings)})
}

func (f featureRoutes) commentListMethods(group community.CommentGroup, replies bool) http.HandlerFunc {
	return methods(map[string]http.HandlerFunc{
		"GET":  f.readCommunity(f.s.listComments(group, replies)),
		"POST": f.writeCommunity(f.s.createComment(group, replies)),
	})
}

func (f featureRoutes) commentRoutes(path string, group community.CommentGroup) {
	s := f.s
	base := BasePath + path + "/{id}"
	route(f.v1, base, map[string]http.HandlerFunc{
		"PUT":    f.writeCommunity(s.updateComment(group)),
		"DELETE": f.writeCommunity(s.deleteComment(group)),
	})
	f.v1.HandleFunc(base+"/replies", f.commentListMethods(group, true))
	route(f.v1, base+"/vote", map[string]http.HandlerFunc{"PUT": f.writeCommunity(s.voteComment(group))})
	route(f.v1, base+"/reports", map[string]http.HandlerFunc{"POST": f.writeCommunity(s.reportComment(group))})
}

func communityUser(r *http.Request) int64 {
	c, _ := callerOf(r)
	return c.account.ID
}

func communityPage(q *queryReader) community.PageQuery {
	return community.PageQuery{
		Page: q.integer("page", 1, 1, math.MaxInt32), Size: q.integer("size", defaultPageSize, 1, maxPageSize),
	}
}

func (q *queryReader) option(name, fallback string, known ...string) string {
	v, ok := q.single(name)
	if !ok {
		if fallback == "" {
			q.bad(name, "required")
		}
		return fallback
	}
	if !slices.Contains(known, v) {
		q.bad(name, "unknown_value")
	}
	return v
}

func (q *queryReader) searchText() string {
	v, ok := q.single("q")
	if ok {
		switch n := utf8.RuneCountInString(v); {
		case n < 1:
			q.bad("q", "too_short")
		case n > maxQ:
			q.bad("q", "too_long")
		}
	}
	return v
}

func (q *queryReader) valid(w http.ResponseWriter) bool {
	if len(q.fields) == 0 {
		return true
	}
	writeValidation(w, q.fields)
	return false
}

func (s *server) replyCommunity(w http.ResponseWriter, r *http.Request, status int, body any, err error) {
	switch {
	case err == nil && status == http.StatusNoContent:
		w.WriteHeader(status)
	case err == nil:
		writeJSON(w, status, body)
	case errors.Is(err, community.ErrNotFound):
		notFound(w, r)
	case errors.Is(err, community.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "Only the owner can change this item.")
	case errors.Is(err, community.ErrDuplicate):
		writeError(w, http.StatusConflict, "already_exists", "You already have an open report on this item.")
	case errors.Is(err, community.ErrNested):
		writeValidation(w, []fieldError{{Field: "id", Code: "invalid"}})
	case errors.Is(err, community.ErrLimit):
		writeValidation(w, []fieldError{{Field: "account", Code: "too_many"}})
	default:
		s.storeFailed(w, "community request", err)
	}
}

func optionalString(o *jsonObject, name string) *string {
	if _, ok := o.fields[name]; !ok {
		return nil
	}
	return o.nullableStr(name)
}

func contentText(o *jsonObject, name, text string, max int) {
	o.notEmpty(name, strings.TrimSpace(text))
	o.maxLen(name, text, max)
}

func commentInput(w http.ResponseWriter, r *http.Request) (community.CommentInput, bool) {
	o, ok := readObject(w, r)
	if !ok {
		return community.CommentInput{}, false
	}
	text := o.str("text")
	contentText(o, "text", text, 10000)
	image := optionalString(o, "imageUrl")
	if image != nil {
		code := accounts.CheckHTTPSURL(*image)
		if code == "" && !definitions.IsPublicHTTPSURL(*image) {
			code = "invalid"
		}
		o.check("imageUrl", code)
	}
	var spoiler bool
	if raw, ok := o.fields["spoiler"]; ok {
		o.order = append(o.order, "spoiler")
		if string(raw) == "null" || json.Unmarshal(raw, &spoiler) != nil {
			o.check("spoiler", "invalid")
		}
	}
	return community.CommentInput{Text: text, ImageURL: image, Spoiler: spoiler}, !o.failed(w)
}

func voteInput(w http.ResponseWriter, r *http.Request) (*string, bool) {
	o, ok := readObject(w, r)
	if !ok {
		return nil, false
	}
	choice := o.nullableStr("vote")
	if choice != nil && *choice != "up" && *choice != "down" {
		o.check("vote", "unknown_value")
	}
	return choice, !o.failed(w)
}

func reportInput(w http.ResponseWriter, r *http.Request, collection bool) (string, *string, bool) {
	o, ok := readObject(w, r)
	if !ok {
		return "", nil, false
	}
	reason := o.str("reason")
	valid := false
	switch reason {
	case "spam", "harassment_or_hate", "sexual_content", "illegal_content", "other":
		valid = true
	case "broken_titles", "duplicate_titles":
		valid = collection
	case "spoiler":
		valid = !collection
	}
	if !valid {
		o.check("reason", "unknown_value")
	}
	details := optionalString(o, "details")
	if details != nil {
		contentText(o, "details", *details, 500)
	}
	return reason, details, !o.failed(w)
}
