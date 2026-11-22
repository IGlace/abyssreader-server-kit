// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package catalog is the server's catalog: titles, their source titles, genres and sources.
// The operator fills it with `admin import` from an import manifest (import.go); the
// kit never crawls. The read side (this file and search.go) serves the core catalog routes.
package catalog

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/covers"
	"github.com/IGlace/abyssreader-server-kit/internal/pages"
	"github.com/IGlace/abyssreader-server-kit/internal/store/sqlcdb"
)

// ErrNotFound means there is no such title.
var ErrNotFound = errors.New("not found")

// Fetch downloads and checks one cover URL; covers.Fetcher.Fetch in production.
type Fetch func(ctx context.Context, url string) (covers.Image, error)

// Catalog reads and writes the catalog.
type Catalog struct {
	db             *sql.DB
	q              *sqlcdb.Queries
	covers         *covers.Store
	fetch          Fetch
	now            func() time.Time
	pages          *covers.Store // the page images of hosted chapters (UsePages)
	showHosted     bool          // the read side shows the hosted source (ShowHosted)
	removeChapters bool          // an import may remove hosted chapters (RemoveChapters)
	// prepare checks, slices and stores one page file; pages.Prepare (a test seam).
	prepare func(root *os.Root, path string, w, h int, store *covers.Store) (pages.Source, error)
}

// New returns the catalog on db. store and fetch are needed only to import; the server's read
// side passes nil for both.
func New(db *sql.DB, store *covers.Store, fetch Fetch) *Catalog {
	return &Catalog{db: db, q: sqlcdb.New(db), covers: store, fetch: fetch, now: time.Now, prepare: pages.Prepare}
}

// Title is one title as the read routes show it.
type Title struct {
	ID          int64
	Slug        string
	Name        string
	AltNames    []string
	Description *string
	Status      string
	Genres      []GenreRef
	Authors     []Author
	CoverImage  string // the stored cover's file name; "" when the title has no cover
	Rating      *float64
	Views       int64
	Langs       []string // the languages of its source titles, in their order
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Sources     []SourceTitle
}

// GenreRef is a genre on a title.
type GenreRef struct {
	ID         int64
	Name, Slug string
}

// Author is a person credited on a title.
type Author struct {
	ID         int64
	Name, Role string
}

// SourceTitle is a title on one source.
type SourceTitle struct {
	ID         int64
	SourceSlug string
	URL        string
	Lang       string
	Hosted     bool // on the server's own hosted source
	// Hosted source titles only; nil elsewhere.
	ReadingMode *string
	Rights      *Rights
}

// Genre is a genre with how many titles have it.
type Genre struct {
	ID         int64
	Name, Slug string
	TitleCount int64
}

// Source is a source of the catalog.
type Source struct {
	Slug, Name, Website, Lang string
	IconURL                   *string
	Hosted                    bool
}

func millis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// Title returns one title by id.
func (c *Catalog) Title(ctx context.Context, id int64) (Title, error) {
	return c.one(ctx, "t.id = ?", id)
}

// TitleBySlug returns one title by slug.
func (c *Catalog) TitleBySlug(ctx context.Context, slug string) (Title, error) {
	return c.one(ctx, "t.slug = ?", slug)
}

func (c *Catalog) one(ctx context.Context, condition string, key any) (Title, error) {
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Title{}, err
	}
	defer tx.Rollback()
	row, err := scanTitle(tx.QueryRowContext(ctx, "SELECT "+titleColumns+" FROM titles t WHERE "+condition+" AND "+VisibleTitle, key, c.showHosted))
	if errors.Is(err, sql.ErrNoRows) {
		return Title{}, ErrNotFound
	}
	if err != nil {
		return Title{}, err
	}
	ts, err := c.load(ctx, c.q.WithTx(tx), []sqlcdb.Title{row})
	if err != nil {
		return Title{}, err
	}
	return ts[0], nil
}

// CountView counts one read of a title (GET /titles/{id} with countView=true).
func (c *Catalog) CountView(ctx context.Context, id int64) error {
	return c.q.CountView(ctx, id)
}

// load fills in the lists of each title row, keeping the rows' order.
func (c *Catalog) load(ctx context.Context, q *sqlcdb.Queries, rows []sqlcdb.Title) ([]Title, error) {
	out := make([]Title, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]int64, len(rows))
	at := make(map[int64]*Title, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
		t := Title{
			ID: r.ID, Slug: r.Slug, Name: r.Name, Status: r.Status, Views: r.Views,
			CreatedAt: millis(r.CreatedAt), UpdatedAt: millis(r.UpdatedAt),
			AltNames: []string{}, Genres: []GenreRef{}, Authors: []Author{}, Langs: []string{}, Sources: []SourceTitle{},
		}
		if r.Description.Valid {
			t.Description = &r.Description.String
		}
		if r.Rating.Valid {
			t.Rating = &r.Rating.Float64
		}
		if r.CoverImage.Valid {
			t.CoverImage = r.CoverImage.String
		}
		out[i] = t
		at[r.ID] = &out[i]
	}
	alt, err := q.AltNamesOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, a := range alt {
		at[a.TitleID].AltNames = append(at[a.TitleID].AltNames, a.Name)
	}
	genres, err := q.GenresOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, g := range genres {
		at[g.TitleID].Genres = append(at[g.TitleID].Genres, GenreRef{ID: g.ID, Name: g.Name, Slug: g.Slug})
	}
	authors, err := q.AuthorsOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, a := range authors {
		at[a.TitleID].Authors = append(at[a.TitleID].Authors, Author{ID: a.ID, Name: a.Name, Role: a.Role})
	}
	sts, err := q.SourceTitlesOf(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, s := range sts {
		t := at[s.TitleID]
		if s.Hosted == 1 && !c.showHosted {
			continue // hosted-chapters is off: hidden, kept
		}
		st := SourceTitle{ID: s.ID, SourceSlug: s.Slug, URL: s.Url, Lang: s.Lang, Hosted: s.Hosted == 1, Rights: rightsOf(s.Rights)}
		if s.ReadingMode.Valid {
			st.ReadingMode = &s.ReadingMode.String
		}
		t.Sources = append(t.Sources, st)
		seen := false
		for _, l := range t.Langs {
			seen = seen || l == s.Lang
		}
		if !seen {
			t.Langs = append(t.Langs, s.Lang)
		}
	}
	return out, nil
}

// Genres lists every genre at least one title has, by name. While the hosted source is
// hidden, titles with nothing else to read count for nothing.
func (c *Catalog) Genres(ctx context.Context) ([]Genre, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT g.id,g.name,g.slug,count(*) FROM genres g
 JOIN title_genres tg ON tg.genre_id=g.id JOIN titles t ON t.id=tg.title_id
 WHERE `+VisibleTitle+` GROUP BY g.id ORDER BY g.name,g.id`, c.showHosted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Genre{}
	for rows.Next() {
		var g Genre
		if err := rows.Scan(&g.ID, &g.Name, &g.Slug, &g.TitleCount); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Sources lists every source the catalog's source titles point to, by name. The hosted
// source is listed while ShowHosted is on, with the lang of its source titles; its name and
// website are the server's own, which the HTTP layer fills in.
func (c *Catalog) Sources(ctx context.Context) ([]Source, error) {
	rows, err := c.q.ListUsedSources(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Source, 0, len(rows))
	for _, r := range rows {
		s := Source{Slug: r.Slug, Name: r.Name, Website: r.Website, Lang: r.Lang, Hosted: r.Hosted == 1}
		if r.IconUrl.Valid {
			s.IconURL = &r.IconUrl.String
		}
		if s.Hosted {
			if !c.showHosted {
				continue
			}
			if s.Lang, err = c.hostedLang(ctx); err != nil {
				return nil, err
			}
		}
		out = append(out, s)
	}
	return out, nil
}

// TachiyomiIDs lists the Mihon/Tachiyomi numeric ids of a source, for the backup restore.
func (c *Catalog) TachiyomiIDs(ctx context.Context, sourceSlug string) ([]string, error) {
	s, err := c.q.GetSourceBySlug(ctx, sourceSlug)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return c.q.ListTachiyomiIDsOfSource(ctx, s.ID)
}
