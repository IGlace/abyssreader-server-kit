// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package catalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/IGlace/abyssreader-server-kit/internal/covers"
	"github.com/IGlace/abyssreader-server-kit/internal/manifest"
	"github.com/IGlace/abyssreader-server-kit/internal/store/sqlcdb"
	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

// Counts says what an import did with the sources or titles of a manifest.
type Counts struct{ Added, Updated, Unchanged int }

// Failure is one title an import could not take, or one cover it could not store.
type Failure struct{ Ref, Reason string }

// CoverCounts says how many covers an import stored and how many it left as they were.
type CoverCounts struct{ Stored, Unchanged int }

// Report is what `admin import` prints.
type Report struct {
	Sources       Counts
	Titles        Counts
	Failed        []Failure // titles not imported; the others are
	Covers        CoverCounts
	CoverFailures []Failure // titles imported without a cover; `admin covers retry` retries URL covers
	// Hosted chapters.
	Chapters        ChapterCounts
	Pages           PageCounts
	ChapterFailures []Failure // chapters not imported or not updated ("ref/lang/chapter"); the others are
	RemovedChapters []string  // "ref/lang/chapter" of every hosted chapter removed
	KeptChapters    []string  // chapters the manifest no longer lists, kept: removing needs RemoveChapters
}

// CoverReport is what `admin covers retry` prints.
type CoverReport struct {
	Stored   int
	Failures []Failure
}

// Removed is the title `admin titles remove` removed.
type Removed struct {
	Reports int64
	ID      int64
	Name    string
}

// coverWorkers is how many covers download at once.
const coverWorkers = 4

// titleFailure is a reason one title is not imported; any other error stops the import.
type titleFailure struct{ reason string }

func (f titleFailure) Error() string { return f.reason }

func failf(format string, args ...any) error {
	return titleFailure{fmt.Sprintf(format, args...)}
}

type sourceInfo struct {
	id   int64
	lang string
}

// Import takes a manifest in: sources first, then every title in its own
// transaction, then the covers.
//   - A title is found by its ref: a known ref is updated in place (same id, same source title
//     ids for the same source + url), a new ref is added, and an entry that has not changed
//     since the last import is left alone. Nothing is ever deleted except what the title's own
//     entry no longer lists (its other names, genres, authors and source titles).
//   - A title that cannot be taken (an unknown source, a slug or source title that belongs to
//     another ref, ...) is listed in Failed; the others are imported.
//   - A cover that cannot be stored leaves its title without a cover, listed in CoverFailures.
func (c *Catalog) Import(ctx context.Context, m *manifest.Manifest) (Report, error) {
	var r Report
	if c.covers == nil || c.fetch == nil {
		return r, errors.New("catalog: Import needs a cover store and a fetcher")
	}
	if err := c.importSources(ctx, m.Sources, &r); err != nil {
		return r, err
	}
	if err := c.sweepCovers(ctx); err != nil {
		return r, err
	}
	all, err := c.q.ListAllSources(ctx)
	if err != nil {
		return r, err
	}
	srcs := make(map[string]sourceInfo, len(all))
	for _, s := range all {
		if s.Hosted == 0 { // the hosted source is never a catalog source title's
			srcs[s.Slug] = sourceInfo{id: s.ID, lang: s.Lang}
		}
	}
	var hostedID int64
	var root *os.Root // the import folder, for page files
	if slices.ContainsFunc(m.Titles, func(t manifest.Title) bool { return len(t.Hosted) > 0 }) {
		if c.pages == nil {
			return r, errors.New("catalog: importing hosted titles needs a page store")
		}
		if hostedID, err = c.ensureHostedSource(ctx); err != nil {
			return r, err
		}
		if root, err = os.OpenRoot(m.Dir); err != nil {
			return r, fmt.Errorf("import folder: %w", err)
		}
		defer root.Close()
	}

	var oldImages []string // covers a title stopped using; deleted at the end when unused
	var imported []manifest.Title
	seen := map[string]bool{}
	for _, t := range m.Titles {
		if seen[t.Ref] {
			r.Failed = append(r.Failed, Failure{t.Ref, "the ref is listed twice in this manifest; only the first entry was imported"})
			continue
		}
		seen[t.Ref] = true
		var outcome string
		var rm chapterRemovals
		err := c.inTx(ctx, func(q *sqlcdb.Queries) error {
			var err error
			rm = chapterRemovals{}
			outcome, err = c.importTitle(ctx, q, t, srcs, hostedID, &rm, &oldImages)
			return err
		})
		var tf titleFailure
		switch {
		case errors.As(err, &tf):
			r.Failed = append(r.Failed, Failure{t.Ref, tf.reason})
			continue
		case err != nil:
			return r, fmt.Errorf("title %q: %w", t.Ref, err)
		}
		r.Chapters.Removed += len(rm.removed)
		r.RemovedChapters = append(r.RemovedChapters, rm.removed...)
		r.KeptChapters = append(r.KeptChapters, rm.kept...)
		switch outcome {
		case "added":
			r.Titles.Added++
		case "updated":
			r.Titles.Updated++
		default:
			r.Titles.Unchanged++
		}
		imported = append(imported, t)
		if len(t.Hosted) > 0 {
			if err := c.importChapters(ctx, root, t, &r); err != nil {
				return r, fmt.Errorf("title %q: %w", t.Ref, err)
			}
		}
	}

	if err := c.importCovers(ctx, m.Dir, imported, &r, &oldImages); err != nil {
		return r, err
	}
	if err := c.deleteUnused(ctx, oldImages); err != nil {
		return r, err
	}
	slices.Sort(r.RemovedChapters)
	slices.Sort(r.KeptChapters)
	return r, c.sweepPages(ctx)
}

func (c *Catalog) inTx(ctx context.Context, fn func(q *sqlcdb.Queries) error) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(c.q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *Catalog) importSources(ctx context.Context, sources []manifest.Source, r *Report) error {
	return c.inTx(ctx, func(q *sqlcdb.Queries) error {
		if err := checkStoredTachiyomiIDs(ctx, q, sources); err != nil {
			return err
		}
		now := c.now().UnixMilli()
		for _, s := range sources {
			icon := nullString(s.IconURL)
			old, err := q.GetSourceBySlug(ctx, s.Slug)
			var id int64
			switch {
			case errors.Is(err, sql.ErrNoRows):
				if id, err = q.InsertSource(ctx, sqlcdb.InsertSourceParams{Slug: s.Slug, Name: s.Name, Website: s.Website, Lang: s.Lang, IconUrl: icon, CreatedAt: now, UpdatedAt: now}); err != nil {
					return err
				}
				r.Sources.Added++
			case err != nil:
				return err
			default:
				id = old.ID
				ids, err := q.ListTachiyomiIDsOfSource(ctx, id)
				if err != nil {
					return err
				}
				want := slices.Sorted(slices.Values(s.TachiyomiIDs))
				if old.Name == s.Name && old.Website == s.Website && old.Lang == s.Lang && old.IconUrl == icon && slices.Equal(ids, want) {
					r.Sources.Unchanged++
					continue
				}
				if err := q.UpdateSource(ctx, sqlcdb.UpdateSourceParams{Name: s.Name, Website: s.Website, Lang: s.Lang, IconUrl: icon, UpdatedAt: now, ID: id}); err != nil {
					return err
				}
				r.Sources.Updated++
			}
			if err := q.DeleteTachiyomiIDsOfSource(ctx, id); err != nil {
				return err
			}
			for _, tid := range s.TachiyomiIDs {
				if err := q.InsertTachiyomiID(ctx, sqlcdb.InsertTachiyomiIDParams{TachiyomiID: tid, SourceID: id}); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// Validate against the claims present before any source is changed. Older imports
// could store zero-padded variants. Never let a new import pick one owner silently.
func checkStoredTachiyomiIDs(ctx context.Context, q *sqlcdb.Queries, sources []manifest.Source) error {
	requested := map[string]string{}
	for _, source := range sources {
		for _, id := range source.TachiyomiIDs {
			requested[id] = source.Slug
		}
	}
	if len(requested) == 0 {
		return nil
	}

	stored, err := q.ListTachiyomiIDClaims(ctx)
	if err != nil {
		return err
	}
	for _, claim := range stored {
		id := strings.TrimLeft(claim.TachiyomiID, "0")
		if slug, ok := requested[id]; ok && slug != claim.Slug {
			return fmt.Errorf("Tachiyomi id %s for source %q is already claimed by source %q (stored as %q)", id, slug, claim.Slug, claim.TachiyomiID)
		}
	}
	return nil
}

type resolvedSourceTitle struct {
	sourceID   int64
	url, lang  string
	existingID int64 // 0 when the title does not have it yet
}

func (c *Catalog) importTitle(ctx context.Context, q *sqlcdb.Queries, t manifest.Title, srcs map[string]sourceInfo, hostedID int64, rm *chapterRemovals, oldImages *[]string) (string, error) {
	// Everything that can fail the title is checked before anything is written.
	var sts []resolvedSourceTitle
	for _, st := range t.SourceTitles {
		info, ok := srcs[st.Source]
		if !ok {
			return "", failf("source %q is neither in this manifest's sources nor imported before", st.Source)
		}
		lang := st.Lang
		if lang == "" {
			lang = info.lang
		}
		if slices.ContainsFunc(sts, func(x resolvedSourceTitle) bool { return x.sourceID == info.id && x.url == st.URL }) {
			return "", failf("source title %s %s is listed twice", st.Source, st.URL)
		}
		owner, err := q.GetSourceTitleOwner(ctx, sqlcdb.GetSourceTitleOwnerParams{SourceID: info.id, Url: st.URL})
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return "", err
		case owner.Ref != t.Ref:
			return "", failf("source title %s %s belongs to the title %q; a source title belongs to one title", st.Source, st.URL, owner.Ref)
		}
		sts = append(sts, resolvedSourceTitle{sourceID: info.id, url: st.URL, lang: lang, existingID: owner.ID})
	}
	type genre struct{ slug, name string }
	var genres []genre
	for _, g := range t.Genres {
		slug := Slugify(g)
		if slug == "" {
			return "", failf("genre %q has no letter or digit to make a slug from", g)
		}
		if utf8.RuneCountInString(slug) > maxSlug {
			return "", failf("genre %q makes a slug longer than %d characters", shorten(g), maxSlug)
		}
		if !slices.ContainsFunc(genres, func(x genre) bool { return x.slug == slug }) {
			genres = append(genres, genre{slug, g})
		}
	}
	if ref, err := q.GetTitleRefBySlug(ctx, t.Slug); err == nil && ref != t.Ref {
		return "", failf("slug %q belongs to the title %q", t.Slug, ref)
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	// The entry decides "unchanged", with the languages its source titles take from their
	// sources: a source's new lang must reach the source titles that follow it.
	effective := t.Hash()
	for _, st := range sts {
		effective += " " + st.lang
	}
	sum := sha256.Sum256([]byte(effective))
	hash := hex.EncodeToString(sum[:])
	old, err := q.GetTitleByRef(ctx, t.Ref)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if exists && old.ManifestHash == hash {
		return "unchanged", nil
	}

	now := c.now().UnixMilli()
	var cover sql.NullString
	switch {
	case t.CoverURL != nil:
		cover = sql.NullString{String: *t.CoverURL, Valid: true}
	case t.CoverFile != "":
		cover = sql.NullString{String: "file:" + t.CoverFile, Valid: true}
	}
	var year sql.NullInt64
	if t.Year != nil {
		year = sql.NullInt64{Int64: int64(*t.Year), Valid: true}
	}
	var id int64
	if exists {
		id = old.ID
		err = q.UpdateTitle(ctx, sqlcdb.UpdateTitleParams{
			Slug: t.Slug, Name: t.Name, NameKey: strings.ToLower(t.Name), Description: nullString(t.Description),
			Status: t.Status, Rating: nullFloat(t.Rating), CoverSource: cover, ManifestHash: hash, UpdatedAt: now, PublicationYear: year, ID: id,
		})
		if err != nil {
			return "", err
		}
		// A different cover source makes the cover pending; the cover step stores the new one.
		if old.CoverSource != cover {
			if old.CoverImage.Valid {
				*oldImages = append(*oldImages, old.CoverImage.String)
			}
			if err := q.SetCover(ctx, sqlcdb.SetCoverParams{UpdatedAt: now, ID: id}); err != nil {
				return "", err
			}
		}
	} else {
		id, err = q.InsertTitle(ctx, sqlcdb.InsertTitleParams{
			Ref: t.Ref, Slug: t.Slug, Name: t.Name, NameKey: strings.ToLower(t.Name), Description: nullString(t.Description),
			Status: t.Status, Rating: nullFloat(t.Rating), CoverSource: cover, ManifestHash: hash, CreatedAt: now, UpdatedAt: now, PublicationYear: year,
		})
		if err != nil {
			return "", err
		}
	}

	if err := q.DeleteAltNames(ctx, id); err != nil {
		return "", err
	}
	for i, name := range t.AltNames {
		if err := q.InsertAltName(ctx, sqlcdb.InsertAltNameParams{TitleID: id, Position: int64(i), Name: name, NameKey: strings.ToLower(name)}); err != nil {
			return "", err
		}
	}
	if err := q.DeleteTitleGenres(ctx, id); err != nil {
		return "", err
	}
	for i, g := range genres {
		gid, err := q.PutGenre(ctx, sqlcdb.PutGenreParams{Slug: g.slug, Name: g.name})
		if err != nil {
			return "", err
		}
		if err := q.InsertTitleGenre(ctx, sqlcdb.InsertTitleGenreParams{TitleID: id, GenreID: gid, Position: int64(i)}); err != nil {
			return "", err
		}
	}
	if err := q.DeleteTitleAuthors(ctx, id); err != nil {
		return "", err
	}
	for i, a := range t.Authors {
		aid, err := q.PutAuthor(ctx, a.Name)
		if err != nil {
			return "", err
		}
		if err := q.InsertTitleAuthor(ctx, sqlcdb.InsertTitleAuthorParams{TitleID: id, Position: int64(i), AuthorID: aid, Role: a.Role}); err != nil {
			return "", err
		}
	}
	had, err := q.ListSourceTitleIDsOfTitle(ctx, id)
	if err != nil {
		return "", err
	}
	if err := importHostedSourceTitles(ctx, q, t.Ref, id, hostedID, t.Hosted, c.removeChapters, rm); err != nil {
		return "", err
	}
	if len(rm.kept) > 0 {
		// The entry is not fully applied: the next import looks at it again and lists what it
		// keeps until the removal is allowed.
		if err := q.SetManifestHash(ctx, sqlcdb.SetManifestHashParams{ManifestHash: "", ID: id}); err != nil {
			return "", err
		}
	}
	for i, st := range sts {
		pos := int64(i) // the hosted source titles come first whatever their positions
		if st.existingID != 0 {
			err = q.UpdateSourceTitle(ctx, sqlcdb.UpdateSourceTitleParams{Lang: st.lang, Position: pos, ID: st.existingID})
		} else {
			err = q.InsertSourceTitle(ctx, sqlcdb.InsertSourceTitleParams{TitleID: id, SourceID: st.sourceID, Url: st.url, Lang: st.lang, Position: pos})
		}
		if err != nil {
			return "", err
		}
	}
	for _, hid := range had {
		if !slices.ContainsFunc(sts, func(x resolvedSourceTitle) bool { return x.existingID == hid }) {
			if err := q.DeleteSourceTitle(ctx, hid); err != nil {
				return "", err
			}
		}
	}
	if exists {
		return "updated", nil
	}
	return "added", nil
}

// importCovers stores the covers of the imported titles: a file cover is read again on every
// import (it may have changed under the same name), an https cover is downloaded only while
// the title has none stored.
func (c *Catalog) importCovers(ctx context.Context, dir string, titles []manifest.Title, r *Report, oldImages *[]string) error {
	var root *os.Root
	var downloads []sqlcdb.Title
	for _, t := range titles {
		row, err := c.q.GetTitleByRef(ctx, t.Ref)
		if err != nil {
			return err
		}
		switch {
		case !row.CoverSource.Valid:
		case strings.HasPrefix(row.CoverSource.String, "file:"):
			if root == nil {
				if root, err = os.OpenRoot(dir); err != nil {
					return fmt.Errorf("import folder: %w", err)
				}
				defer root.Close()
			}
			img, err := readCoverFile(root, t.CoverFile)
			if err != nil {
				r.CoverFailures = append(r.CoverFailures, Failure{t.Ref, err.Error()})
				if err := c.setCover(ctx, row, "", err.Error(), oldImages); err != nil {
					return err
				}
				continue
			}
			stored, err := c.storeCover(ctx, row, img, oldImages)
			if err != nil {
				return err
			}
			if stored {
				r.Covers.Stored++
			} else {
				r.Covers.Unchanged++
			}
		case row.CoverImage.Valid:
			r.Covers.Unchanged++
		default:
			downloads = append(downloads, row)
		}
	}
	stored, failures, err := c.download(ctx, downloads, oldImages)
	r.Covers.Stored += stored
	r.CoverFailures = append(r.CoverFailures, failures...)
	return err
}

// RetryCovers downloads again every https cover that has no stored image (`admin covers
// retry`). A cover file of an import folder is read again by importing the folder again.
func (c *Catalog) RetryCovers(ctx context.Context) (CoverReport, error) {
	if c.covers == nil || c.fetch == nil {
		return CoverReport{}, errors.New("catalog: RetryCovers needs a cover store and a fetcher")
	}
	if err := c.sweepCovers(ctx); err != nil {
		return CoverReport{}, err
	}
	pending, err := c.q.ListPendingCovers(ctx)
	if err != nil {
		return CoverReport{}, err
	}
	rows := make([]sqlcdb.Title, len(pending))
	for i, p := range pending {
		rows[i] = sqlcdb.Title{ID: p.ID, Ref: p.Ref, CoverSource: p.CoverSource, CoverImage: p.CoverImage}
	}
	var unused []string
	stored, failures, err := c.download(ctx, rows, &unused)
	if err != nil {
		return CoverReport{}, err
	}
	return CoverReport{Stored: stored, Failures: failures}, c.deleteUnused(ctx, unused)
}

// download fetches the covers of rows, coverWorkers at a time. Each cover is stored and
// recorded as soon as it arrives, so memory stays near coverWorkers × covers.MaxBytes and an
// interrupted batch keeps what it stored: a re-run fetches only the covers still missing. A
// cancelled ctx stops new downloads; the error is then ctx's.
func (c *Catalog) download(ctx context.Context, rows []sqlcdb.Title, oldImages *[]string) (int, []Failure, error) {
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu       sync.Mutex // one writer at a time; the downloads themselves run in parallel
		stored   int
		failures []Failure
		firstErr error
	)
	next := make(chan sqlcdb.Title)
	var wg sync.WaitGroup
	for range min(coverWorkers, len(rows)) {
		wg.Go(func() {
			for row := range next {
				if ctx.Err() != nil {
					continue // cancelled: never start another download (drain the feed)
				}
				img, fetchErr := c.fetch(ctx, row.CoverSource.String)
				if fetchErr != nil && ctx.Err() != nil {
					continue // cut off: not a failure of the cover
				}
				mu.Lock()
				// A cover in hand is kept even when the batch was cancelled meanwhile.
				ok, failure, err := c.recordDownload(context.WithoutCancel(ctx), row, img, fetchErr, oldImages)
				switch {
				case err != nil:
					if firstErr == nil {
						firstErr = err
					}
					cancel()
				case ok:
					stored++
				case failure != nil:
					failures = append(failures, *failure)
				}
				mu.Unlock()
			}
		})
	}
feed:
	for _, row := range rows {
		// When a worker waits and ctx is done, both cases of the select are ready and Go picks
		// one at random: check first, so a cancelled import never feeds another row.
		if ctx.Err() != nil {
			break
		}
		select {
		case next <- row:
		case <-ctx.Done():
			break feed
		}
	}
	close(next)
	wg.Wait()
	if firstErr == nil {
		firstErr = parent.Err()
	}
	return stored, failures, firstErr
}

// recordDownload stores one downloaded cover, or records why it failed. It returns true when
// a cover was stored.
func (c *Catalog) recordDownload(ctx context.Context, row sqlcdb.Title, img covers.Image, fetchErr error, oldImages *[]string) (bool, *Failure, error) {
	// Re-read: the title may have changed while its cover downloaded.
	cur, err := c.q.GetTitle(ctx, row.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil
	} else if err != nil {
		return false, nil, err
	}
	if cur.CoverSource != row.CoverSource || cur.CoverImage.Valid {
		return false, nil, nil
	}
	if fetchErr != nil {
		reason := fmt.Sprintf("%s: %v", row.CoverSource.String, fetchErr)
		return false, &Failure{row.Ref, reason}, c.setCover(ctx, cur, "", reason, oldImages)
	}
	if _, err := c.storeCover(ctx, cur, img, oldImages); err != nil {
		return false, nil, err
	}
	return true, nil, nil
}

// storeCover stores img as row's cover; false when it already was.
func (c *Catalog) storeCover(ctx context.Context, row sqlcdb.Title, img covers.Image, oldImages *[]string) (bool, error) {
	name, err := c.covers.Put(img)
	if err != nil {
		return false, err
	}
	if row.CoverImage.Valid && row.CoverImage.String == name {
		return false, nil
	}
	return true, c.setCover(ctx, row, name, "", oldImages)
}

// setCover records a title's cover (name "" = none) and why it failed (reason "" = it did
// not). updatedAt moves only when the stored image changes.
func (c *Catalog) setCover(ctx context.Context, row sqlcdb.Title, name, reason string, oldImages *[]string) error {
	updated := row.UpdatedAt
	if row.CoverImage.String != name {
		updated = c.now().UnixMilli()
		if row.CoverImage.Valid {
			*oldImages = append(*oldImages, row.CoverImage.String)
		}
	}
	return c.q.SetCover(ctx, sqlcdb.SetCoverParams{
		CoverImage: sql.NullString{String: name, Valid: name != ""},
		CoverError: sql.NullString{String: reason, Valid: reason != ""},
		UpdatedAt:  updated,
		ID:         row.ID,
	})
}

// sweepCovers reclaims files left by a stopped command. As for other catalog writes,
// the admin command holds the exclusive lock across the database check and file removal.
func (c *Catalog) sweepCovers(ctx context.Context) error {
	names, err := c.q.ListCoverImageNames(ctx)
	if err != nil {
		return err
	}
	return c.covers.Prune(names)
}

// deleteUnused deletes stored covers no title uses any more.
func (c *Catalog) deleteUnused(ctx context.Context, names []string) error {
	for _, name := range names {
		n, err := c.q.CountCoverUse(ctx, sql.NullString{String: name, Valid: true})
		if err != nil {
			return err
		}
		if n == 0 {
			if err := c.covers.Remove(name); err != nil {
				return err
			}
		}
	}
	return nil
}

// readCoverFile reads a plain cover file inside the import folder.
func readCoverFile(root *os.Root, path string) (covers.Image, error) {
	fileError := func(err error) error {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			err = pe.Err // the reason without the folder's own path
		}
		return fmt.Errorf("cover file %s: %v", path, err)
	}
	rel := filepath.FromSlash(path)
	st, err := root.Lstat(rel)
	if err != nil {
		return covers.Image{}, fileError(err)
	}
	if !st.Mode().IsRegular() {
		return covers.Image{}, fileError(errors.New("not a plain file"))
	}
	f, err := root.Open(rel)
	if err != nil {
		return covers.Image{}, fileError(err)
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		return covers.Image{}, fileError(errors.New("not a plain file"))
	}
	b, err := io.ReadAll(io.LimitReader(f, covers.MaxBytes+1))
	if err != nil {
		return covers.Image{}, fileError(err)
	}
	img, err := covers.Check(b)
	if err != nil {
		return covers.Image{}, fileError(err)
	}
	return img, nil
}

// Remove removes one title, its source titles (hosted ones with their chapters) and, when no
// other title uses them, its cover and its page files (`admin titles remove <ref>`).
func (c *Catalog) Remove(ctx context.Context, ref string) (Removed, error) {
	row, err := c.q.GetTitleByRef(ctx, ref)
	if errors.Is(err, sql.ErrNoRows) {
		return Removed{}, ErrNotFound
	}
	if err != nil {
		return Removed{}, err
	}
	// One write transaction: every account's records on the title become tombstones (each
	// device drops them on its next pull), then the title goes.
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return Removed{}, err
	}
	defer tx.Rollback()
	var reports int64
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM title_reports WHERE title_id=?", row.ID).Scan(&reports); err != nil {
		return Removed{}, err
	}
	if _, err := usersync.RemoveTitle(ctx, tx, row.ID, c.now()); err != nil {
		return Removed{}, err
	}
	if _, err := c.q.WithTx(tx).DeleteTitle(ctx, row.ID); err != nil {
		return Removed{}, err
	}
	if err := tx.Commit(); err != nil {
		return Removed{}, err
	}
	if row.CoverImage.Valid && c.covers != nil {
		if err := c.deleteUnused(ctx, []string{row.CoverImage.String}); err != nil {
			return Removed{}, err
		}
	}
	// Its hosted chapters went with it; their page files no other chapter uses go now.
	if err := c.sweepPages(ctx); err != nil {
		return Removed{}, err
	}
	return Removed{ID: row.ID, Name: row.Name, Reports: reports}, nil
}

// maxSlug is the contract's longest genre slug, in characters. A schema-valid name (at most
// 255 characters) always gives a slug within it, since lower-casing maps one character to one;
// the check in importTitle is a guard should that ever change.
const maxSlug = 255

// shorten cuts a long name for a report line.
func shorten(s string) string {
	if r := []rune(s); len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}

// Slugify makes a genre's slug from its name: lower-cased letters and digits, every other run
// of characters one dash. "Slice of Life" and "slice-of-life" give the same slug.
func Slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		} else {
			dash = true
		}
	}
	return b.String()
}

func nullString(s *string) sql.NullString {
	if s == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *s, Valid: true}
}

func nullFloat(f *float64) sql.NullFloat64 {
	if f == nil {
		return sql.NullFloat64{}
	}
	return sql.NullFloat64{Float64: *f, Valid: true}
}
