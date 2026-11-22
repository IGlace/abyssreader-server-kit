// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"slices"
)

const exportPageSize = 128

// Drive the catalog lookups from the caller's live records. The CROSS JOIN
// keeps the indexed sync range as the outer loop regardless of table sizes.
const exportTitles = `SELECT t.id,t.name
	FROM sync_records r INDEXED BY sync_records_user_title CROSS JOIN titles t ON t.id=r.title_id
	WHERE r.title_id>? AND r.user_id=? AND r.deleted=0 AND r.kind<>'inbox'
	GROUP BY r.title_id ORDER BY r.title_id LIMIT ?`

type exporter struct {
	ctx                   context.Context
	db                    *sql.DB
	user                  int64
	hosted                bool
	publicURL, serverName string
	out                   *gzip.Writer
	named                 map[int64]bool
}

// Export streams the caller's live library and reading state. Short, independent
// SELECTs allow sync and account deletion to proceed even on a slow download.
// An error after the first write deliberately leaves the gzip trailer unwritten.
func Export(ctx context.Context, db *sql.DB, userID int64, hosted bool, publicURL, serverName string, w io.Writer) error {
	x := exporter{ctx: ctx, db: db, user: userID, hosted: hosted, publicURL: publicURL, serverName: serverName, out: gzip.NewWriter(w), named: map[int64]bool{}}
	type title struct {
		id   int64
		name string
	}
	scanTitle := func(r *sql.Rows) (title, error) {
		var t title
		err := r.Scan(&t.id, &t.name)
		return t, err
	}
	var after int64
	for {
		titles, err := readExportRows(ctx, db, exportTitles,
			scanTitle, after, userID, exportPageSize)
		if err != nil {
			return err
		}
		for _, t := range titles {
			if err := x.writeTitle(t.id, t.name); err != nil {
				return err
			}
			after = t.id
		}
		if len(titles) < exportPageSize {
			return x.out.Close()
		}
	}
}

// readExportRows closes the statement (and releases its connection) before the
// caller can write a result. Each query is limited by a page or by its key.
func readExportRows[T any](ctx context.Context, db *sql.DB, q string, scan func(*sql.Rows) (T, error), args ...any) ([]T, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var batch []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		batch = append(batch, v)
	}
	return batch, rows.Err()
}

type exportPosition struct {
	SourceSlug, ChapterURL, ChapterName string
	PageIndex                           int64
	ChapterNumber                       *float64
	at                                  int64
}

func (x *exporter) titleState(id int64, name string) (Entry, exportPosition, error) {
	e := Entry{Title: name, Extra: &Extra{}}
	var p exportPosition
	// At most one live row of each kind for (user, key_title).
	rows, err := x.db.QueryContext(x.ctx, `SELECT kind,data,client_updated_at FROM sync_records WHERE user_id=? AND key_title=? AND deleted=0
		AND kind IN ('favorite','planning','blacklist','rating','position') ORDER BY kind`, x.user, id)
	if err != nil {
		return e, p, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var data sql.NullString
		var at int64
		if err := rows.Scan(&kind, &data, &at); err != nil {
			return e, p, err
		}
		switch kind {
		case "position":
			if err := json.Unmarshal([]byte(data.String), &p); err != nil {
				return e, p, err
			}
			p.at = at
			continue
		case "rating":
			var d struct{ Opinion string }
			if err := json.Unmarshal([]byte(data.String), &d); err != nil {
				return e, p, err
			}
			e.Extra.Vote = d.Opinion
		default:
			e.Extra.Lists = append(e.Extra.Lists, kind)
			if kind == "favorite" {
				e.Favorite = true
				e.FavoriteModified = at / 1000
			}
		}
		e.Modified = max(e.Modified, at/1000)
	}
	return e, p, rows.Err()
}

type exportSource struct {
	id, sourceID, tachiyomiID, firstID int64
	slug, name, website, url           string
	hosted                             bool
}

// Hosted source-title URLs contain local row IDs. Migration 8 assigns the
// reserved source a negative ID scoped to this database, even when it moves.
const hostedBackupID = "(SELECT source_id FROM backup_hosted_identity WHERE singleton=1)"

// The manifest gives a set of numeric aliases; choose a stable, positive int64
// when available. Zero here signals that a stub ID must be derived.
const exportSources = `SELECT st.id,s.id,s.slug,s.name,s.website,st.url,s.hosted,
	COALESCE((SELECT CAST(tachiyomi_id AS INTEGER) FROM source_tachiyomi_ids INDEXED BY source_tachiyomi_ids_source WHERE source_id=s.id
	AND CAST(tachiyomi_id AS INTEGER)>0 AND CAST(CAST(tachiyomi_id AS INTEGER) AS TEXT)=ltrim(tachiyomi_id,'0')
	ORDER BY tachiyomi_id LIMIT 1),CASE WHEN s.hosted=1 THEN ` + hostedBackupID + ` ELSE 0 END),
	(SELECT f.id FROM source_titles f WHERE f.title_id=st.title_id AND f.source_id=s.id ORDER BY f.position,f.id LIMIT 1)
	FROM source_titles st JOIN sources s ON s.id=st.source_id WHERE st.title_id=? AND (s.hosted=0 OR ?) `

const exportActiveSources = exportSources + `AND st.id>? AND (s.slug=? OR EXISTS (
	SELECT 1 FROM sync_records r INDEXED BY sync_records_user_title WHERE r.title_id=st.title_id AND r.user_id=?
	AND r.deleted=0 AND r.kind='chapterMark' AND r.source_slug=s.slug)) ORDER BY st.id LIMIT ?`

const exportMarks = `SELECT r.id,r.chapter_url,r.data,r.client_updated_at FROM sync_records r INDEXED BY sync_records_user_title
	WHERE r.title_id=? AND r.user_id=? AND r.deleted=0 AND r.kind='chapterMark' AND r.source_slug=? AND r.id>? AND r.id<=? `

const exportMarkOrder = `ORDER BY r.id LIMIT ?`

func (x *exporter) scanExportSource(rows *sql.Rows) (exportSource, error) {
	var s exportSource
	err := rows.Scan(&s.id, &s.sourceID, &s.slug, &s.name, &s.website, &s.url, &s.hosted, &s.tachiyomiID, &s.firstID)
	if err != nil {
		return s, err
	}
	if s.hosted {
		s.name, s.website = x.serverName, x.publicURL
	}
	if s.tachiyomiID == 0 {
		// Mihon reserves zero for LocalSource. Its generated HTTP source IDs
		// and the manifest's mapped IDs are nonnegative. A stable negative
		// Long therefore identifies a missing source without claiming either.
		s.tachiyomiID = backupStubID(s.slug)
	}
	return s, nil
}

// The manifest slug survives website and public server address changes.
// Version the hash domain so cross-server implementations can pin test vectors.
func backupStubID(slug string) int64 {
	sum := sha256.Sum256([]byte("abyssreader:backup-source:v2\x00" + slug))
	return int64(binary.BigEndian.Uint64(sum[:8])) | math.MinInt64
}

func (x *exporter) writeTitle(id int64, name string) error {
	base, pos, err := x.titleState(id, name)
	if err != nil {
		return err
	}
	// Non-hosted sync keys only identify a source slug. Within that slug its
	// first catalog source title supplies the URL. Hosted chapter IDs also
	// identify the exact source title when multiple languages share the slug.
	var positionOwner int64
	if pos.SourceSlug == "hosted" && x.hosted && pos.ChapterURL != "" {
		if err := x.db.QueryRowContext(x.ctx, `SELECT COALESCE((SELECT c.source_title_id FROM chapters c JOIN source_titles st
			ON st.id=c.source_title_id WHERE c.id=CAST(substr(?,11) AS INTEGER) AND '/chapters/'||c.id=? AND st.title_id=?),0)`,
			pos.ChapterURL, pos.ChapterURL, id).Scan(&positionOwner); err != nil {
			return err
		}
	}
	var after int64
	wrote := false
	for {
		sources, err := readExportRows(x.ctx, x.db, exportActiveSources,
			x.scanExportSource, id, x.hosted, after, pos.SourceSlug, x.user, exportPageSize)
		if err != nil {
			return err
		}
		for _, s := range sources {
			after = s.id
			if !s.hosted && s.id != s.firstID {
				continue
			}
			ownsPos := pos.SourceSlug == s.slug && (s.id == positionOwner || (positionOwner == 0 && s.id == s.firstID))
			emitted, err := x.writeSourceTitle(id, base, s, pos, ownsPos)
			if err != nil {
				return err
			}
			wrote = wrote || emitted
		}
		if len(sources) < exportPageSize {
			break
		}
	}
	if wrote {
		return nil
	}
	// The kit currently publishes no definitions. Prefer a visible hosted
	// source, then the first community source as in the official exporter.
	sources, err := readExportRows(x.ctx, x.db, exportSources+`ORDER BY s.hosted DESC,st.position,st.id LIMIT 1`, x.scanExportSource, id, x.hosted)
	if err != nil || len(sources) == 0 {
		return err
	}
	e := x.entry(base, sources[0])
	if err := WriteEntry(x.out, e); err != nil {
		return err
	}
	return x.writeSourceName(sources[0])
}

func (x *exporter) entry(e Entry, s exportSource) Entry {
	extra := *e.Extra
	e.Extra = &extra
	e.Extra.Website = s.website
	e.Source = s.tachiyomiID
	e.URL = s.url
	return e
}

func (x *exporter) writeSourceName(s exportSource) error {
	if x.named[s.sourceID] {
		return nil
	}
	x.named[s.sourceID] = true
	return WriteSource(x.out, s.tachiyomiID, s.name)
}

type markPage struct {
	last               int64
	count              int
	hasPosition        bool
	body               []byte
	derivedNameIndexes []int
}

// readMarkPage encodes only to a private buffer while SQLite is reading. The
// response writer never runs with rows open.
func (x *exporter) readMarkPage(id int64, s exportSource, after, through int64, limit int, posURL string) (markPage, error) {
	q := exportMarks
	args := []any{id, x.user, s.slug, after, through}
	if s.hosted {
		// Unknown/removed chapter IDs follow the same first-source-title fallback.
		q += `AND COALESCE((SELECT c.source_title_id FROM chapters c JOIN source_titles st ON st.id=c.source_title_id
			WHERE c.id=CAST(substr(r.chapter_url,11) AS INTEGER) AND '/chapters/'||c.id=r.chapter_url AND st.title_id=r.title_id),?)=? `
		args = append(args, s.firstID, s.id)
	}
	rows, err := x.db.QueryContext(x.ctx, q+exportMarkOrder, append(args, limit)...)
	if err != nil {
		return markPage{}, err
	}
	defer rows.Close()
	var p markPage
	var buf bytes.Buffer
	for rows.Next() {
		var c Chapter
		var raw string
		var at int64
		if err := rows.Scan(&p.last, &c.URL, &raw, &at); err != nil {
			return p, err
		}
		var d struct {
			Read, Bookmarked bool
			LastPageRead     int64
			ChapterName      string
			ChapterNumber    *float64
		}
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			return p, err
		}
		c.Read, c.Bookmark = d.Read, d.Bookmarked
		c.Page = d.LastPageRead
		var derived bool
		c.Name, derived = exportChapterName(d.ChapterName, c.URL, d.ChapterNumber)
		if derived {
			p.derivedNameIndexes = append(p.derivedNameIndexes, p.count+1)
		}
		c.Modified = at / 1000
		if d.ChapterNumber != nil {
			c.Number = float32(*d.ChapterNumber)
		}
		if err := WriteChapter(&buf, c); err != nil {
			return p, err
		}
		p.count++
		p.hasPosition = p.hasPosition || c.URL == posURL
	}
	p.body = buf.Bytes()
	return p, rows.Err()
}

// A protobuf entry needs its byte length before its chapters. Keep the first
// page in memory. For larger entries, remember only each subsequent page's
// bounds, digest and indexes of generated names, then reread it after writing
// the length. A racing sync that changes the chapter or marker aborts the stream.
type markRange struct {
	first, last        int64
	count              int
	digest             [sha256.Size]byte
	derivedNameIndexes []int
}

// A very large source title may exceed the restore chapter limit. Continue it
// in another BackupManga with the same source and URL so every mark survives.
type chapterFrame struct {
	after              int64
	size               int64
	count              int
	first              []byte
	rest               []markRange
	end, matched       bool
	derivedNameIndexes []int
}

func (x *exporter) sizeFrame(id int64, s exportSource, after int64, posURL string) (chapterFrame, error) {
	f := chapterFrame{after: after}
	for f.count < MaxChaptersPerEntry {
		limit := min(exportPageSize, MaxChaptersPerEntry-f.count)
		p, err := x.readMarkPage(id, s, f.after, math.MaxInt64, limit, posURL)
		if err != nil {
			return f, err
		}
		if p.count != 0 {
			if f.count == 0 {
				f.first = p.body
			} else {
				f.rest = append(f.rest, markRange{
					first: f.after, last: p.last, count: p.count, digest: sha256.Sum256(p.body),
					derivedNameIndexes: p.derivedNameIndexes,
				})
			}
			for _, index := range p.derivedNameIndexes {
				f.derivedNameIndexes = append(f.derivedNameIndexes, f.count+index)
			}
			f.size += int64(len(p.body))
			f.count += p.count
			f.matched = f.matched || p.hasPosition
			f.after = p.last
		}
		if p.count < limit {
			f.end = true
			break
		}
	}
	return f, nil
}

func (x *exporter) writeFrame(id int64, s exportSource, entry Entry, f chapterFrame) error {
	// WriteEntryHeader emits synthesized chapters before the streamed marks.
	for _, index := range f.derivedNameIndexes {
		entry.Extra.derivedChapterNameIndexes = append(entry.Extra.derivedChapterNameIndexes, len(entry.Chapters)+index)
	}
	if err := WriteEntryHeader(x.out, entry, f.size); err != nil {
		return err
	}
	if len(f.first) > 0 {
		if err := writeAll(x.out, f.first); err != nil {
			return err
		}
	}
	for _, chunk := range f.rest {
		p, err := x.readMarkPage(id, s, chunk.first, chunk.last, chunk.count, "")
		if err != nil {
			return err
		}
		if p.count != chunk.count || sha256.Sum256(p.body) != chunk.digest ||
			!slices.Equal(p.derivedNameIndexes, chunk.derivedNameIndexes) {
			return errors.New("chapter marks changed during backup export")
		}
		if err := writeAll(x.out, p.body); err != nil {
			return err
		}
	}
	return nil
}

func (x *exporter) writeSourceTitle(id int64, base Entry, s exportSource, pos exportPosition, ownsPos bool) (bool, error) {
	var after int64
	emitted := false
	for {
		entry := x.entry(base, s)
		frame, err := x.sizeFrame(id, s, after, pos.ChapterURL)
		if err != nil {
			return false, err
		}
		after = frame.after
		// Put history in the same entry as its mark. When there is no mark,
		// synthesize the page-bearing chapter on the final entry.
		if ownsPos && pos.ChapterURL != "" && (frame.matched || frame.end) {
			entry.History = &History{URL: pos.ChapterURL, LastRead: pos.at}
			if !frame.matched {
				entry.Extra.PositionOnlyChapter = true
				name, derived := exportChapterName(pos.ChapterName, pos.ChapterURL, pos.ChapterNumber)
				if derived {
					entry.Extra.derivedChapterNameIndexes = []int{1}
				}
				c := Chapter{URL: pos.ChapterURL, Name: name, Page: pos.PageIndex, Modified: pos.at / 1000}
				if pos.ChapterNumber != nil {
					c.Number = float32(*pos.ChapterNumber)
				}
				entry.Chapters = []Chapter{c}
			}
		}
		if frame.count == 0 && (emitted && entry.History == nil || !ownsPos) {
			break
		}
		if err := x.writeFrame(id, s, entry, frame); err != nil {
			return false, err
		}
		emitted = true
		if entry.History != nil {
			ownsPos = false
		}
		if frame.end {
			break
		}
	}
	if emitted {
		return true, x.writeSourceName(s)
	}
	return false, nil
}
