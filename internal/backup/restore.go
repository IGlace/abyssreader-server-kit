// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

type unmatchedEntry struct {
	Title      string  `json:"title"`
	SourceName *string `json:"sourceName"`
}
type Report struct {
	Titles struct {
		Matched       int `json:"matched"`
		MatchedByName int `json:"matchedByName"`
		Unmatched     int `json:"unmatched"`
	} `json:"titles"`
	ChapterMarks struct {
		Restored int `json:"restored"`
		Skipped  int `json:"skipped"`
	} `json:"chapterMarks"`
	Unmatched []unmatchedEntry `json:"unmatched"`
}

// Time-less backups use the restore time. Keep this contract choice in one place.
func backupRecordMillis(value int64, now time.Time) int64 {
	ms := value
	if ms > 0 && ms < 100_000_000_000 {
		ms *= 1000
	} // Mihon modifiedAt in seconds; history is normally milliseconds.
	if ms <= 0 || ms > now.Add(5*time.Minute).UnixMilli() {
		ms = now.UnixMilli()
	}
	return ms
}
func backupRecordTime(value int64, now time.Time) string {
	return time.UnixMilli(backupRecordMillis(value, now)).UTC().Format("2006-01-02T15:04:05.000Z")
}

func cleanText(s string, limit int) string {
	var b strings.Builder
	var n int
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = n > 0
			continue
		}
		if !unicode.IsPrint(r) {
			continue
		}
		if space {
			if n+1 >= limit {
				break
			}
			b.WriteByte(' ')
			n++
			space = false
		}
		b.WriteRune(r)
		n++
		if n == limit {
			break
		}
	}
	return b.String()
}

func hasMark(c Chapter) bool { return c.Read || c.Bookmark || (c.Page > 0 && !c.positionOnly) }

func numberOf(c Chapter) any {
	if c.Number < 0 || math.IsNaN(float64(c.Number)) || math.IsInf(float64(c.Number), 0) {
		return nil
	}
	return c.Number
}
func pageOf(c Chapter) any {
	if c.Page <= 0 {
		return nil
	}
	return min(c.Page, 100000)
}
func nameOf(c Chapter) any {
	if n := cleanText(c.Name, 1000); n != "" {
		return n
	}
	return nil
}

type emitChange func(usersync.Change) error

// Generate records directly into the next short write batch. No file-sized
// write plan or JSON tree survives alongside the decoded chapters.
func plan(e *Entry, m match, now time.Time, emit emitChange) error {
	key := usersync.Key{MainMangaID: m.title}
	send := func(k usersync.Kind, key usersync.Key, data any, at int64, fallbackName bool) error {
		var body json.RawMessage
		if data != nil {
			var err error
			body, err = json.Marshal(data)
			if err != nil {
				return err
			}
		}
		return emit(usersync.Change{Kind: k, Key: key, Data: body, ClientUpdatedAt: backupRecordTime(at, now), RestoreNameIsFallback: fallbackName})
	}
	for _, kind := range []usersync.Kind{usersync.Favorite, usersync.Planning, usersync.Blacklist} {
		member := kind == usersync.Favorite && e.Favorite
		if e.Extra != nil {
			member = slices.Contains(e.Extra.Lists, string(kind))
		}
		if member {
			at := e.Modified
			if kind == usersync.Favorite && e.FavoriteModified > 0 {
				at = e.FavoriteModified
			}
			if err := send(kind, key, nil, at, false); err != nil {
				return err
			}
		}
	}
	if m.slug == "" {
		return nil
	}
	if x := e.Extra; x != nil && (x.Vote == "up" || x.Vote == "down") {
		if err := send(usersync.Rating, key, map[string]string{"opinion": x.Vote}, e.Modified, false); err != nil {
			return err
		}
	}
	if h := e.History; h != nil && h.LastRead > 0 {
		if p := usersync.ChapterPath(h.URL); len(p) >= 2 {
			pos := map[string]any{"sourceSlug": m.slug, "chapterUrl": p}
			var fallbackName bool
			if c := historyChapter(e); c != nil {
				pos["chapterNumber"], pos["chapterName"], pos["pageIndex"] = numberOf(*c), nameOf(*c), pageOf(*c)
				fallbackName = c.nameIsFallback
			}
			if err := send(usersync.Position, key, pos, h.LastRead, fallbackName); err != nil {
				return err
			}
		}
	}
	for _, c := range e.Chapters {
		if !hasMark(c) {
			continue
		}
		p := usersync.ChapterPath(c.URL)
		if len(p) < 2 {
			continue
		}
		markKey := usersync.Key{MainMangaID: m.title, SourceSlug: m.slug, ChapterURL: p}
		data := map[string]any{"chapterNumber": numberOf(c), "chapterName": nameOf(c), "read": c.Read, "bookmarked": c.Bookmark, "lastPageRead": pageOf(c), "totalPages": nil}
		if err := send(usersync.ChapterMark, markKey, data, c.Modified, c.nameIsFallback); err != nil {
			return err
		}
	}
	return nil
}

type writeBatch func(context.Context, []usersync.Change) ([]usersync.Result, error)

const (
	restoreSetupAllowance = time.Minute
	restoreBatchTimeout   = 10 * time.Second
)

// Every retained chapter can produce one candidate, and an entry can add three
// list memberships, a vote and a position. Include stale and rejected candidates:
// they take the same queue, validation and transaction path as new writes.
func restoreTimeout(f *File, batchSize int) time.Duration {
	candidates := 5 * len(f.Entries)
	for i := range f.Entries {
		candidates += len(f.Entries[i].Chapters)
	}
	batches := (candidates + batchSize - 1) / batchSize
	// Leave time for matching and planning, and one second of work between batches.
	return restoreSetupAllowance + time.Duration(batches)*(restoreBatchTimeout+time.Second)
}

func Restore(ctx context.Context, db *sql.DB, svc *usersync.Service, userID int64, f *File, hosted bool, publicURL string) (Report, error) {
	return restore(ctx, db, f, hosted, publicURL, time.Now(), standardLimits.kept, svc.NewRestoreWriter(userID, hosted).RestoreBatch)
}

// A prefix may already be committed when a restore reaches one of these limits.
var ErrRestoreIncomplete = errors.New("restore work limit reached")
var ErrRestoreRetention = errors.New("restore chapter retention limit reached")
var ErrRestoreCapacity = errors.New("restore reached account storage limit")

// Drivers may return their own interrupt or rollback errors after cancellation.
func restoreError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrRestoreIncomplete
	}
	return err
}

type restoreBatches struct {
	ctx        context.Context
	write      writeBatch
	remaining  int
	changes    []usersync.Change
	owners     []int
	vanished   []bool
	atCapacity bool
	report     *Report
}

func (b *restoreBatches) flush() error {
	if len(b.changes) == 0 {
		return nil
	}
	if err := b.ctx.Err(); err != nil {
		return err
	}
	// A nearly exhausted write allowance must not turn a long stale tail
	// into one transaction per record. Process one last bounded prefix;
	// the next upload can verify it again in full-sized batches.
	clipped := len(b.changes) > b.remaining
	if clipped {
		b.changes = b.changes[:b.remaining]
	}
	ctx, cancel := context.WithTimeout(b.ctx, restoreBatchTimeout)
	defer cancel()
	results, err := b.write(ctx, b.changes)
	if err != nil {
		return restoreError(ctx, err)
	}
	for i, res := range results {
		if res.Status == "rejected" {
			switch res.Reason {
			case "unknown_title", "unknown_source":
				b.vanished[b.owners[i]] = true
			case "cap":
				b.atCapacity = true
			}
		}
		if res.Status == "applied" {
			b.remaining--
			if b.changes[i].Kind == usersync.ChapterMark {
				b.report.ChapterMarks.Restored++
				b.report.ChapterMarks.Skipped--
			}
		}
	}
	if clipped {
		return ErrRestoreIncomplete
	}
	clear(b.changes)
	b.changes = b.changes[:0]
	b.owners = b.owners[:0]
	return nil
}

func restore(ctx context.Context, db *sql.DB, f *File, hosted bool, publicURL string, now time.Time, maxWrites int, write writeBatch) (Report, error) {
	batchSize := max(1, min(maxWrites, usersync.MaxBatch))
	ctx, cancel := context.WithTimeout(ctx, restoreTimeout(f, batchSize))
	defer cancel()
	r := Report{Unmatched: []unmatchedEntry{}}
	r.ChapterMarks.Skipped = f.Dropped
	for _, e := range f.Entries {
		for _, c := range e.Chapters {
			if hasMark(c) {
				r.ChapterMarks.Skipped++
			}
		}
	}
	matches, err := matchSnapshot(ctx, db, f.Entries, hosted, publicURL)
	if err != nil {
		return r, restoreError(ctx, err)
	}
	planEntry := duplicatePlanner(ctx, f.Entries, matches, now)
	b := restoreBatches{ctx: ctx, write: write, remaining: maxWrites, vanished: make([]bool, len(f.Entries)), report: &r}
	for i := range f.Entries {
		if ctx.Err() != nil {
			err = ctx.Err()
			break
		}
		e := &f.Entries[i]
		if matches[i].title == 0 {
			e.Chapters = nil
			continue
		}
		err = planEntry(e, matches[i], now, func(c usersync.Change) error {
			if b.remaining == 0 {
				return ErrRestoreIncomplete
			}
			b.changes = append(b.changes, c)
			b.owners = append(b.owners, i)
			if len(b.changes) >= batchSize {
				return b.flush()
			}
			return nil
		})
		e.Chapters = nil
		if err != nil {
			break
		}
	}
	if err == nil {
		err = b.flush()
	}
	err = restoreError(ctx, err)
	if err != nil && !errors.Is(err, ErrRestoreIncomplete) {
		return r, err
	}
	if b.atCapacity {
		err = ErrRestoreCapacity
	} else if f.Dropped > 0 {
		err = ErrRestoreRetention
	}
	for i, e := range f.Entries {
		if matches[i].title != 0 && !b.vanished[i] {
			if matches[i].slug == "" {
				r.Titles.MatchedByName++
			} else {
				r.Titles.Matched++
			}
			continue
		}
		r.Titles.Unmatched++
		if len(r.Unmatched) >= 500 {
			continue
		}
		title := cleanText(e.Title, 200)
		if title == "" {
			title = cleanText(e.URL, 200)
		}
		if title == "" {
			title = "?"
		}
		line := unmatchedEntry{Title: title}
		if n := cleanText(f.SourceNames[e.Source], 200); n != "" {
			line.SourceName = &n
		}
		r.Unmatched = append(r.Unmatched, line)
	}
	*f = File{}
	return r, err
}
