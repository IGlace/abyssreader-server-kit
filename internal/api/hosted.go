// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/IGlace/abyssreader-server-kit/internal/catalog"
)

// pagesPath is where page images are served, outside the base path like the covers: images
// load with no headers. The URL is ABYSS_PUBLIC_URL + pagesPath + the
// file's name, the SHA-256 of its bytes, so it never changes and may be cached forever.
const pagesPath = "/pages/"

// maxChaptersPage is the contract's size limit (and default) of GET .../chapters.
const maxChaptersPage = 100

// feature wraps the handler of a route of the optional feature name: while the operator has it
// off, the route answers 404 feature_disabled before anything else, and its data stays.
// Every optional feature's routes go through it.
func (s *server) feature(name string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.Config.FeatureOn(name) {
			featureDisabled(w, r)
			return
		}
		h(w, r)
	}
}

// limited wraps a handler with a per-client request budget: 429 rate_limited with
// Retry-After once it is spent.
func (s *server) limited(l *limiter, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if wait, ok := l.allow(s.clientKey(r)); !ok {
			writeRateLimited(w, wait)
			return
		}
		h(w, r)
	}
}

type chapterBody struct {
	ID          int64    `json:"id"`
	URL         string   `json:"url"`
	Number      *float64 `json:"number"`
	Name        *string  `json:"name"`
	Volume      *float64 `json:"volume"`
	PublishedAt string   `json:"publishedAt"`
	PageCount   int64    `json:"pageCount"`
	Credits     *string  `json:"credits"`
}

type chaptersPage struct {
	Items      []chapterBody `json:"items"`
	Page       int           `json:"page"`
	Size       int           `json:"size"`
	TotalItems int64         `json:"totalItems"`
	HasMore    bool          `json:"hasMore"`
}

type pageImageBody struct {
	Index    int    `json:"index"`
	ImageURL string `json:"imageUrl"`
	Width    int64  `json:"width"`
	Height   int64  `json:"height"`
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 || id > maxID {
		writeValidation(w, []fieldError{{Field: "id", Code: "invalid"}})
		return 0, false
	}
	return id, true
}

// listChapters answers GET /api/v1/source-titles/{id}/chapters.
func (s *server) listChapters(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	q := &queryReader{v: r.URL.Query()}
	page := q.integer("page", 1, 1, math.MaxInt32)
	size := q.integer("size", maxChaptersPage, 1, maxChaptersPage)
	if sort, ok := q.single("sort"); ok && sort != "number" {
		q.bad("sort", "unknown_value")
	}
	asc := false // the contract's default order for sort=number
	if order, ok := q.single("order"); ok {
		switch order {
		case "asc":
			asc = true
		case "desc":
		default:
			q.bad("order", "unknown_value")
		}
	}
	if len(q.fields) > 0 {
		writeValidation(w, q.fields)
		return
	}
	chs, total, err := s.catalog.Chapters(r.Context(), id, page, size, asc)
	if errors.Is(err, catalog.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "There is no such hosted source title on this server.")
		return
	}
	if err != nil {
		s.storeFailed(w, "chapters", err)
		return
	}
	out := chaptersPage{Items: make([]chapterBody, len(chs)), Page: page, Size: size, TotalItems: total,
		HasMore: int64(page)*int64(size) < total}
	for i, c := range chs {
		out.Items[i] = chapterBody{
			ID: c.ID, URL: "/chapters/" + strconv.FormatInt(c.ID, 10), Number: c.Number, Name: c.Name, Volume: c.Volume,
			PublishedAt: timestamp(c.PublishedAt), PageCount: c.PageCount, Credits: c.Credits,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// listChapterPages answers GET /api/v1/chapters/{id}/pages: the whole list, in reading order.
func (s *server) listChapterPages(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ps, err := s.catalog.ChapterPages(r.Context(), id)
	if errors.Is(err, catalog.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "There is no such chapter on this server.")
		return
	}
	if err != nil {
		s.storeFailed(w, "chapter pages", err)
		return
	}
	out := listBody[pageImageBody]{Items: make([]pageImageBody, len(ps))}
	for i, p := range ps {
		out.Items[i] = pageImageBody{Index: i, ImageURL: s.Config.PublicURL + pagesPath + p.Image, Width: p.Width, Height: p.Height}
	}
	writeJSON(w, http.StatusOK, out)
}

// servePage answers GET and HEAD /pages/{name}, like the covers: no access key, no session,
// cacheable forever.
func (s *server) servePage(w http.ResponseWriter, r *http.Request) {
	if s.Pages == nil {
		notFound(w, r)
		return
	}
	serveImage(w, r, s.Pages, r.PathValue("name"))
}
