// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"context"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

type fileKey struct {
	kind       usersync.Kind
	title      int64
	slug, path string
}

type occurrence struct{ entry, chapter int }

// A winning chapter borrows strings from the decoded file. Keep a value copy
// because completed entries release their chapter slices between batches.
type fileWinner struct {
	entry   int
	chapter Chapter
}

type entryPlanner func(*Entry, match, time.Time, emitChange) error

// A repeated input key must propose the same merged value every time it is
// offered. Otherwise a later restore time can let a losing untimed duplicate
// overwrite the winner, even though neither the file nor the user changed.
// Winners retain decoded metadata. Serialized changes live only in a write batch.
func duplicatePlanner(ctx context.Context, entries []Entry, matches []match, now time.Time) entryPlanner {
	if len(matches) == 0 {
		return plan
	}
	first := make(map[fileKey]occurrence)
	repeated := make(map[fileKey][]occurrence)
	add := func(key fileKey, loc occurrence) {
		if earlier, ok := first[key]; ok {
			if repeated[key] == nil {
				repeated[key] = []occurrence{earlier}
			}
			repeated[key] = append(repeated[key], loc)
		} else {
			first[key] = loc
		}
	}
	for i := range entries {
		if ctx.Err() != nil {
			return plan
		}
		m := matches[i]
		if m.slug == "" {
			continue
		}
		e := &entries[i]
		if e.Extra != nil && (e.Extra.Vote == "up" || e.Extra.Vote == "down") {
			add(fileKey{kind: usersync.Rating, title: m.title}, occurrence{entry: i})
		}
		if h := e.History; h != nil && h.LastRead > 0 && len(usersync.ChapterPath(h.URL)) >= 2 {
			add(fileKey{kind: usersync.Position, title: m.title}, occurrence{entry: i})
		}
		for j, c := range e.Chapters {
			if !hasMark(c) {
				continue
			}
			path := usersync.ChapterPathKey(c.URL)
			if len(path) >= 2 {
				add(fileKey{kind: usersync.ChapterMark, slug: m.slug, path: path}, occurrence{i, j})
			}
		}
	}
	if len(repeated) == 0 {
		return plan
	}
	fixed := make(map[fileKey]fileWinner, len(repeated))
	for key, locations := range repeated {
		if ctx.Err() != nil {
			return plan
		}
		winner := locations[0]
		at := fileTime(entries, key.kind, winner, now)
		var rank historyRank
		if key.kind == usersync.Position {
			rank = rankHistory(&entries[winner.entry])
		}
		var mark Chapter
		if key.kind == usersync.ChapterMark {
			mark = entries[winner.entry].Chapters[winner.chapter]
		}
		for _, next := range locations[1:] {
			ts := fileTime(entries, key.kind, next, now)
			newer := ts > at
			if key.kind == usersync.ChapterMark {
				in := entries[next.entry].Chapters[next.chapter]
				mark.Read = mark.Read || in.Read
				mark.Bookmark = mark.Bookmark || in.Bookmark
				if newer {
					mark.Modified = in.Modified
					if pageOf(in) != nil {
						mark.Page = in.Page
					}
					if numberOf(in) != nil {
						mark.Number = in.Number
					}
					if nameOf(in) != nil {
						mark.Name = in.Name
						mark.nameIsFallback = in.nameIsFallback
					}
				}
			}
			if key.kind == usersync.Position && ts >= at {
				nextRank := rankHistory(&entries[next.entry])
				if ts == at {
					newer = nextRank.number > rank.number || nextRank.number == rank.number && nextRank.page > rank.page
				}
				if newer {
					rank = nextRank
				}
			}
			if newer {
				winner, at = next, ts
			}
		}
		if key.kind == usersync.Position {
			if positionChapter := historyChapter(&entries[winner.entry]); positionChapter != nil {
				mark = *positionChapter
			}
		}
		fixed[key] = fileWinner{entry: winner.entry, chapter: mark}
	}
	return func(e *Entry, m match, now time.Time, emit emitChange) error {
		return plan(e, m, now, func(c usersync.Change) error {
			k := fileKey{kind: c.Kind, title: c.Key.MainMangaID}
			if c.Kind == usersync.ChapterMark {
				k = fileKey{kind: c.Kind, slug: c.Key.SourceSlug, path: usersync.ChapterPathKey(c.Key.ChapterURL)}
			}
			winner, ok := fixed[k]
			if !ok {
				return emit(c)
			}
			e := &entries[winner.entry]
			var single Entry
			switch k.kind {
			case usersync.Rating:
				single.Extra = &Extra{Vote: e.Extra.Vote}
				single.Modified = e.Modified
			case usersync.Position:
				single.History = e.History
				single.Chapters = []Chapter{winner.chapter}
			case usersync.ChapterMark:
				single.Chapters = []Chapter{winner.chapter}
			}
			return plan(&single, matches[winner.entry], now, func(merged usersync.Change) error {
				if merged.Kind != c.Kind {
					return nil
				}
				// Keep the owner's key for the write-lock catalog check.
				merged.Key = c.Key
				return emit(merged)
			})
		})
	}
}

func fileTime(entries []Entry, kind usersync.Kind, loc occurrence, now time.Time) int64 {
	e := &entries[loc.entry]
	at := e.Modified
	switch kind {
	case usersync.Position:
		at = e.History.LastRead
	case usersync.ChapterMark:
		at = e.Chapters[loc.chapter].Modified
	}
	return backupRecordMillis(at, now)
}

func historyChapter(e *Entry) *Chapter {
	// Prefer the exact chapter named by the flag over a normalized URL alias.
	if e.Extra != nil && e.Extra.PositionOnlyChapter {
		for i, c := range e.Chapters {
			if c.positionOnly {
				return &e.Chapters[i]
			}
		}
	}
	path := usersync.ChapterPathKey(e.History.URL)
	for i, c := range e.Chapters {
		if usersync.ChapterPathKey(c.URL) == path {
			return &e.Chapters[i]
		}
	}
	return nil
}

type historyRank struct {
	number float32
	page   int64
}

func rankHistory(e *Entry) historyRank {
	r := historyRank{number: -1, page: -1}
	c := historyChapter(e)
	if c == nil {
		return r
	}
	if numberOf(*c) != nil {
		r.number = c.Number
	}
	if c.Page > 0 {
		r.page = min(c.Page, 100000)
	}
	return r
}
