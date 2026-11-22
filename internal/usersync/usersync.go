// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package usersync is the sync contract (contract/sync/sync.schema.json, unchanged in contract
// v1) and the library it fills: push, pull, the conflict rules, tombstones, and the
// caller's own state on titles. It gives the same answers as the official server for the same
// pushes; the contract's fixtures (contract/sync/rules, invalid, push) are its tests.
//
// It is the only writer of sync_accounts and sync_records. Every write runs in one SQLite write
// transaction (BEGIN IMMEDIATE: one writer for the whole database, across processes too, so the
// admin commands are serialised with the server), which is what keeps an account's sequence
// numbers in commit order: a pull at cursor n never later misses a record with seq <= n.
package usersync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pb33f/jsonschema/v6"

	kit "github.com/IGlace/abyssreader-server-kit"
)

// Kind is a sync record kind (the schema's kind).
type Kind string

const (
	Favorite    Kind = "favorite"
	Planning    Kind = "planning"
	Blacklist   Kind = "blacklist"
	Rating      Kind = "rating"
	Position    Kind = "position"
	ChapterMark Kind = "chapterMark"
	Inbox       Kind = "inbox"
)

var kinds = []Kind{Favorite, Planning, Blacklist, Rating, Position, ChapterMark, Inbox}

// IsList reports whether k is a library list (presence is the value; data is always null).
func (k Kind) IsList() bool { return k == Favorite || k == Planning || k == Blacklist }

// defaultCaps are the per-account record caps of the sync contract (spec §5): checked only when
// a push would create a record; an update of an existing one is never capped.
var defaultCaps = map[Kind]int{
	Favorite: 20000, Planning: 20000, Blacklist: 20000, Rating: 20000,
	Position: 20000, ChapterMark: 100000, Inbox: 20000,
}

// dataFields are each kind's data fields in wire order (the library lists have none).
var dataFields = map[Kind][]string{
	Rating:      {"opinion"},
	Position:    {"sourceSlug", "chapterUrl", "chapterNumber", "chapterName", "pageIndex"},
	ChapterMark: {"chapterNumber", "chapterName", "read", "lastPageRead", "totalPages", "bookmarked"},
	Inbox:       {"sourceSlug", "latestChapterNumber", "latestChapterName", "latestChapterUrl", "detectedAt", "seen"},
}

const (
	// MaxBatch is the contract's largest push (pushEnvelope maxItems).
	MaxBatch = 500
	// MaxPull is the largest pull page (the contract's limit maximum); DefaultPull is the default.
	MaxPull     = 1000
	DefaultPull = 500
	// clockTolerance: a clientUpdatedAt later than now + this is stored as now (spec §5).
	clockTolerance = 5 * time.Minute
	// TombstoneLifetime is how long a removal is kept (spec Q9). A device whose cursor is older
	// than a purged tombstone pulls a full snapshot again (409).
	TombstoneLifetime = 90 * 24 * time.Hour
	receiptLifetime   = TombstoneLifetime + 7*24*time.Hour

	timestampLayout = "2006-01-02T15:04:05.000Z"
)

var (
	// ErrNoAccount means the account is gone (deleted while its request ran).
	ErrNoAccount = errors.New("no such account")
	// ErrBadCursor means a pull cursor this server never gave (400 validation_failed).
	ErrBadCursor = errors.New("bad cursor")
	// ErrFullResync means the cursor can no longer be served: the app pulls again without one
	// (409 {fullResyncRequired: true}).
	ErrFullResync = errors.New("full resync required")
)

// Service reads and writes an account's synced records.
type Service struct {
	db           *sql.DB
	writer       chan struct{} // one SQLite write transaction for this service's pushes and restore batches
	now          func() time.Time
	caps         map[Kind]int
	maxStored    int // the account's ceiling on stored rows, removals included
	receiptLimit int
	purgeBatch   int           // tombstones deleted per purge transaction
	purged       func(n int64) // test hook: after each purge batch
}

// maxStoredRows is the ceiling on every row an account stores: its live records and its
// removals. A removal of a key the server never had is stored too (the contract's rule
// delete-unknown-key-tombstones), so without a ceiling one account could grow the table without
// end. The live caps add up to 220 000 (the official server's caps; a very heavy reader nears
// the 100 000 chapter marks); the 30 000 above that leave room for 90 days of removals, which a
// real reader never comes close to. A push that would add a row past it is `rejected: cap`;
// what replaces a stored row (an update, a removal of a stored record, a re-add) always passes.
const maxStoredRows = 250_000

// purgeBatchSize keeps each purge transaction short: SQLite has one writer, and pushes wait
// for it at most the busy timeout.
const purgeBatchSize = 500

// New returns the service on db.
func New(db *sql.DB) *Service {
	caps := make(map[Kind]int, len(defaultCaps))
	for k, n := range defaultCaps {
		caps[k] = n
	}
	return &Service{db: db, writer: make(chan struct{}, 1), now: time.Now, caps: caps, maxStored: maxStoredRows, receiptLimit: maxAppliedReceipts, purgeBatch: purgeBatchSize}
}

// WithClock sets the server's clock (tests).
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

func ms(t time.Time) int64     { return t.UnixMilli() }
func format(msec int64) string { return time.UnixMilli(msec).UTC().Format(timestampLayout) }
func parseTime(s string) (int64, bool) {
	t, err := time.Parse(timestampLayout, s)
	if err != nil || t.Before(time.Unix(0, 0)) { // the contract's earliest timestamp is the epoch
		return 0, false
	}
	return t.UnixMilli(), true
}

// ── The schema ──

var (
	schemaOnce sync.Once
	pushItem   *jsonschema.Schema
	changeItem *jsonschema.Schema
	schemaErr  error
)

// itemSchema is the sync schema's pushItem, compiled from the embedded contract file.
func itemSchema() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(kit.SyncSchemaJSON()))
		if err != nil {
			schemaErr = err
			return
		}
		c := jsonschema.NewCompiler()
		const url = "https://abyssreader.com/schemas/sync.schema.json"
		if err := c.AddResource(url, doc); err != nil {
			schemaErr = err
			return
		}
		pushItem, schemaErr = c.Compile(url + "#/$defs/pushItem")
		if schemaErr == nil {
			changeItem, schemaErr = c.Compile(url + "#/$defs/change")
		}
	})
	return pushItem, schemaErr
}

// ── Records ──

// Key is a record's key as the wire carries it. SourceSlug and ChapterURL are set for a chapter
// mark only, whose identity they are (its MainMangaID is the title it belongs to).
type Key struct {
	MainMangaID int64  `json:"mainMangaId"`
	SourceSlug  string `json:"sourceSlug,omitempty"`
	ChapterURL  string `json:"chapterUrl,omitempty"`
}

// Change is one record as a pull gives it (the schema's change).
type Change struct {
	Kind            Kind            `json:"kind"`
	Key             Key             `json:"key"`
	Data            json.RawMessage `json:"data"`
	ClientUpdatedAt string          `json:"clientUpdatedAt"`
	Deleted         bool            `json:"deleted"`
	// Restore-only metadata inferred by the backup decoder.
	RestoreNameIsFallback bool `json:"-"`
}

// record is a parsed push item or a stored record.
type record struct {
	kind    Kind
	key     Key
	data    []byte // normalised JSON; nil for the lists and for a removal
	at      int64  // clientUpdatedAt, unix ms
	deleted bool
}

// identity is the stored key: (key_title, source_slug, url_hash), and the chapter url.
func (r record) identity() (int64, string, []byte, string) {
	if r.kind == ChapterMark {
		h := sha256.Sum256([]byte(r.key.ChapterURL))
		return 0, r.key.SourceSlug, h[:], r.key.ChapterURL
	}
	return r.key.MainMangaID, "", []byte{}, ""
}

// slugs are the source slugs the record names (a mark's key, a position's or inbox's data).
func (r record) slugs() []string {
	var out []string
	if r.key.SourceSlug != "" {
		out = append(out, r.key.SourceSlug)
	}
	if r.data != nil && (r.kind == Position || r.kind == Inbox) {
		var d struct {
			SourceSlug *string `json:"sourceSlug"`
		}
		if json.Unmarshal(r.data, &d) == nil && d.SourceSlug != nil {
			out = append(out, *d.SourceSlug)
		}
	}
	return out
}

// parseItem checks one push item: the schema's pushItem, then what the schema cannot say (a
// real date not before the epoch, finite numbers). nil means `rejected: invalid`.
func parseItem(item json.RawMessage) *record {
	schema, err := itemSchema()
	if err != nil {
		return nil
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(item))
	if err != nil || schema.Validate(v) != nil {
		return nil
	}
	var in struct {
		Kind            Kind                       `json:"kind"`
		Key             Key                        `json:"key"`
		Data            map[string]json.RawMessage `json:"data"`
		ClientUpdatedAt string                     `json:"clientUpdatedAt"`
		Deleted         bool                       `json:"deleted"`
	}
	if err := json.Unmarshal(item, &in); err != nil {
		return nil
	}
	r := &record{kind: in.Kind, key: in.Key, deleted: in.Deleted}
	var ok bool
	if r.at, ok = parseTime(in.ClientUpdatedAt); !ok {
		return nil
	}
	if r.deleted || r.kind.IsList() {
		return r
	}
	if r.data, ok = normalise(r.kind, in.Data); !ok {
		return nil
	}
	return r
}

// normalise writes a record's data with every field of its kind in wire order, null when
// absent; numbers must be finite and detectedAt a real time.
func normalise(k Kind, data map[string]json.RawMessage) ([]byte, bool) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range dataFields[k] {
		if i > 0 {
			b.WriteByte(',')
		}
		name, _ := json.Marshal(f)
		b.Write(name)
		b.WriteByte(':')
		v, present := data[f]
		if !present || string(v) == "null" {
			b.WriteString("null")
			continue
		}
		switch f {
		case "chapterNumber", "latestChapterNumber", "pageIndex", "lastPageRead", "totalPages":
			n, err := strconv.ParseFloat(string(v), 64)
			if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
				return nil, false
			}
			out, _ := json.Marshal(n)
			b.Write(out)
		case "detectedAt":
			var s string
			if json.Unmarshal(v, &s) != nil {
				return nil, false
			}
			if _, ok := parseTime(s); !ok {
				return nil, false
			}
			b.Write(v)
		default:
			b.Write(v)
		}
	}
	b.WriteByte('}')
	return b.Bytes(), true
}

// number reads a numeric data field; absent or null is -Inf (the position tie rule).
func number(data []byte, field string) float64 {
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return math.Inf(-1)
	}
	if n, ok := m[field].(float64); ok {
		return n
	}
	return math.Inf(-1)
}

// ── Push ──

// Result is one push item's answer (the schema's pushResult).
type Result struct {
	OpID   string `json:"opId"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// PushOutcome is the push response: one result per item and the cursor after the push.
type PushOutcome struct {
	Results []Result `json:"results"`
	Cursor  string   `json:"cursor"`
}

func opIDOf(item json.RawMessage) string {
	var in struct {
		OpID any `json:"opId"`
	}
	json.Unmarshal(item, &in)
	if s, ok := in.OpID.(string); ok && s != "" {
		if r := []rune(s); len(r) > 100 {
			return string(r[:100])
		}
		return s
	}
	return "?"
}

// Push applies the items in order, in one write transaction, each one alone (one bad item
// never fails the batch): schema → `rejected: invalid`; a title that does not exist →
// `unknown_title`; a source slug that does not exist → `unknown_source`; then the conflict
// rules (spec §5): the newer clientUpdatedAt wins, a tie keeps the stored record except a
// position with a higher chapter, then page; a removal leaves a tombstone. The caller checked
// the envelope (1..MaxBatch objects).
func (s *Service) Push(ctx context.Context, userID int64, items []json.RawMessage) (PushOutcome, error) {
	parsed := make([]*record, len(items))
	opIDs := make([]string, len(items))
	titleSet, slugSet := map[int64]bool{}, map[string]bool{}
	for i, it := range items {
		opIDs[i] = opIDOf(it)
		if parsed[i] = parseItem(it); parsed[i] != nil {
			titleSet[parsed[i].key.MainMangaID] = true
			for _, sl := range parsed[i].slugs() {
				slugSet[sl] = true
			}
		}
	}
	now := ms(s.now())
	var out PushOutcome
	err := s.write(ctx, userID, func(tx *sql.Tx, seq *int64) error {
		titles, err := existing(ctx, tx, `SELECT id FROM titles WHERE id IN `, keys(titleSet))
		if err != nil {
			return err
		}
		sources, err := existing(ctx, tx, `SELECT slug FROM sources WHERE slug IN `, keys(slugSet))
		if err != nil {
			return err
		}
		receipts, err := appliedReceipts(ctx, tx, userID, opIDs)
		if err != nil {
			return err
		}
		counts := map[Kind]int{}
		var receiptSequence int64
		out.Results = make([]Result, len(items))
		for i, r := range parsed {
			opID := opIDs[i]
			if receipt, ok := receipts[opID]; ok {
				out.Results[i] = receipt
				continue
			}
			res := Result{OpID: opID}
			switch {
			case r == nil:
				res.Status, res.Reason = "rejected", "invalid"
			case !titles[r.key.MainMangaID]:
				res.Status, res.Reason = "rejected", "unknown_title"
			case !allIn(r.slugs(), sources):
				res.Status, res.Reason = "rejected", "unknown_source"
			default:
				r.clamp(now)
				if res.Status, res.Reason, err = s.apply(ctx, tx, userID, *r, seq, now, counts); err != nil {
					return err
				}
			}
			if res.Status == "applied" {
				if receiptSequence == 0 {
					if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(receipt_seq), 0)
						FROM sync_applied_operations WHERE user_id = ?`, userID).Scan(&receiptSequence); err != nil {
						return err
					}
				}
				receiptSequence++
				if _, err := tx.ExecContext(ctx, `INSERT INTO sync_applied_operations
					(user_id, op_id, status, reason, applied_at, receipt_seq) VALUES (?, ?, ?, ?, ?, ?)`,
					userID, res.OpID, res.Status, res.Reason, now, receiptSequence); err != nil {
					return err
				}
				receipts[opID] = res
			}
			out.Results[i] = res
		}
		if cutoff := receiptSequence - int64(s.receiptLimit); cutoff > 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM sync_applied_operations
				WHERE user_id = ? AND receipt_seq <= ?`, userID, cutoff); err != nil {
				return err
			}
		}
		out.Cursor = cursor{seq: *seq}.String()
		return nil
	})
	if err != nil {
		return PushOutcome{}, err
	}
	return out, nil
}

// write runs f in one write transaction for the account, with its sequence number: f takes
// numbers with *seq++, and the last one is stored when f succeeds. The cursor after the write
// is the result's Cursor.
func (s *Service) write(ctx context.Context, userID int64, f func(tx *sql.Tx, seq *int64) error) (err error) {
	// Queue before BEGIN IMMEDIATE so a restore's next batch cannot repeatedly
	// take the SQLite lock while a waiting push backs off in the busy handler.
	select {
	case s.writer <- struct{}{}:
		defer func() { <-s.writer }()
	case <-ctx.Done():
		return ctx.Err()
	}
	tx, err := s.db.BeginTx(ctx, nil) // BEGIN IMMEDIATE (store.Open sets _txlock)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()
	seq, err := lockAccount(ctx, tx, userID)
	if err != nil {
		return err
	}
	start := seq
	if err = f(tx, &seq); err != nil {
		return err
	}
	if seq != start {
		if _, err = tx.ExecContext(ctx, `UPDATE sync_accounts SET seq = ? WHERE user_id = ?`, seq, userID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// lockAccount returns the account's sequence number inside a write transaction, making its
// sync_accounts row on its first write. ErrNoAccount when the account is gone.
func lockAccount(ctx context.Context, tx *sql.Tx, userID int64) (int64, error) {
	var seq int64
	err := tx.QueryRowContext(ctx, `SELECT seq FROM sync_accounts WHERE user_id = ?`, userID).Scan(&seq)
	if err == nil {
		return seq, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO sync_accounts (user_id) SELECT id FROM users WHERE id = ?`, userID)
	if err != nil {
		return 0, err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return 0, ErrNoAccount
	}
	return 0, nil
}

// stored is the record (live or tombstone) for r's key, if any.
func stored(ctx context.Context, tx *sql.Tx, userID int64, r record) (*record, error) {
	kt, slug, hash, _ := r.identity()
	var data sql.NullString
	var at, deleted int64
	err := tx.QueryRowContext(ctx, queryStored, userID, string(r.kind), kt, slug, hash).Scan(&data, &at, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cur := &record{kind: r.kind, key: r.key, at: at, deleted: deleted == 1}
	if data.Valid {
		cur.data = []byte(data.String)
	}
	return cur, nil
}

// apply runs the conflict rules for one checked record and writes it when it wins.
func (s *Service) apply(ctx context.Context, tx *sql.Tx, userID int64, r record, seq *int64, now int64, counts map[Kind]int) (string, string, error) {
	cur, err := stored(ctx, tx, userID, r)
	if err != nil {
		return "", "", err
	}
	status, reason, err := s.decide(ctx, tx, userID, r, cur, counts)
	if status == "applied" {
		*seq++
		err = put(ctx, tx, userID, []record{r}, *seq, now)
	}
	return status, reason, err
}

func (r *record) clamp(now int64) {
	if r.at > now+clockTolerance.Milliseconds() {
		r.at = now
	}
}

// decide applies the sync conflict rules and accounts for a winning write against
// both caps. cur includes preceding writes in this transaction.
func (s *Service) decide(ctx context.Context, tx *sql.Tx, userID int64, r record, cur *record, counts map[Kind]int) (string, string, error) {
	if cur != nil {
		wins := r.at > cur.at
		if r.at == cur.at {
			wins = r.kind == Position && !r.deleted && !cur.deleted && positionTieWins(*cur, r)
		}
		if !wins {
			return "stale", "", nil
		}
	}
	// A new key adds a row, a removal of an unknown key too: the ceiling on stored rows.
	grows := cur == nil
	if grows {
		total, ok := counts[allKinds]
		if !ok {
			if err := tx.QueryRowContext(ctx, queryStoredCount, userID).Scan(&total); err != nil {
				return "", "", err
			}
			counts[allKinds] = total
		}
		if total >= s.maxStored {
			return "rejected", "cap", nil
		}
	}
	if createsRecord := !r.deleted && (cur == nil || cur.deleted); createsRecord {
		n, ok := counts[r.kind]
		if !ok {
			if err := tx.QueryRowContext(ctx, queryLiveCount, userID, string(r.kind)).Scan(&n); err != nil {
				return "", "", err
			}
		}
		if n >= s.caps[r.kind] {
			counts[r.kind] = n
			return "rejected", "cap", nil
		}
		counts[r.kind] = n + 1
	} else if r.deleted && cur != nil && !cur.deleted {
		if n, ok := counts[r.kind]; ok {
			counts[r.kind] = n - 1
		}
	}
	if grows {
		counts[allKinds]++
	}
	return "applied", "", nil
}

// put writes the records, in order, with consecutive sequence numbers.
func put(ctx context.Context, tx *sql.Tx, userID int64, records []record, firstSeq, now int64) error {
	args := make([]any, 0, 12*len(records))
	for i, r := range records {
		kt, slug, hash, url := r.identity()
		var data any
		var deletedAt any
		if r.deleted {
			deletedAt = now
		} else if r.data != nil {
			data = string(r.data)
		}
		args = append(args, userID, string(r.kind), kt, slug, hash, url, r.key.MainMangaID, data, r.at, r.deleted, deletedAt, firstSeq+int64(i))
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO sync_records
		(user_id, kind, key_title, source_slug, url_hash, chapter_url, title_id, data, client_updated_at, deleted, deleted_at, seq)
		VALUES `+rowPlaceholders(12, len(records))+`
		ON CONFLICT (user_id, kind, key_title, source_slug, url_hash) DO UPDATE SET
		title_id = excluded.title_id, data = excluded.data, client_updated_at = excluded.client_updated_at,
		deleted = excluded.deleted, deleted_at = excluded.deleted_at, seq = excluded.seq`, args...)
	return err
}

func rowPlaceholders(columns, count int) string {
	return strings.TrimSuffix(strings.Repeat(placeholders(columns)+",", count), ",")
}

func positionTieWins(cur, in record) bool {
	if a, b := number(in.data, "chapterNumber"), number(cur.data, "chapterNumber"); a != b {
		return a > b
	}
	return number(in.data, "pageIndex") > number(cur.data, "pageIndex")
}

// existing returns which of the values exist, by a query `... IN (?, ...)` in chunks.
func existing[T comparable](ctx context.Context, tx *sql.Tx, query string, values []T) (map[T]bool, error) {
	out := make(map[T]bool, len(values))
	const chunk = 500
	for len(values) > 0 {
		n := min(chunk, len(values))
		args := make([]any, n)
		for i, v := range values[:n] {
			args[i] = v
		}
		rows, err := tx.QueryContext(ctx, query+placeholders(n), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v T
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return nil, err
			}
			out[v] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		values = values[n:]
	}
	return out, nil
}

func placeholders(n int) string { return "(" + strings.TrimSuffix(strings.Repeat("?,", n), ",") + ")" }

func keys[T comparable](m map[T]bool) []T {
	out := make([]T, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func allIn(slugs []string, set map[string]bool) bool {
	for _, s := range slugs {
		if !set[s] {
			return false
		}
	}
	return true
}

// ── Pull ──

// Page is one pull page (the schema's pullResponse).
type Page struct {
	Changes []Change `json:"changes"`
	Cursor  string   `json:"cursor"`
	HasMore bool     `json:"hasMore"`
}

// cursor is a parsed pull cursor: "d.<seq>" (a delta after seq) or "s.<startSeq>.<afterId>" (a
// snapshot page after record id afterId, which ends with the delta cursor d.<startSeq>). The
// app never parses them.
type cursor struct {
	snapshot bool
	seq      int64 // delta: after this seq; snapshot: the account's seq when it started
	afterID  int64
}

func (c cursor) String() string {
	if c.snapshot {
		return "s." + strconv.FormatInt(c.seq, 10) + "." + strconv.FormatInt(c.afterID, 10)
	}
	return "d." + strconv.FormatInt(c.seq, 10)
}

// count reads a cursor's number: digits only, no leading zero, within int64.
func count(s string) (int64, bool) {
	if s == "" || len(s) > 18 || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

func parseCursor(s string) (cursor, bool) {
	p := strings.Split(s, ".")
	switch {
	case len(p) == 2 && p[0] == "d":
		n, ok := count(p[1])
		return cursor{seq: n}, ok
	case len(p) == 3 && p[0] == "s":
		start, ok1 := count(p[1])
		after, ok2 := count(p[2])
		return cursor{snapshot: true, seq: start, afterID: after}, ok1 && ok2
	}
	return cursor{}, false
}

// Pull reads one page of changes after the cursor; no cursor is the first page of a full
// snapshot (every live record, no tombstones), which ends with a delta cursor at the account's
// sequence number read when the snapshot started, so whatever was written meanwhile comes in
// the next delta. A delta gives records and tombstones with seq > the cursor's, in seq order.
// At most limit changes (1..MaxPull, checked by the caller) are read: memory stays bounded.
func (s *Service) Pull(ctx context.Context, userID int64, raw *string, limit int) (Page, error) {
	c := cursor{snapshot: true}
	first := raw == nil
	if !first {
		var ok bool
		if c, ok = parseCursor(*raw); !ok {
			return Page{}, ErrBadCursor
		}
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true}) // a plain BEGIN: a read snapshot, no write lock
	if err != nil {
		return Page{}, err
	}
	defer tx.Rollback()
	var seq, floor int64
	err = tx.QueryRowContext(ctx, `SELECT seq, floor_seq FROM sync_accounts WHERE user_id = ?`, userID).Scan(&seq, &floor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Page{}, err
	}
	if first {
		c.seq = seq
	}
	// A cursor ahead of the account (the server was restored from an older backup) or, for a
	// delta, below the floor (a removal it never saw was purged) can no longer be served.
	if c.seq > seq || (!c.snapshot && c.seq < floor) {
		return Page{}, ErrFullResync
	}
	var rows *sql.Rows
	if c.snapshot {
		rows, err = tx.QueryContext(ctx, querySnapshot, userID, c.afterID, limit+1)
	} else {
		rows, err = tx.QueryContext(ctx, queryDelta, userID, c.seq, limit+1)
	}
	if err != nil {
		return Page{}, err
	}
	defer rows.Close()
	p := Page{Changes: make([]Change, 0, min(limit, 64))}
	var lastID, lastSeq int64
	for rows.Next() {
		if len(p.Changes) == limit {
			p.HasMore = true
			break
		}
		var ch Change
		var id, rseq, deleted, at int64
		var data sql.NullString
		if err := rows.Scan(&id, &rseq, &ch.Kind, &ch.Key.MainMangaID, &ch.Key.SourceSlug, &ch.Key.ChapterURL, &data, &at, &deleted); err != nil {
			return Page{}, err
		}
		ch.ClientUpdatedAt, ch.Deleted, ch.Data = format(at), deleted == 1, json.RawMessage("null")
		if data.Valid && !ch.Deleted {
			ch.Data = json.RawMessage(data.String)
		}
		p.Changes = append(p.Changes, ch)
		lastID, lastSeq = id, rseq
	}
	if err := rows.Err(); err != nil {
		return Page{}, err
	}
	switch {
	case c.snapshot && p.HasMore:
		p.Cursor = cursor{snapshot: true, seq: c.seq, afterID: lastID}.String()
	case c.snapshot:
		p.Cursor = cursor{seq: c.seq}.String()
	case len(p.Changes) > 0:
		p.Cursor = cursor{seq: lastSeq}.String()
	default:
		p.Cursor = c.String()
	}
	return p, nil
}

// ── The caller's state on titles ──

// State is the caller's own state on one title (the contract's MyTitleState).
type State struct {
	Favorite, Planning, Blacklist bool
	Vote                          *string // "up", "down" or nil
}

// States gives the account's state on each of the titles (every id has one; none is all
// false with no vote).
func (s *Service) States(ctx context.Context, userID int64, titleIDs []int64) (map[int64]State, error) {
	out := make(map[int64]State, len(titleIDs))
	for _, id := range titleIDs {
		out[id] = State{}
	}
	for len(titleIDs) > 0 {
		n := min(500, len(titleIDs))
		args := []any{userID}
		for _, id := range titleIDs[:n] {
			args = append(args, id)
		}
		rows, err := s.db.QueryContext(ctx, queryStates(n), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id int64
			var kind Kind
			var data sql.NullString
			if err := rows.Scan(&id, &kind, &data); err != nil {
				rows.Close()
				return nil, err
			}
			st := out[id]
			switch kind {
			case Favorite:
				st.Favorite = true
			case Planning:
				st.Planning = true
			case Blacklist:
				st.Blacklist = true
			case Rating:
				var d struct {
					Opinion string `json:"opinion"`
				}
				if json.Unmarshal([]byte(data.String), &d) == nil && (d.Opinion == "up" || d.Opinion == "down") {
					st.Vote = &d.Opinion
				}
			}
			out[id] = st
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		titleIDs = titleIDs[n:]
	}
	return out, nil
}

// ── Server-made writes ──

// RemoveTitle tombstones, inside tx (the title's removal), every live record of every account
// on the title, so each device drops them on its next pull. A tombstone is stamped with the
// server's time, and always later than the record it replaces (a device holding that record
// keeps it on a tie). Run it before the title row goes. It returns how many records it
// tombstoned.
func RemoveTitle(ctx context.Context, tx *sql.Tx, titleID int64, now time.Time) (int, error) {
	rows, err := tx.QueryContext(ctx, queryRemoveTitle, titleID)
	if err != nil {
		return 0, err
	}
	type live struct{ id, user, at int64 }
	var found []live
	for rows.Next() {
		var l live
		if err := rows.Scan(&l.id, &l.user, &l.at); err != nil {
			rows.Close()
			return 0, err
		}
		found = append(found, l)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	at := ms(now)
	for i := 0; i < len(found); {
		user := found[i].user
		seq, err := lockAccount(ctx, tx, user)
		if err != nil {
			return 0, err
		}
		for ; i < len(found) && found[i].user == user; i++ {
			seq++
			if _, err := tx.ExecContext(ctx, `UPDATE sync_records SET data = NULL, deleted = 1, deleted_at = ?,
				client_updated_at = ?, seq = ? WHERE id = ?`, at, max(at, found[i].at+1), seq, found[i].id); err != nil {
				return 0, err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sync_accounts SET seq = ? WHERE user_id = ?`, seq, user); err != nil {
			return 0, err
		}
	}
	return len(found), nil
}

// PurgeTombstones deletes the tombstones made before `before` (TombstoneLifetime ago, in the
// server's loop) and raises each account's floor past the ones it deletes. It works in batches
// of purgeBatch, each its own short write transaction, so a large purge never holds SQLite's one
// writer for long. Applied receipts use this schedule with a longer lifetime. It returns the
// number of tombstones deleted.
func (s *Service) PurgeTombstones(ctx context.Context, before time.Time) (int64, error) {
	var total int64
	for {
		n, err := s.purgeOnce(ctx, ms(before))
		total += n
		if err != nil {
			return total, err
		}
		if n < int64(s.purgeBatch) {
			return total, s.purgeReceipts(ctx, ms(before.Add(TombstoneLifetime-receiptLifetime)))
		}
	}
}

func (s *Service) purgeOnce(ctx context.Context, cutoff int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, queryPurgeBatch, cutoff, s.purgeBatch)
	if err != nil {
		return 0, err
	}
	var ids []any
	floors := map[int64]int64{}
	for rows.Next() {
		var id, user, seq int64
		if err := rows.Scan(&id, &user, &seq); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
		floors[user] = max(floors[user], seq)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(ids) == 0 {
		return 0, err
	}
	for user, floor := range floors {
		if _, err := tx.ExecContext(ctx, `UPDATE sync_accounts SET floor_seq = max(floor_seq, ?) WHERE user_id = ?`, floor, user); err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sync_records WHERE id IN `+placeholders(len(ids)), ids...); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	if s.purged != nil {
		s.purged(int64(len(ids)))
	}
	return int64(len(ids)), nil
}
