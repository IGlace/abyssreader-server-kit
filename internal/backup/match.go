// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/IGlace/abyssreader-server-kit/internal/catalog"
	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
	"golang.org/x/text/unicode/norm"
	"modernc.org/sqlite"
)

type match struct {
	title int64
	slug  string
}
type catalogReader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// These comparison keys match the official backup reader. Computing them on read
// also covers existing manifests, without rewriting catalog names or URLs.
func init() {
	for name, normalize := range map[string]func(string) string{
		"backup_host":    websiteHost,
		"backup_name":    titleName,
		"backup_path":    usersync.ChapterPathKey,
		"backup_stub_id": func(slug string) string { return strconv.FormatInt(backupStubID(slug), 10) },
	} {
		sqlite.MustRegisterDeterministicScalarFunction(name, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			s, _ := args[0].(string)
			return normalize(s), nil
		})
	}
}

func websiteHost(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return strings.TrimPrefix(strings.TrimSuffix(strings.ToLower(u.Hostname()), "."), "www.")
}

var seasons = regexp.MustCompile(`\b(?:season|part|s)\s*([0-9]+)\b`)

func titleName(raw string) string {
	// Follow TitleNormalizer in the official catalog, including accents, articles,
	// format labels and season numbers. Only exact, unambiguous keys may match.
	s := strings.Map(func(r rune) rune {
		if unicode.IsMark(r) {
			return -1
		}
		return r
	}, norm.NFKD.String(raw))
	s = strings.ReplaceAll(strings.ToLower(norm.NFC.String(s)), "&", " and ")
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\'', '’', '‘', 0x60, '´':
			return -1
		}
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return r
		}
		return ' '
	}, s)
	var suffix []string
	s = seasons.ReplaceAllStringFunc(s, func(m string) string { suffix = append(suffix, "#s"+seasons.FindStringSubmatch(m)[1]); return " " })
	words := strings.Fields(s)
	for len(words) > 1 && strings.Contains(" the a an le la les el los las der die das ", " "+words[0]+" ") {
		words = words[1:]
	}
	var keep, plain []string
	for i := 0; i < len(words); i++ {
		w := words[i]
		if w == "light" && i+1 < len(words) && words[i+1] == "novel" {
			i++
			continue
		}
		plain = append(plain, w)
		if !strings.Contains(" manga manhwa manhua webtoon novel official colored coloured uncensored raw ", " "+w+" ") {
			keep = append(keep, w)
		}
	}
	if len(keep) == 0 {
		keep = plain
	}
	key := []rune(strings.Join(append(keep, suffix...), " "))
	return string(key[:min(len(key), 512)])
}

func jsonKeys[T comparable](values map[T]bool) string {
	keys := make([]T, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	data, _ := json.Marshal(keys)
	return string(data)
}

type sourcePath struct{ source, path string }

// Match all chunks on the caller's single read snapshot. Small keys share a
// query; long strings are split before JSON encoding and SQLite binding.
// The count ceiling also bounds generated keys (normalized names and slugs).
func matchEntries(ctx context.Context, db catalogReader, entries []Entry, hosted bool, publicURL string) ([]match, error) {
	const maxKeyBytes = 1 << 20
	const maxChunkEntries = 2048
	out := make([]match, 0, len(entries))
	for len(entries) > 0 {
		size, n := 0, 0
		for n < min(len(entries), maxChunkEntries) {
			e := &entries[n]
			keyBytes := len(e.URL) + len(e.Title)
			if e.Extra != nil {
				keyBytes += len(e.Extra.Website)
			}
			if n > 0 && size+keyBytes > maxKeyBytes {
				break
			}
			size += keyBytes
			n++
		}
		matches, err := matchChunk(ctx, db, entries[:n], hosted, publicURL)
		if err != nil {
			return nil, err
		}
		out = append(out, matches...)
		entries = entries[n:]
	}
	return out, nil
}

// Each chunk uses at most three bulk reads, independent of its chapters.
func matchChunk(ctx context.Context, db catalogReader, entries []Entry, hosted bool, publicURL string) ([]match, error) {
	ids, hosts, paths := map[string]bool{}, map[string]bool{}, map[string]bool{}
	wantedHost := map[sourcePath]bool{}
	hasStubs := false
	for i := range entries {
		e := &entries[i]
		path := usersync.ChapterPathKey(e.URL)
		if path == "" {
			continue
		}
		paths[path] = true
		if e.Source != 0 {
			ids[strconv.FormatInt(e.Source, 10)] = true
			hasStubs = hasStubs || e.Source < 0
		}
		if e.Extra != nil {
			if h := websiteHost(e.Extra.Website); h != "" {
				hosts[h] = true
				wantedHost[sourcePath{h, path}] = true
			}
		}
	}
	out := make([]match, len(entries))
	byID := map[int64]string{}
	slugs := map[string]bool{}
	if len(ids) > 0 {
		// A Mihon re-export can drop field 9100. Resolve generated identities
		// in the same query as mapped IDs; only a unique visible slug can win.
		rows, err := db.QueryContext(ctx, `WITH wanted(id) AS MATERIALIZED (SELECT value FROM json_each(?)),
   stubs(numeric_id,slug) AS (
     SELECT CASE WHEN s.hosted=1 THEN CAST(`+hostedBackupID+` AS TEXT) ELSE backup_stub_id(s.slug) END,s.slug
     FROM sources s WHERE ? AND (s.hosted=0 OR ?)),
   aliases(numeric_id,slug) AS (
     SELECT ltrim(m.tachiyomi_id,'0'),s.slug FROM source_tachiyomi_ids m JOIN sources s ON s.id=m.source_id
     WHERE (s.hosted=0 OR ?) AND ltrim(m.tachiyomi_id,'0') IN wanted
     UNION ALL
     SELECT numeric_id,slug FROM stubs WHERE numeric_id IN wanted)
   SELECT numeric_id,min(slug) FROM aliases GROUP BY numeric_id HAVING min(slug)=max(slug)`,
			jsonKeys(ids), hasStubs, hosted, hosted)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var slug string
			if err = rows.Scan(&id, &slug); err != nil {
				rows.Close()
				return nil, err
			}
			byID[id] = slug
			slugs[slug] = true
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return nil, err
		}
	}
	// Break ties as the official catalog does: first slug, then oldest source-title row.
	type hit struct {
		match
		rowID int64
	}
	prefer := func(old hit, m match, id int64) bool {
		return old.title == 0 || m.slug < old.slug || m.slug == old.slug && id < old.rowID
	}
	byHost := map[sourcePath]hit{}
	bySlug := map[sourcePath]hit{}
	wantedSlug := map[sourcePath]bool{}
	for i := range entries {
		e := &entries[i]
		if slug := byID[e.Source]; slug != "" {
			wantedSlug[sourcePath{slug, usersync.ChapterPathKey(e.URL)}] = true
		}
	}
	if len(hosts)+len(slugs) > 0 {
		rows, err := db.QueryContext(ctx, `WITH candidates AS MATERIALIZED (
   SELECT id,slug,backup_host(CASE WHEN hosted=1 THEN ? ELSE website END) AS host FROM sources
   WHERE (hosted=0 OR ?) AND (host IN (SELECT value FROM json_each(?)) OR slug IN (SELECT value FROM json_each(?))))
   SELECT c.host,c.slug,backup_path(st.url),st.title_id,st.id FROM candidates c JOIN source_titles st ON st.source_id=c.id
   WHERE backup_path(st.url) IN (SELECT value FROM json_each(?))`, publicURL, hosted, jsonKeys(hosts), jsonKeys(slugs), jsonKeys(paths))
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var host, path string
			var rowID int64
			var m match
			if err = rows.Scan(&host, &m.slug, &path, &m.title, &rowID); err != nil {
				rows.Close()
				return nil, err
			}
			h, s := sourcePath{host, path}, sourcePath{m.slug, path}
			if wantedHost[h] && prefer(byHost[h], m, rowID) {
				byHost[h] = hit{m, rowID}
			}
			if wantedSlug[s] && prefer(bySlug[s], m, rowID) {
				bySlug[s] = hit{m, rowID}
			}
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return nil, err
		}
	}
	names := map[string]bool{}
	for i := range entries {
		e := &entries[i]
		p := usersync.ChapterPathKey(e.URL)
		if e.Extra != nil {
			out[i] = byHost[sourcePath{websiteHost(e.Extra.Website), p}].match
		}
		if out[i].title == 0 {
			out[i] = bySlug[sourcePath{byID[e.Source], p}].match
		}
		if out[i].title == 0 {
			if n := titleName(e.Title); n != "" {
				names[n] = true
			}
		}
	}
	if len(names) == 0 {
		return out, nil
	}
	rows, err := db.QueryContext(ctx, `WITH wanted AS MATERIALIZED (SELECT value FROM json_each(?)),
 hits AS (SELECT backup_name(t.name) AS n,t.id FROM titles t WHERE n IN wanted AND `+catalog.VisibleTitle+`
 UNION ALL SELECT backup_name(a.name) AS n,t.id FROM title_alt_names a JOIN titles t ON t.id=a.title_id
 WHERE n IN wanted AND `+catalog.VisibleTitle+`) SELECT n,id FROM hits`, jsonKeys(names), hosted, hosted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	unique := map[string]int64{}
	for rows.Next() {
		var n string
		var id int64
		if err := rows.Scan(&n, &id); err != nil {
			return nil, err
		}
		if previous, seen := unique[n]; !seen || previous == id {
			unique[n] = id
		} else {
			unique[n] = 0
		}
	}
	for i := range entries {
		if out[i].title == 0 {
			out[i].title = unique[titleName(entries[i].Title)]
		}
	}
	return out, rows.Err()
}

// Materialized comparison keys must stay in memory even for a large file.
// Limit temp_store to this reserved connection and release the read snapshot
// before beginning any sync write or network response.
func matchSnapshot(ctx context.Context, db *sql.DB, entries []Entry, hosted bool, publicURL string) ([]match, error) {
	if len(entries) == 0 {
		return nil, ctx.Err()
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	var original int
	if err := conn.QueryRowContext(ctx, "PRAGMA temp_store").Scan(&original); err != nil {
		return nil, err
	}
	if original != 2 {
		if _, err := conn.ExecContext(ctx, "PRAGMA temp_store=MEMORY"); err != nil {
			return nil, err
		}
		defer func() {
			if _, err := conn.ExecContext(context.WithoutCancel(ctx), "PRAGMA temp_store="+strconv.Itoa(original)); err != nil {
				// A connection that could not be reset must not return to the pool.
				_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			}
		}()
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return matchEntries(ctx, tx, entries, hosted, publicURL)
}
