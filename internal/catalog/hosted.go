// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package catalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/covers"
	"github.com/IGlace/abyssreader-server-kit/internal/manifest"
	"github.com/IGlace/abyssreader-server-kit/internal/pages"
	"github.com/IGlace/abyssreader-server-kit/internal/store/sqlcdb"
)

// Hosted chapters: the server is one source of its own, the hosted source. A
// hosted title has one source title on it per language (its key in the manifest), each with
// its chapters; a chapter's pages are files of the import folder, checked, cut into slices
// when taller than pages.MaxHeight, and stored by content in the page store.
//
// Import order inside one title: the title's own transaction makes or updates its hosted
// source titles (and drops the languages the entry no longer lists); then every chapter is
// imported in its own transaction, after its page files are stored one by one. A page file
// already stored (same bytes) is used again without being read as an image. At the end of an
// import, page files no chapter uses any more are deleted with their images.

// Rights are a hosted source title's licence and credit facts.
type Rights = manifest.Rights

// Chapter is one hosted chapter as the read routes show it.
type Chapter struct {
	ID            int64
	Number        *float64
	Name          *string
	Volume        *float64
	Credits       *string
	PublishedAt   time.Time
	PageCount     int64
	SourceTitleID int64
}

// ChapterPage is one served page image of a chapter.
type ChapterPage struct {
	Image         string // the stored file's name in the page store
	Width, Height int64
}

// ChapterCounts says what an import did with the hosted chapters.
type ChapterCounts struct{ Added, Updated, Unchanged, Removed int }

// PageCounts says how many page files an import read and stored, and how many it found
// stored already.
type PageCounts struct{ Stored, Reused int }

// UsePages gives the catalog its page store, which importing and removing hosted titles need.
func (c *Catalog) UsePages(s *covers.Store) *Catalog {
	c.pages = s
	return c
}

// RemoveChapters allows an import to remove hosted chapters its manifest no longer lists
// (`admin import --remove-chapters`). Without it, such chapters are kept and listed in
// KeptChapters: a removed chapter loses its id, and readers their marks on it.
func (c *Catalog) RemoveChapters(on bool) *Catalog {
	c.removeChapters = on
	return c
}

// chapterRemovals collects the chapters an import removed, or kept because removing needs
// RemoveChapters, as "ref/lang/chapter".
type chapterRemovals struct{ removed, kept []string }

// ShowHosted says whether the read side shows the hosted source and its source titles: true
// while the optional feature hosted-chapters is on. Off, they are hidden and kept.
func (c *Catalog) ShowHosted(on bool) *Catalog {
	c.showHosted = on
	return c
}

// hostedURL is the server-relative url of a hosted source title.
func hostedURL(id int64) string { return "/source-titles/" + strconv.FormatInt(id, 10) }

// ensureHostedSource returns the hosted source's id, making it on the first hosted import.
func (c *Catalog) ensureHostedSource(ctx context.Context) (int64, error) {
	s, err := c.q.GetHostedSource(ctx)
	if err == nil {
		return s.ID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if _, err := c.q.GetSourceBySlug(ctx, manifest.HostedSourceSlug); err == nil {
		return 0, fmt.Errorf("a source with the slug %q was imported before hosted chapters existed; the slug now names the server's own hosted source", manifest.HostedSourceSlug)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	now := c.now().UnixMilli()
	return c.q.InsertHostedSource(ctx, sqlcdb.InsertHostedSourceParams{Slug: manifest.HostedSourceSlug, Name: manifest.HostedSourceSlug, CreatedAt: now, UpdatedAt: now})
}

// importHostedSourceTitles makes and updates a title's hosted source titles, inside the
// title's transaction. An entry without `hosted` leaves them as they are: a catalog-only
// manifest never touches hosted content. A language the entry no longer lists is removed with
// its chapters only when removal is allowed; otherwise it is kept and its chapters are listed.
func importHostedSourceTitles(ctx context.Context, q *sqlcdb.Queries, ref string, titleID, hostedID int64, hosted []manifest.Hosted, remove bool, rm *chapterRemovals) error {
	if len(hosted) == 0 {
		return nil
	}
	have, err := q.ListHostedSourceTitlesOfTitle(ctx, titleID)
	if err != nil {
		return err
	}
	byLang := make(map[string]int64, len(have))
	for _, h := range have {
		byLang[h.Lang] = h.ID
	}
	for i, h := range hosted {
		id, ok := byLang[h.Lang]
		if ok {
			delete(byLang, h.Lang)
			err = q.UpdateSourceTitle(ctx, sqlcdb.UpdateSourceTitleParams{Lang: h.Lang, Position: int64(i), ID: id})
		} else {
			// The url names the id, which exists only once the row does.
			id, err = q.InsertHostedSourceTitle(ctx, sqlcdb.InsertHostedSourceTitleParams{
				TitleID: titleID, SourceID: hostedID, Url: "/source-titles/new-" + h.Lang, Lang: h.Lang, Position: int64(i),
			})
			if err == nil {
				err = q.SetSourceTitleURL(ctx, sqlcdb.SetSourceTitleURLParams{Url: hostedURL(id), ID: id})
			}
		}
		if err != nil {
			return err
		}
		var rights sql.NullString
		if h.Rights != nil {
			b, _ := json.Marshal(h.Rights) // a struct of string pointers always marshals
			rights = sql.NullString{String: string(b), Valid: true}
		}
		if err := q.PutHostedSourceTitle(ctx, sqlcdb.PutHostedSourceTitleParams{SourceTitleID: id, ReadingMode: nullString(h.ReadingMode), Rights: rights}); err != nil {
			return err
		}
	}
	dropped := slices.Sorted(maps.Keys(byLang))
	for k, lang := range dropped {
		id := byLang[lang]
		refs, err := q.ListChapterRefs(ctx, id)
		if err != nil {
			return err
		}
		names := make([]string, len(refs))
		for j, cr := range refs {
			names[j] = ref + "/" + lang + "/" + cr.Ref
		}
		slices.Sort(names)
		if len(refs) > 0 && !remove {
			rm.kept = append(rm.kept, names...)
			if err := q.UpdateSourceTitle(ctx, sqlcdb.UpdateSourceTitleParams{Lang: lang, Position: int64(len(hosted) + k), ID: id}); err != nil {
				return err
			}
			continue
		}
		// Its chapters go with it; their page files are swept at the end of the import.
		if err := q.DeleteSourceTitle(ctx, id); err != nil {
			return err
		}
		rm.removed = append(rm.removed, names...)
	}
	return nil
}

// importChapters imports the chapters of a title that was just imported. A chapter that
// cannot be taken (a page outside the limits, a missing file, ...) is listed in
// ChapterFailures and keeps what it had; the others are imported. Chapters the entry no longer
// lists are removed.
func (c *Catalog) importChapters(ctx context.Context, root *os.Root, t manifest.Title, r *Report) error {
	row, err := c.q.GetTitleByRef(ctx, t.Ref)
	if err != nil {
		return err
	}
	sts, err := c.q.ListHostedSourceTitlesOfTitle(ctx, row.ID)
	if err != nil {
		return err
	}
	for _, h := range t.Hosted {
		var stID int64
		for _, st := range sts {
			if st.Lang == h.Lang {
				stID = st.ID
			}
		}
		if stID == 0 {
			return fmt.Errorf("hosted source title %s of %s is missing after its import", h.Lang, t.Ref)
		}
		listed := make(map[string]bool, len(h.Chapters))
		for pos, ch := range h.Chapters {
			listed[ch.Ref] = true
			if err := ctx.Err(); err != nil {
				return err
			}
			outcome, err := c.importChapter(ctx, root, stID, pos, ch, &r.Pages)
			var tf titleFailure
			switch {
			case errors.As(err, &tf):
				r.ChapterFailures = append(r.ChapterFailures, Failure{t.Ref + "/" + h.Lang + "/" + ch.Ref, tf.reason})
				continue
			case err != nil:
				return err
			}
			switch outcome {
			case "added":
				r.Chapters.Added++
			case "updated":
				r.Chapters.Updated++
			default:
				r.Chapters.Unchanged++
			}
		}
		refs, err := c.q.ListChapterRefs(ctx, stID)
		if err != nil {
			return err
		}
		slices.SortFunc(refs, func(a, b sqlcdb.ListChapterRefsRow) int { return strings.Compare(a.Ref, b.Ref) })
		for _, cr := range refs {
			if listed[cr.Ref] {
				continue
			}
			name := t.Ref + "/" + h.Lang + "/" + cr.Ref
			if !c.removeChapters {
				r.KeptChapters = append(r.KeptChapters, name)
				continue
			}
			if err := c.q.DeleteChapter(ctx, cr.ID); err != nil {
				return err
			}
			r.Chapters.Removed++
			r.RemovedChapters = append(r.RemovedChapters, name)
		}
	}
	return nil
}

// importChapter stores a chapter's page files one by one, then records the chapter in one
// transaction. An unchanged entry with unchanged page files is left alone.
func (c *Catalog) importChapter(ctx context.Context, root *os.Root, stID int64, pos int, ch manifest.Chapter, counts *PageCounts) (string, error) {
	files := make([]string, len(ch.Pages))
	var pageCount int64
	for i, p := range ch.Pages {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		sha, err := pages.HashFile(root, p.File)
		if err != nil {
			return "", failf("page %d, %v", i+1, err)
		}
		n, err := c.storedPageFile(ctx, sha, i, p)
		if err != nil {
			return "", err
		}
		if n > 0 {
			counts.Reused++
		} else {
			src, err := c.prepare(root, p.File, p.Width, p.Height, c.pages)
			switch {
			case ctx.Err() != nil:
				return "", ctx.Err()
			case errors.Is(err, pages.ErrStore):
				return "", err
			case err != nil:
				return "", failf("page %d, %v", i+1, err)
			}
			// What is stored is recorded even when the import is being stopped, so that a
			// re-run uses it.
			if err := c.recordPageFile(context.WithoutCancel(ctx), src); err != nil {
				return "", err
			}
			counts.Stored++
			sha, n = src.SHA256, int64(len(src.Slices))
		}
		files[i] = sha
		if pageCount += n; pageCount > pages.MaxChapterImages {
			return "", failf("the chapter would have more than %d page images (its pages and the slices of its long strips); split it into several chapters", pages.MaxChapterImages)
		}
	}
	sum := sha256.Sum256([]byte(ch.Hash() + " " + strings.Join(files, " ")))
	hash := hex.EncodeToString(sum[:])

	old, err := c.q.GetChapterByRef(ctx, sqlcdb.GetChapterByRefParams{SourceTitleID: stID, Ref: ch.Ref})
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if exists && old.ManifestHash == hash {
		if old.Position != int64(pos) {
			if err := c.q.SetChapterPosition(ctx, sqlcdb.SetChapterPositionParams{Position: int64(pos), ID: old.ID}); err != nil {
				return "", err
			}
		}
		return "unchanged", nil
	}
	published := c.now().UnixMilli()
	switch {
	case ch.PublishedAt != nil:
		published = ch.Published().UnixMilli()
	case exists:
		published = old.PublishedAt // the first import's time stays
	}
	err = c.inTx(ctx, func(q *sqlcdb.Queries) error {
		id := old.ID
		var err error
		if exists {
			err = q.UpdateChapter(ctx, sqlcdb.UpdateChapterParams{
				Position: int64(pos), Number: nullFloat(ch.Number), Name: nullString(ch.Name), Volume: nullFloat(ch.Volume),
				Credits: nullString(ch.Credits), PublishedAt: published, PageCount: pageCount, ManifestHash: hash, ID: id,
			})
		} else {
			id, err = q.InsertChapter(ctx, sqlcdb.InsertChapterParams{
				SourceTitleID: stID, Ref: ch.Ref, Position: int64(pos), Number: nullFloat(ch.Number), Name: nullString(ch.Name),
				Volume: nullFloat(ch.Volume), Credits: nullString(ch.Credits), PublishedAt: published, PageCount: pageCount, ManifestHash: hash,
			})
		}
		if err != nil {
			return err
		}
		if err := q.DeleteChapterPages(ctx, id); err != nil {
			return err
		}
		for i, sha := range files {
			if err := q.InsertChapterPage(ctx, sqlcdb.InsertChapterPageParams{ChapterID: id, Position: int64(i), FileSha256: sha}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if exists {
		return "updated", nil
	}
	return "added", nil
}

// storedPageFile returns how many images the page file sha is served as when it is stored
// already (0 when it must be read). A declared size must be the file's own.
func (c *Catalog) storedPageFile(ctx context.Context, sha string, i int, p manifest.Page) (int64, error) {
	pf, err := c.q.GetPageFile(ctx, sha)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	imgs, err := c.q.ListPageImagesOfFile(ctx, sha)
	if err != nil {
		return 0, err
	}
	for _, img := range imgs {
		if !c.pages.Has(img.Image) {
			return 0, nil // read it again; the record is replaced
		}
	}
	if p.Width != 0 && int64(p.Width) != pf.Width {
		return 0, failf("page %d, %s: declared width %d, the file is %d px wide", i+1, p.File, p.Width, pf.Width)
	}
	if p.Height != 0 && int64(p.Height) != pf.Height {
		return 0, failf("page %d, %s: declared height %d, the file is %d px tall", i+1, p.File, p.Height, pf.Height)
	}
	return int64(len(imgs)), nil
}

// recordPageFile records what a page file became.
func (c *Catalog) recordPageFile(ctx context.Context, src pages.Source) error {
	return c.inTx(ctx, func(q *sqlcdb.Queries) error {
		if err := q.PutPageFile(ctx, sqlcdb.PutPageFileParams{Sha256: src.SHA256, Width: int64(src.Width), Height: int64(src.Height)}); err != nil {
			return err
		}
		if err := q.DeletePageImagesOfFile(ctx, src.SHA256); err != nil {
			return err
		}
		for i, s := range src.Slices {
			err := q.InsertPageImage(ctx, sqlcdb.InsertPageImageParams{FileSha256: src.SHA256, Slice: int64(i), Image: s.Name, Width: int64(s.Width), Height: int64(s.Height)})
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// sweepPages deletes the page files no chapter uses, and every image of theirs no other page
// file uses. Then it removes what the page store holds that no row names: slices a stopped or
// failed import stored and never recorded, images a re-read page file no longer gives, and
// half-written files. Only files with a stored image's name are touched, and never the covers.
func (c *Catalog) sweepPages(ctx context.Context) error {
	if c.pages == nil {
		return nil
	}
	unused, err := c.q.ListUnusedPageFiles(ctx)
	if err != nil {
		return err
	}
	for _, sha := range unused {
		if err := c.q.DeletePageFile(ctx, sha); err != nil { // its page_images rows go with it
			return err
		}
	}
	named, err := c.q.ListPageImageNames(ctx)
	if err != nil {
		return err
	}
	return c.pages.Prune(named)
}

// ── Reading ──

// hostedLang is the hosted source's lang: the one lang of its source titles, or "mul"
// (ISO 639-2: several languages) when they have more than one.
func (c *Catalog) hostedLang(ctx context.Context) (string, error) {
	langs, err := c.q.ListHostedLangs(ctx)
	switch {
	case err != nil:
		return "", err
	case len(langs) == 1:
		return langs[0], nil
	case len(langs) == 0:
		return "en", nil
	}
	return "mul", nil
}

// Chapters returns one page of a hosted source title's chapters by number (chapters without
// one last), and how many it has. ErrNotFound when the id is no hosted source title.
func (c *Catalog) Chapters(ctx context.Context, sourceTitleID int64, page, size int, asc bool) ([]Chapter, int64, error) {
	if _, err := c.q.GetHostedSourceTitleID(ctx, sourceTitleID); errors.Is(err, sql.ErrNoRows) {
		return nil, 0, ErrNotFound
	} else if err != nil {
		return nil, 0, err
	}
	total, err := c.q.CountChapters(ctx, sourceTitleID)
	if err != nil {
		return nil, 0, err
	}
	offset := int64(page-1) * int64(size)
	if offset >= total {
		return []Chapter{}, total, nil
	}
	var rows []sqlcdb.Chapter
	if asc {
		rows, err = c.q.ListChaptersByNumberAsc(ctx, sqlcdb.ListChaptersByNumberAscParams{SourceTitleID: sourceTitleID, Limit: int64(size), Offset: offset})
	} else {
		rows, err = c.q.ListChaptersByNumberDesc(ctx, sqlcdb.ListChaptersByNumberDescParams{SourceTitleID: sourceTitleID, Limit: int64(size), Offset: offset})
	}
	if err != nil {
		return nil, 0, err
	}
	out := make([]Chapter, len(rows))
	for i, r := range rows {
		out[i] = chapterOf(r)
	}
	return out, total, nil
}

func chapterOf(r sqlcdb.Chapter) Chapter {
	ch := Chapter{ID: r.ID, PublishedAt: millis(r.PublishedAt), PageCount: r.PageCount, SourceTitleID: r.SourceTitleID}
	if r.Number.Valid {
		ch.Number = &r.Number.Float64
	}
	if r.Volume.Valid {
		ch.Volume = &r.Volume.Float64
	}
	if r.Name.Valid {
		ch.Name = &r.Name.String
	}
	if r.Credits.Valid {
		ch.Credits = &r.Credits.String
	}
	return ch
}

// ChapterPages returns a chapter's served images in reading order. ErrNotFound when there is
// no such chapter.
func (c *Catalog) ChapterPages(ctx context.Context, chapterID int64) ([]ChapterPage, error) {
	if _, err := c.q.GetChapter(ctx, chapterID); errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	rows, err := c.q.ListChapterImages(ctx, sqlcdb.ListChapterImagesParams{ChapterID: chapterID, Limit: pages.MaxChapterImages})
	if err != nil {
		return nil, err
	}
	out := make([]ChapterPage, len(rows))
	for i, r := range rows {
		out[i] = ChapterPage{Image: r.Image, Width: r.Width, Height: r.Height}
	}
	return out, nil
}

// rightsOf reads a stored rights record.
func rightsOf(s sql.NullString) *Rights {
	if !s.Valid {
		return nil
	}
	var r Rights
	if err := json.Unmarshal([]byte(s.String), &r); err != nil {
		return nil // written by the import from the same struct; never expected
	}
	return &r
}
