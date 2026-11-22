// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package usersync

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/url"
	"slices"
	"strings"
	"unicode"
)

// ChapterPath reduces an absolute or root-relative file URL to its path and
// query. Fragments are reader-local and do not identify chapters.
func ChapterPath(raw string) string {
	s := strings.TrimSpace(raw)
	if strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 || strings.HasPrefix(s, "//") {
		return ""
	}
	s, _, _ = strings.Cut(s, "#")
	if strings.HasPrefix(s, "/") {
		return s
	}
	scheme, rest, ok := strings.Cut(s, "://")
	if !ok || !(strings.EqualFold(scheme, "http") || strings.EqualFold(scheme, "https")) {
		return ""
	}
	end := strings.IndexAny(rest, "/?")
	if end < 0 {
		end = len(rest)
	}
	origin, err := url.Parse(s[:len(s)-len(rest)+end])
	if err != nil || origin.Hostname() == "" {
		return ""
	}
	path := rest[end:]
	if strings.HasPrefix(path, "/") {
		return path
	}
	return "/" + path
}

// ChapterPathKey is the comparison spelling shared by catalog and mark matching.
func ChapterPathKey(raw string) string {
	path := ChapterPath(raw)
	if path == "" {
		return ""
	}
	p, _, _ := strings.Cut(path, "?")
	suffix := path[len(p):]
	p = strings.TrimRight(p, "/")
	if p == "" {
		p = "/"
	}
	return p + suffix
}

func alternatePath(canonical string) string {
	p, _, _ := strings.Cut(canonical, "?")
	if p == "/" {
		return canonical
	}
	return p + "/" + canonical[len(p):]
}

// One old absolute spelling per normalized key is typical. Allocate a set of
// additional IDs only when previous clients have actually stored duplicates.
type aliasIDs struct {
	first  int64
	others map[int64]bool
}

func aliasHash(slug, path string) [32]byte { return sha256.Sum256([]byte(slug + "\x00" + path)) }

// Ordinary relative URLs and their trailing-slash variants have indexed raw
// keys. Only other spellings need an account-scoped scan. A read snapshot sets
// its sequence watermark before loading them; later pushes are read by seq.
func (w *RestoreWriter) loadAliases(ctx context.Context) error {
	tx, err := w.s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var seq int64
	err = tx.QueryRowContext(ctx, `SELECT seq FROM sync_accounts WHERE user_id=?`, w.userID).Scan(&seq)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	w.aliases = map[[32]byte]aliasIDs{}
	if seq > 0 {
		if err := w.refreshAliases(ctx, tx, 0); err != nil {
			w.aliases = nil
			return err
		}
	}
	w.seenSeq = seq
	return nil
}

func (w *RestoreWriter) refreshAliases(ctx context.Context, tx *sql.Tx, since int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,source_slug,chapter_url FROM sync_records WHERE user_id=? AND seq>? AND kind='chapterMark'
		AND (substr(chapter_url,1,1)<>'/' OR instr(chapter_url,'#') OR instr(chapter_url,'//') OR chapter_url!=rtrim(chapter_url))`, w.userID, since)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var slug, url string
		if err := rows.Scan(&id, &slug, &url); err != nil {
			return err
		}
		path := ChapterPathKey(url)
		if path == "" || path == url || alternatePath(path) == url {
			continue
		}
		h := aliasHash(slug, path)
		ids := w.aliases[h]
		switch {
		case ids.first == 0:
			ids.first = id
		case ids.first != id:
			if ids.others == nil {
				ids.others = map[int64]bool{}
			}
			ids.others[id] = true
		}
		w.aliases[h] = ids
	}
	return rows.Err()
}

type restoreKey struct {
	kind Kind
	key  Key
}

func restoreIdentity(r record) restoreKey {
	k := r.key
	if r.kind == ChapterMark {
		k.MainMangaID = 0
		k.ChapterURL = ChapterPathKey(k.ChapterURL)
	}
	return restoreKey{r.kind, k}
}

func (w *RestoreWriter) restoreRows(ctx context.Context, tx *sql.Tx, parsed []*record) (map[restoreKey]*record, error) {
	keys := map[restoreKey]bool{}
	aliases := map[int64]bool{}
	for _, r := range parsed {
		if r == nil {
			continue
		}
		key := restoreIdentity(*r)
		keys[key] = true
		if key.kind == ChapterMark {
			if ids, ok := w.aliases[aliasHash(key.key.SourceSlug, key.key.ChapterURL)]; ok {
				aliases[ids.first] = true
				for id := range ids.others {
					aliases[id] = true
				}
			}
			key.key.ChapterURL = alternatePath(key.key.ChapterURL)
			keys[key] = true
		}
	}
	current := map[restoreKey]*record{}
	if len(keys) == 0 {
		return current, nil
	}
	args := make([]any, 0, 4*len(keys)+1)
	for k := range keys {
		r := record{kind: k.kind, key: k.key}
		title, slug, hash, _ := r.identity()
		args = append(args, k.kind, title, slug, hash)
	}
	args = append(args, w.userID)
	const cols = "r.kind,r.title_id,r.source_slug,r.chapter_url,r.data,r.client_updated_at,r.deleted"
	q := `WITH wanted(kind,title,slug,hash) AS (VALUES ` + rowPlaceholders(4, len(keys)) + `)
		SELECT ` + cols + ` FROM wanted w CROSS JOIN sync_records r WHERE r.user_id=? AND r.kind=w.kind AND r.key_title=w.title AND r.source_slug=w.slug AND r.url_hash=w.hash`
	if err := readRestoreRows(ctx, tx, q, args, current); err != nil {
		return nil, err
	}
	ids := make([]any, 0, len(aliases))
	for id := range aliases {
		ids = append(ids, id)
	}
	for chunk := range slices.Chunk(ids, MaxBatch) {
		q := `SELECT ` + cols + ` FROM sync_records r WHERE r.id IN ` + placeholders(len(chunk)) + ` AND r.user_id=?`
		args := append(slices.Clip(chunk), w.userID)
		if err := readRestoreRows(ctx, tx, q, args, current); err != nil {
			return nil, err
		}
	}
	return current, nil
}

func readRestoreRows(ctx context.Context, tx *sql.Tx, q string, args []any, current map[restoreKey]*record) error {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r record
		var data sql.NullString
		if err := rows.Scan(&r.kind, &r.key.MainMangaID, &r.key.SourceSlug, &r.key.ChapterURL, &data, &r.at, &r.deleted); err != nil {
			return err
		}
		if data.Valid {
			r.data = []byte(data.String)
		}
		key := restoreIdentity(r)
		// If a client pushed both spellings, choose the newest state. A
		// tombstone wins equal times; ties between live rows are deterministic.
		old := current[key]
		if old == nil || r.at > old.at || r.at == old.at && (r.deleted && !old.deleted || r.deleted == old.deleted && r.key.ChapterURL < old.key.ChapterURL) {
			current[key] = &r
		}
	}
	return rows.Err()
}
