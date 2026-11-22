// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package usersync

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"strings"
	"time"

	"github.com/pb33f/jsonschema/v6"
)

// RestoreWriter belongs to one file and account. Use it sequentially across
// batches. It caches only row IDs for exceptional URL spellings, never row data.
type RestoreWriter struct {
	s        *Service
	userID   int64
	hosted   bool
	aliases  map[[32]byte]aliasIDs
	seenSeq  int64
	counts   map[Kind]int
	countSeq int64
}

// NewRestoreWriter starts one account-scoped restore session.
func (s *Service) NewRestoreWriter(userID int64, hosted bool) *RestoreWriter {
	return &RestoreWriter{s: s, userID: userID, hosted: hosted}
}

// RestoreBatch is the convenience method for a single batch.
func (s *Service) RestoreBatch(ctx context.Context, userID int64, changes []Change, hosted bool) ([]Result, error) {
	return s.NewRestoreWriter(userID, hosted).RestoreBatch(ctx, changes)
}

type titleSource struct {
	title int64
	slug  string
}

func restoreRecord(c Change, now int64) (*record, error) {
	if c.Deleted || c.Kind == Inbox {
		return nil, nil
	}
	if c.ClientUpdatedAt != "" {
		at, err := time.Parse(time.RFC3339Nano, c.ClientUpdatedAt)
		if err != nil || at.Before(time.Unix(0, 0)) {
			return nil, nil
		}
		c.ClientUpdatedAt = format(ms(at))
	} else {
		c.ClientUpdatedAt = format(now)
	}
	if c.Kind == ChapterMark {
		url := ChapterPath(c.Key.ChapterURL)
		key := ChapterPathKey(url)
		c.Key.ChapterURL = key
		if url == alternatePath(key) {
			c.Key.ChapterURL = url
		}
	}
	if _, err := itemSchema(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return nil, nil
	}
	obj, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil || changeItem.Validate(obj) != nil {
		return nil, nil
	}
	r := &record{kind: c.Kind, key: c.Key}
	r.at, _ = parseTime(c.ClientUpdatedAt)
	r.clamp(now)
	if !c.Kind.IsList() {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(c.Data, &fields); err != nil {
			return nil, nil
		}
		var ok bool
		if r.data, ok = normalise(c.Kind, fields); !ok {
			return nil, nil
		}
	}
	return r, nil
}

// RestoreBatch makes at most MaxBatch decisions in a single write transaction;
// validation and the initial account-scoped URL scan run without the write lock.
func (w *RestoreWriter) RestoreBatch(ctx context.Context, changes []Change) ([]Result, error) {
	if len(changes) > MaxBatch {
		return nil, errors.New("restore batch exceeds sync batch limit")
	}
	results := make([]Result, len(changes))
	if len(changes) == 0 {
		return results, nil
	}
	now := ms(w.s.now())
	parsed := make([]*record, len(changes))
	subjects := make([]titleSource, len(changes))
	needed := map[titleSource]bool{}
	hasMarks := false
	for i, c := range changes {
		r, err := restoreRecord(c, now)
		if err != nil {
			return nil, err
		}
		if r == nil {
			results[i] = Result{Status: "rejected", Reason: "invalid"}
			continue
		}
		parsed[i] = r
		subject := titleSource{title: r.key.MainMangaID}
		if slugs := r.slugs(); len(slugs) > 0 {
			subject.slug = slugs[0]
		}
		subjects[i] = subject
		needed[subject] = true
		hasMarks = hasMarks || r.kind == ChapterMark
	}
	if hasMarks && w.aliases == nil {
		if err := w.loadAliases(ctx); err != nil {
			return nil, err
		}
	}
	var committed int64
	var counts map[Kind]int
	err := w.s.write(ctx, w.userID, func(tx *sql.Tx, seq *int64) error {
		reasons, err := restoreVisibility(ctx, tx, needed, w.hosted)
		if err != nil {
			return err
		}
		if hasMarks && w.seenSeq < *seq {
			if err := w.refreshAliases(ctx, tx, w.seenSeq); err != nil {
				return err
			}
		}
		current, err := w.restoreRows(ctx, tx, parsed)
		if err != nil {
			return err
		}
		counts = map[Kind]int{}
		// Push, removal and restore all advance the account sequence. Purging
		// tombstones only lowers the total, so the cached upper bound is safe
		// until it would refuse a write; then recount inside the transaction.
		if w.counts != nil && w.countSeq == *seq {
			counts = maps.Clone(w.counts)
			if total, ok := counts[allKinds]; ok && total+len(changes) > w.s.maxStored {
				delete(counts, allKinds)
			}
		}
		var writes []record
		for i, r := range parsed {
			if r == nil {
				continue
			}
			if reason := reasons[subjects[i]]; reason != "" {
				results[i] = Result{Status: "rejected", Reason: reason}
				continue
			}
			key := restoreIdentity(*r)
			cur := current[key]
			if cur != nil && r.kind == ChapterMark {
				r.key.ChapterURL = cur.key.ChapterURL
			}
			if cur != nil && !cur.deleted {
				if changes[i].RestoreNameIsFallback {
					keepRestoreChapterName(cur, r)
				}
				if !mergeRestore(cur, r) {
					results[i].Status = "stale"
					continue
				}
			}
			status, reason, err := w.s.decide(ctx, tx, w.userID, *r, cur, counts)
			if err != nil {
				return err
			}
			results[i] = Result{Status: status, Reason: reason}
			if status == "applied" {
				writes = append(writes, *r)
				current[key] = r
			}
		}
		if len(writes) > 0 {
			if err := put(ctx, tx, w.userID, writes, *seq+1, now); err != nil {
				return err
			}
			*seq += int64(len(writes))
		}
		committed = *seq
		return nil
	})
	if err != nil {
		return nil, err
	}
	w.countSeq, w.counts = committed, counts
	if hasMarks {
		w.seenSeq = committed
	}
	return results, nil
}

func keepRestoreChapterName(cur, in *record) {
	var stored struct {
		ChapterName json.RawMessage
		ChapterURL  string
		SourceSlug  string
	}
	if json.Unmarshal(cur.data, &stored) != nil {
		return
	}
	var name string
	if json.Unmarshal(stored.ChapterName, &name) != nil || strings.TrimSpace(name) == "" {
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(in.data, &fields) != nil || fields == nil {
		return
	}
	if in.kind == Position {
		var url, source string
		_ = json.Unmarshal(fields["chapterUrl"], &url)
		_ = json.Unmarshal(fields["sourceSlug"], &source)
		if source != stored.SourceSlug || ChapterPathKey(url) != ChapterPathKey(stored.ChapterURL) {
			return
		}
	}
	fields["chapterName"] = stored.ChapterName
	in.data, _ = normalise(in.kind, fields)
}

// Restore only replaces differing values when the file is newer. An old file
// may still add chapter flags; stamp that merge beyond the stored time so
// devices take it on their next pull.
func mergeRestore(cur, in *record) bool {
	if in.kind.IsList() || equalData(cur.data, in.data) {
		return false
	}
	if in.kind != ChapterMark {
		return in.at > cur.at
	}
	var stored, file map[string]json.RawMessage
	_ = json.Unmarshal(cur.data, &stored)
	_ = json.Unmarshal(in.data, &file)
	for _, k := range []string{"read", "bookmarked"} {
		if string(file[k]) == "true" {
			stored[k] = json.RawMessage("true")
		}
	}
	newer := in.at > cur.at
	if newer {
		for _, k := range []string{"chapterNumber", "chapterName", "lastPageRead"} {
			if v := file[k]; len(v) > 0 && string(v) != "null" {
				stored[k] = v
			}
		}
	}
	in.data, _ = normalise(ChapterMark, stored)
	if equalData(in.data, cur.data) {
		return false
	}
	if !newer {
		in.at = cur.at + 1
	}
	return true
}

// Push normalizes the order and types of fields but preserves wire string
// escapes. Different legal spellings of the same string are equal on replay.
func equalData(a, b []byte) bool {
	if bytes.Equal(a, b) {
		return true
	}
	var left, right map[string]any
	return json.Unmarshal(a, &left) == nil && json.Unmarshal(b, &right) == nil && reflect.DeepEqual(left, right)
}

// A match is a hint from an earlier read. Verify title visibility and any
// named source-title association inside the write transaction; both can vanish
// between batches during a catalog import or hosted-content purge.
func restoreVisibility(ctx context.Context, tx *sql.Tx, needed map[titleSource]bool, hosted bool) (map[titleSource]string, error) {
	out := map[titleSource]string{}
	if len(needed) == 0 {
		return out, nil
	}
	args := make([]any, 0, 2*len(needed)+2)
	for k := range needed {
		args = append(args, k.title, k.slug)
		out[k] = "unknown_title"
	}
	args = append(args, hosted, hosted)
	rows, err := tx.QueryContext(ctx, `WITH wanted(title,slug) AS (VALUES `+rowPlaceholders(2, len(needed))+`)
		SELECT title,slug,slug='' OR EXISTS (SELECT 1 FROM source_titles st JOIN sources s ON s.id=st.source_id
			WHERE st.title_id=w.title AND s.slug=w.slug AND (? OR s.hosted=0))
		FROM wanted w WHERE EXISTS (SELECT 1 FROM source_titles st JOIN sources s ON s.id=st.source_id
			WHERE st.title_id=w.title AND (? OR s.hosted=0))`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k titleSource
		var valid bool
		if err := rows.Scan(&k.title, &k.slug, &valid); err != nil {
			return nil, err
		}
		if valid {
			delete(out, k)
		} else {
			out[k] = "unknown_source"
		}
	}
	return out, rows.Err()
}
