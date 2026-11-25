// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package catalog

import (
	"context"
	"database/sql"
	"slices"
	"strings"

	"github.com/IGlace/abyssreader-server-kit/internal/store/sqlcdb"
	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

// VisibleTitle requires a source title the reader can see. The containing query
// aliases the title as t and binds the hosted-chapters switch to the placeholder.
const VisibleTitle = `EXISTS (SELECT 1 FROM source_titles st JOIN sources s ON s.id = st.source_id
 WHERE st.title_id = t.id AND (? OR s.hosted = 0))`

// Sort is a GET /api/v1/titles sort value (the contract's TitleSort).
type Sort string

const (
	SortAdded        Sort = "added"        // by collection membership order
	SortLatest       Sort = "latest"       // by updatedAt (the default)
	SortPopular      Sort = "popular"      // by rating, unrated titles last
	SortTrending     Sort = "trending"     // by views
	SortAlphabetical Sort = "alphabetical" // by name
)

// Query is a catalog search. The zero values of the filters mean "any".
type Query struct {
	Page, Size    int // 1-based page; the HTTP layer checks the bounds
	Q             string
	Genres        []int64 // the title has every one of them
	ExcludeGenres []int64 // the title has none of them
	Statuses      []string
	Langs         []string // the title has a source title in one of them
	Sources       []string // the title has a source title on one of them (slugs)
	MinRating     *float64
	MaxRating     *float64
	Sort          Sort
	Asc           bool // ascending order; the HTTP layer applies the route's default
	// HideBlacklistOf, when not 0, is the signed-in caller: titles on their blacklist are left
	// out (the contract's searchTitles).
	HideBlacklistOf int64
	// Library, when set, keeps only the titles on one of an account's lists (GET /me/library).
	Library *Library
	// CollectionID limits the result to one collection. Zero means all titles.
	CollectionID int64
}

// Library names one of an account's library lists: favorite, planning or blacklist (the sync
// kinds, written by internal/usersync).
type Library struct {
	UserID int64
	Kind   string
}

// FilterError means a filter names a genre id or a source slug the server does not have
// (400 validation_failed: an unknown filter value is never an empty page).
type FilterError struct{ Field string }

func (e *FilterError) Error() string { return "unknown " + e.Field }

// Page is one page of a search.
type Page struct {
	Titles []Title
	Total  int64
}

// Search answers GET /api/v1/titles. q matches the name and the other names, case-insensitive,
// anywhere in them. Ties of the sort key are broken by id, ascending, so a catalog that does
// not change pages without duplicates or gaps. The search is built here rather than with sqlc
// because every filter is optional and repeated.
func (c *Catalog) Search(ctx context.Context, q Query) (Page, error) {
	if err := c.checkFilters(ctx, q); err != nil {
		return Page{}, err
	}
	countSQL, pageSQL, args := c.searchSQL(q)

	// Count, page and source details must see the same catalog during import or purge.
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Page{}, err
	}
	defer tx.Rollback()
	if q.CollectionID != 0 {
		var present bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM collections WHERE id=?)", q.CollectionID).Scan(&present); err != nil {
			return Page{}, err
		}
		if !present {
			return Page{}, ErrNotFound
		}
	}
	var total int64
	if err := tx.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return Page{}, err
	}
	offset := int64(q.Page-1) * int64(q.Size)
	if offset >= total {
		return Page{Titles: []Title{}, Total: total}, nil
	}
	rows, err := tx.QueryContext(ctx, pageSQL, append(args, q.Size, offset)...)
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	var found []sqlcdb.Title
	for rows.Next() {
		t, err := scanTitle(rows)
		if err != nil {
			return Page{}, err
		}
		found = append(found, t)
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	titles, err := c.load(ctx, c.q.WithTx(tx), found)
	if err != nil {
		return Page{}, err
	}
	return Page{Titles: titles, Total: total}, nil
}

// searchSQL builds the count and page queries with the same search filters.
func (c *Catalog) searchSQL(q Query) (countSQL, pageSQL string, args []any) {
	where := []string{VisibleTitle}
	args = []any{c.showHosted}
	in := func(n int) string { return "(" + strings.TrimSuffix(strings.Repeat("?,", n), ",") + ")" }

	if q.Q != "" {
		pattern := "%" + likeEscaper.Replace(strings.ToLower(q.Q)) + "%"
		where = append(where, `(t.name_key LIKE ? ESCAPE '\' OR EXISTS (SELECT 1 FROM title_alt_names a WHERE a.title_id = t.id AND a.name_key LIKE ? ESCAPE '\'))`)
		args = append(args, pattern, pattern)
	}
	for _, g := range dedupe(q.Genres) {
		where = append(where, `EXISTS (SELECT 1 FROM title_genres g WHERE g.title_id = t.id AND g.genre_id = ?)`)
		args = append(args, g)
	}
	if ex := dedupe(q.ExcludeGenres); len(ex) > 0 {
		where = append(where, `NOT EXISTS (SELECT 1 FROM title_genres g WHERE g.title_id = t.id AND g.genre_id IN `+in(len(ex))+`)`)
		for _, g := range ex {
			args = append(args, g)
		}
	}
	if st := dedupe(q.Statuses); len(st) > 0 {
		where = append(where, `t.status IN `+in(len(st)))
		for _, s := range st {
			args = append(args, s)
		}
	}
	if langs := dedupe(q.Langs); len(langs) > 0 {
		hidden := ""
		if !c.showHosted {
			hidden = ` AND st.source_id NOT IN (SELECT id FROM sources WHERE hosted = 1)`
		}
		where = append(where, `EXISTS (SELECT 1 FROM source_titles st WHERE st.title_id = t.id AND st.lang IN `+in(len(langs))+hidden+`)`)
		for _, l := range langs {
			args = append(args, l)
		}
	}
	if srcs := dedupe(q.Sources); len(srcs) > 0 {
		where = append(where, `EXISTS (SELECT 1 FROM source_titles st JOIN sources s ON s.id = st.source_id WHERE st.title_id = t.id AND s.slug IN `+in(len(srcs))+`)`)
		for _, s := range srcs {
			args = append(args, s)
		}
	}
	// An unrated title matches only when the range is the whole 0–10.
	min, max := 0.0, 10.0
	if q.MinRating != nil {
		min = *q.MinRating
	}
	if q.MaxRating != nil {
		max = *q.MaxRating
	}
	if min > 0 || max < 10 {
		where = append(where, `t.rating IS NOT NULL AND t.rating >= ? AND t.rating <= ?`)
		args = append(args, min, max)
	}
	// The account's records (internal/usersync): `t.id IN (...)` lets SQLite start from the
	// account's list (the unique index on user_id, kind, key_title), not from every title.
	if q.Library != nil {
		where = append(where, `t.id IN (`+usersync.LibraryTitles+`)`)
		args = append(args, q.Library.UserID, q.Library.Kind)
	}
	if q.HideBlacklistOf != 0 {
		where = append(where, `t.id NOT IN (`+usersync.BlacklistTitles+`)`)
		args = append(args, q.HideBlacklistOf)
	}
	cond := " WHERE " + strings.Join(where, " AND ")

	dir := "DESC"
	if q.Asc {
		dir = "ASC"
	}
	var order string
	switch q.Sort {
	case SortAdded:
		order = "ct.position " + dir
	case SortPopular:
		order = "t.rating " + dir + " NULLS LAST"
	case SortTrending:
		order = "t.views " + dir
	case SortAlphabetical:
		order = "t.name_key " + dir + ", t.name " + dir
	default:
		order = "t.updated_at " + dir
	}
	from := " FROM titles t"
	if q.CollectionID != 0 {
		from += " JOIN collection_titles ct ON ct.title_id=t.id AND ct.collection_id=?"
		args = append([]any{q.CollectionID}, args...)
	}
	countSQL = "SELECT count(*)" + from + cond
	pageSQL = "SELECT " + titleColumns + from + cond +
		` ORDER BY ` + order + `, t.id ASC LIMIT ? OFFSET ?`
	return countSQL, pageSQL, args
}

func scanTitle(row interface{ Scan(...any) error }) (sqlcdb.Title, error) {
	var t sqlcdb.Title
	err := row.Scan(&t.ID, &t.Ref, &t.Slug, &t.Name, &t.NameKey, &t.Description, &t.Status, &t.Rating, &t.Views,
		&t.CoverSource, &t.CoverImage, &t.CoverError, &t.ManifestHash, &t.CreatedAt, &t.UpdatedAt)
	return t, err
}

// titleColumns are the columns used by the public catalog's title reads.
const titleColumns = `t.id, t.ref, t.slug, t.name, t.name_key, t.description, t.status, t.rating, t.views,
	t.cover_source, t.cover_image, t.cover_error, t.manifest_hash, t.created_at, t.updated_at`

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// checkFilters refuses a genre id or a source slug the server does not have.
func (c *Catalog) checkFilters(ctx context.Context, q Query) error {
	if len(q.Genres)+len(q.ExcludeGenres) > 0 {
		ids, err := c.q.ListGenreIDs(ctx)
		if err != nil {
			return err
		}
		for _, g := range q.Genres {
			if !slices.Contains(ids, g) {
				return &FilterError{Field: "genre"}
			}
		}
		for _, g := range q.ExcludeGenres {
			if !slices.Contains(ids, g) {
				return &FilterError{Field: "excludeGenre"}
			}
		}
	}
	if len(q.Sources) > 0 {
		all, err := c.q.ListAllSources(ctx)
		if err != nil {
			return err
		}
		for _, s := range q.Sources {
			// A switched-off hosted source is unknown here.
			if !slices.ContainsFunc(all, func(r sqlcdb.ListAllSourcesRow) bool { return r.Slug == s && (r.Hosted == 0 || c.showHosted) }) {
				return &FilterError{Field: "source"}
			}
		}
	}
	return nil
}

func dedupe[T comparable](xs []T) []T {
	var out []T
	seen := make(map[T]bool, len(xs))
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
