// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"errors"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/IGlace/abyssreader-server-kit/internal/catalog"
)

// The contract's Title, sent whole: every field, null when it has no value.
type titleBody struct {
	ID          int64             `json:"id"`
	Slug        string            `json:"slug"`
	Name        string            `json:"name"`
	AltNames    []string          `json:"altNames"`
	Description *string           `json:"description"`
	Status      string            `json:"status"`
	Genres      []genreRefBody    `json:"genres"`
	Authors     []authorBody      `json:"authors"`
	CoverURL    *string           `json:"coverUrl"`
	Rating      *float64          `json:"rating"`
	Views       int64             `json:"views"`
	Langs       []string          `json:"langs"`
	CreatedAt   string            `json:"createdAt"`
	UpdatedAt   string            `json:"updatedAt"`
	Sources     []sourceTitleBody `json:"sources"`
	Me          *myTitleState     `json:"me"`
}

type genreRefBody struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type authorBody struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

type sourceTitleBody struct {
	ID            int64       `json:"id"`
	SourceSlug    string      `json:"sourceSlug"`
	URL           string      `json:"url"`
	Lang          string      `json:"lang"`
	HasDefinition bool        `json:"hasDefinition"`
	ReadingMode   *string     `json:"readingMode"` // hosted source titles only
	Rights        *rightsBody `json:"rights"`      // hosted source titles only
}

type rightsBody struct {
	Holder      *string `json:"holder"`
	Licence     *string `json:"licence"`
	LicenceURL  *string `json:"licenceUrl"`
	Attribution *string `json:"attribution"`
	Changes     *string `json:"changes"`
}

// myTitleState is the contract's MyTitleState: the caller's own lists and vote.
type myTitleState struct {
	Favorite  bool    `json:"favorite"`
	Planning  bool    `json:"planning"`
	Blacklist bool    `json:"blacklist"`
	Vote      *string `json:"vote"`
}

type titlesPage struct {
	Items      []titleBody `json:"items"`
	Page       int         `json:"page"`
	Size       int         `json:"size"`
	TotalItems int64       `json:"totalItems"`
	HasMore    bool        `json:"hasMore"`
}

type genreBody struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	TitleCount int64  `json:"titleCount"`
}

type sourceBody struct {
	Slug    string  `json:"slug"`
	Name    string  `json:"name"`
	IconURL *string `json:"iconUrl"`
	Lang    string  `json:"lang"`
	Website string  `json:"website"`
	Hosted  bool    `json:"hosted"`
}

type listBody[T any] struct {
	Items []T `json:"items"`
}

// timestamp is the contract's Timestamp: UTC with exactly three decimals.
func timestamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// coverURL is a stored cover's absolute, header-free URL.
func (s *server) coverURL(name string) *string {
	if name == "" {
		return nil
	}
	u := s.Config.PublicURL + coversPath + name
	return &u
}

// titleStates gives the caller's own state on each title (`me`): their lists and vote, from
// their synced records; nil for an anonymous caller (`me: null`).
func (s *server) titleStates(r *http.Request, ids []int64) (map[int64]*myTitleState, error) {
	c, ok := callerOf(r)
	if !ok {
		return nil, nil
	}
	states, err := s.sync.States(r.Context(), c.account.ID, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]*myTitleState, len(ids))
	for id, st := range states {
		out[id] = &myTitleState{Favorite: st.Favorite, Planning: st.Planning, Blacklist: st.Blacklist, Vote: st.Vote}
	}
	return out, nil
}

func (s *server) titleBodies(r *http.Request, ts []catalog.Title) ([]titleBody, error) {
	ids := make([]int64, len(ts))
	for i, t := range ts {
		ids[i] = t.ID
	}
	states, err := s.titleStates(r, ids)
	if err != nil {
		return nil, err
	}
	out := make([]titleBody, len(ts))
	for i, t := range ts {
		b := titleBody{
			ID: t.ID, Slug: t.Slug, Name: t.Name, AltNames: t.AltNames, Description: t.Description, Status: t.Status,
			Genres: make([]genreRefBody, len(t.Genres)), Authors: make([]authorBody, len(t.Authors)),
			CoverURL: s.coverURL(t.CoverImage), Rating: t.Rating, Views: t.Views, Langs: t.Langs,
			CreatedAt: timestamp(t.CreatedAt), UpdatedAt: timestamp(t.UpdatedAt),
			Sources: make([]sourceTitleBody, len(t.Sources)), Me: states[t.ID],
		}
		for j, g := range t.Genres {
			b.Genres[j] = genreRefBody{ID: g.ID, Name: g.Name, Slug: g.Slug}
		}
		for j, a := range t.Authors {
			b.Authors[j] = authorBody{ID: a.ID, Name: a.Name, Role: a.Role}
		}
		for j, st := range t.Sources {
			b.Sources[j] = sourceTitleBody{
				ID: st.ID, SourceSlug: st.SourceSlug, URL: st.URL, Lang: st.Lang,
				HasDefinition: !st.Hosted && s.hasDefinition(st.SourceSlug),
				ReadingMode:   st.ReadingMode,
			}
			if r := st.Rights; r != nil {
				b.Sources[j].Rights = &rightsBody{Holder: r.Holder, Licence: r.Licence, LicenceURL: r.LicenceURL, Attribution: r.Attribution, Changes: r.Changes}
			}
		}
		out[i] = b
	}
	return out, nil
}

// Query parameter rules of the contract.
var (
	sourceSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	langPattern       = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z]{2,4})?$`)
	titleStatuses     = []string{"unknown", "ongoing", "completed", "licensed", "publishing_finished", "cancelled", "on_hiatus"}
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
	maxID           = math.MaxInt32
	maxQ            = 200
	// maxFilterValues caps each repeated filter (genre, excludeGenre, status, lang, source), so
	// a request cannot carry thousands of values into one query.
	maxFilterValues = 50
)

// queryReader reads query parameters and collects one FieldError per bad parameter.
type queryReader struct {
	v      url.Values
	fields []fieldError
}

func (q *queryReader) bad(field, code string) {
	for _, f := range q.fields {
		if f.Field == field {
			return
		}
	}
	q.fields = append(q.fields, fieldError{Field: field, Code: code})
}

// single reads a parameter that may appear once.
func (q *queryReader) single(name string) (string, bool) {
	vs, ok := q.v[name]
	if !ok {
		return "", false
	}
	if len(vs) != 1 {
		q.bad(name, "invalid")
		return "", false
	}
	return vs[0], true
}

func (q *queryReader) integer(name string, def, min, max int) int {
	s, ok := q.single(name)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(s)
	switch {
	case err != nil:
		q.bad(name, "invalid")
	case n < min:
		q.bad(name, "too_small")
	case n > max:
		q.bad(name, "too_large")
	default:
		return n
	}
	return def
}

func (q *queryReader) boolean(name string, def bool) bool {
	s, ok := q.single(name)
	if !ok {
		return def
	}
	switch s {
	case "true":
		return true
	case "false":
		return false
	}
	q.bad(name, "invalid")
	return def
}

func (q *queryReader) rating(name string) *float64 {
	s, ok := q.single(name)
	if !ok {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	switch {
	case err != nil || math.IsNaN(f) || math.IsInf(f, 0):
		q.bad(name, "invalid")
	case f < 0:
		q.bad(name, "too_small")
	case f > 10:
		q.bad(name, "too_large")
	default:
		return &f
	}
	return nil
}

// list reads a repeated parameter, at most maxFilterValues values.
func (q *queryReader) list(name string) []string {
	vs := q.v[name]
	if len(vs) > maxFilterValues {
		q.bad(name, "too_many")
		return nil
	}
	return vs
}

func (q *queryReader) ids(name string) []int64 {
	var out []int64
	for _, s := range q.list(name) {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 1 || n > maxID {
			q.bad(name, "invalid")
			continue
		}
		out = append(out, n)
	}
	return out
}

func (q *queryReader) matching(name string, p *regexp.Regexp) []string {
	vs := q.list(name)
	for _, s := range vs {
		if !p.MatchString(s) {
			q.bad(name, "invalid")
		}
	}
	return vs
}

// searchTitles answers GET /api/v1/titles.
func (s *server) searchTitles(w http.ResponseWriter, r *http.Request) {
	q := &queryReader{v: r.URL.Query()}
	query := q.titleQuery()
	query.Genres = q.ids("genre")
	query.ExcludeGenres = q.ids("excludeGenre")
	query.Langs = q.matching("lang", langPattern)
	query.Sources = q.matching("source", sourceSlugPattern)
	query.MinRating = q.rating("minRating")
	query.MaxRating = q.rating("maxRating")
	query.Statuses = q.list("status")
	for _, st := range query.Statuses {
		if !slices.Contains(titleStatuses, st) {
			q.bad("status", "unknown_value")
		}
	}
	if query.MinRating != nil && query.MaxRating != nil && *query.MinRating > *query.MaxRating {
		q.bad("minRating", "invalid")
	}
	if len(q.fields) > 0 {
		writeValidation(w, q.fields)
		return
	}
	// A signed-in caller's blacklist is left out of the search (the contract's searchTitles).
	if c, ok := callerOf(r); ok {
		query.HideBlacklistOf = c.account.ID
	}
	s.answerTitles(w, r, query)
}

// titleQuery reads what the search and the library share: page, size, q, sort and order.
func (q *queryReader) titleQuery() catalog.Query {
	query := catalog.Query{
		Page: q.integer("page", 1, 1, math.MaxInt32),
		Size: q.integer("size", defaultPageSize, 1, maxPageSize),
		Sort: catalog.SortLatest,
	}
	if text, ok := q.single("q"); ok {
		switch n := utf8.RuneCountInString(text); {
		case n < 1:
			q.bad("q", "too_short")
		case n > maxQ:
			q.bad("q", "too_long")
		}
		query.Q = text
	}
	if sort, ok := q.single("sort"); ok {
		switch catalog.Sort(sort) {
		case catalog.SortLatest, catalog.SortPopular, catalog.SortTrending, catalog.SortAlphabetical:
			query.Sort = catalog.Sort(sort)
		default:
			q.bad("sort", "unknown_value")
		}
	}
	query.Asc = query.Sort == catalog.SortAlphabetical // the contract's default order per sort
	if order, ok := q.single("order"); ok {
		switch order {
		case "asc":
			query.Asc = true
		case "desc":
			query.Asc = false
		default:
			q.bad("order", "unknown_value")
		}
	}
	return query
}

// answerTitles runs a checked query and writes the TitlesPage.
func (s *server) answerTitles(w http.ResponseWriter, r *http.Request, query catalog.Query) {
	page, err := s.catalog.Search(r.Context(), query)
	var fe *catalog.FilterError
	if errors.As(err, &fe) {
		writeValidation(w, []fieldError{{Field: fe.Field, Code: "unknown_value"}})
		return
	}
	var items []titleBody
	if err == nil {
		items, err = s.titleBodies(r, page.Titles)
	}
	if err != nil {
		s.storeFailed(w, "search", err)
		return
	}
	writeJSON(w, http.StatusOK, titlesPage{
		Items: items, Page: query.Page, Size: query.Size, TotalItems: page.Total,
		HasMore: int64(query.Page)*int64(query.Size) < page.Total,
	})
}

// getTitle answers GET /api/v1/titles/{id}.
func (s *server) getTitle(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 || id > maxID {
		writeValidation(w, []fieldError{{Field: "id", Code: "invalid"}})
		return
	}
	s.answerTitle(w, r, func() (catalog.Title, error) { return s.catalog.Title(r.Context(), id) })
}

// getTitleBySlug answers GET /api/v1/titles/by-slug/{slug}.
func (s *server) getTitleBySlug(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if n := utf8.RuneCountInString(slug); n < 1 || n > 255 {
		writeValidation(w, []fieldError{{Field: "slug", Code: "invalid"}})
		return
	}
	s.answerTitle(w, r, func() (catalog.Title, error) { return s.catalog.TitleBySlug(r.Context(), slug) })
}

func (s *server) answerTitle(w http.ResponseWriter, r *http.Request, find func() (catalog.Title, error)) {
	q := &queryReader{v: r.URL.Query()}
	countView := q.boolean("countView", true)
	if len(q.fields) > 0 {
		writeValidation(w, q.fields)
		return
	}
	t, err := find()
	if errors.Is(err, catalog.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "There is no such title on this server.")
		return
	}
	// A view counts on GET only, once per client and title in viewWindow, so a loop of requests
	// cannot push a title up the trending sort.
	if err == nil && countView && r.Method == http.MethodGet {
		if _, fresh := s.views.allow(s.clientKey(r) + " " + strconv.FormatInt(t.ID, 10)); fresh {
			if err = s.catalog.CountView(r.Context(), t.ID); err == nil {
				t.Views++
			}
		}
	}
	var bodies []titleBody
	if err == nil {
		bodies, err = s.titleBodies(r, []catalog.Title{t})
	}
	if err != nil {
		s.storeFailed(w, "title", err)
		return
	}
	writeJSON(w, http.StatusOK, bodies[0])
}

// listGenres answers GET /api/v1/genres.
func (s *server) listGenres(w http.ResponseWriter, r *http.Request) {
	genres, err := s.catalog.Genres(r.Context())
	if err != nil {
		s.storeFailed(w, "genres", err)
		return
	}
	out := listBody[genreBody]{Items: make([]genreBody, len(genres))}
	for i, g := range genres {
		out.Items[i] = genreBody{ID: g.ID, Name: g.Name, Slug: g.Slug, TitleCount: g.TitleCount}
	}
	writeJSON(w, http.StatusOK, out)
}

// listSources answers GET /api/v1/sources.
func (s *server) listSources(w http.ResponseWriter, r *http.Request) {
	sources, err := s.catalog.Sources(r.Context())
	if err != nil {
		s.storeFailed(w, "sources", err)
		return
	}
	out := listBody[sourceBody]{Items: make([]sourceBody, len(sources))}
	for i, src := range sources {
		out.Items[i] = sourceBody{Slug: src.Slug, Name: src.Name, IconURL: src.IconURL, Lang: src.Lang, Website: src.Website, Hosted: src.Hosted}
		if src.Hosted {
			// The hosted source is the server itself: its name and its own origin.
			out.Items[i].Name, out.Items[i].Website = s.Config.ServerName, s.Config.PublicURL
		}
	}
	writeJSON(w, http.StatusOK, out)
}
