// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/backup"
	"github.com/IGlace/abyssreader-server-kit/internal/config"
)

func exportAccount(t *testing.T, h *harness, token string) []byte {
	t.Helper()
	r, err := http.NewRequest("GET", h.srv.URL+backupPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	p, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	h.contract.Check(t, r, resp, p)
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("export %d %v", resp.StatusCode, resp.Header)
	}
	return p
}
func counts(t *testing.T, a answer, matched, byName, miss, restored, skipped int) {
	t.Helper()
	var r backup.Report
	if err := json.Unmarshal(a.raw, &r); err != nil {
		t.Fatal(err)
	}
	got := []int{r.Titles.Matched, r.Titles.MatchedByName, r.Titles.Unmatched, r.ChapterMarks.Restored, r.ChapterMarks.Skipped}
	if !reflect.DeepEqual(got, []int{matched, byName, miss, restored, skipped}) {
		t.Fatalf("counts: %v; report %s", got, a.raw)
	}
}

func TestAccountBackupRoundTripAndLaterRemoval(t *testing.T) {
	h := newHarness(t, backupOn)
	h.smallCatalog()
	a, _ := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	b, _ := h.signUp("Other", "other@example.com", "correct-horse-7")
	ts := "2026-10-10T10:00:00.000Z"
	items := allKinds(2, ts)
	// The file format carries one page and chapter description on the chapter
	// referenced by the history row. Total page counts and the inbox are excluded.
	mark := items[5]["data"].(obj)
	mark["totalPages"] = nil
	pos := items[4]["data"].(obj)
	pos["pageIndex"], pos["chapterNumber"], pos["chapterName"] = mark["lastPageRead"], mark["chapterNumber"], mark["chapterName"]
	h.push(a, items...)
	h.push(b, item("planning", titleKey(1), nil, ts, false))
	file := exportAccount(t, h, a)
	restore := func() answer { return h.do("POST", restorePath, upload(t, b, "different.extension", file)) }
	result := restore()
	h.expect(result, 200, "")
	counts(t, result, 1, 0, 0, 1, 0)
	changes, cursor := h.pull(b, "", 0)
	if len(changes) != 7 {
		t.Fatalf("target: %v", changes)
	}
	for _, it := range items[:6] {
		found := false
		for _, ch := range changes {
			if reflect.DeepEqual(asChange(it), ch) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing %v from %v", asChange(it), changes)
		}
	}
	result = restore()
	counts(t, result, 1, 0, 0, 0, 1)
	if delta, next := h.pull(b, cursor, 0); len(delta) != 0 || cursor != next {
		t.Fatalf("replay moved cursor %s -> %s", cursor, next)
	}
	del := h.push(b, item("favorite", titleKey(2), nil, "2026-11-01T00:00:00.000Z", true))
	restore()
	if delta, _ := h.pull(b, del.body["cursor"].(string), 0); len(delta) != 0 {
		t.Fatalf("older file revived removal: %v", delta)
	}
	if own, c := h.pull(a, "", 0); len(own) != 7 || c != "d.7" {
		t.Fatal("changed source account")
	}
}

// Construct just the fields emitted by each upstream app. Both omit field 100
// (favorite defaults to true); only Mihon writes the modification timestamp.
func foreignFixture(t *testing.T, old bool) []byte {
	t.Helper()
	var raw bytes.Buffer
	for _, e := range []backup.Entry{
		{Source: 77, URL: "https://old.test/series/tidewater", Title: "ignored"},
		{Source: 88, URL: "/no", Title: "El guardián del farol"},
		{Source: 88, URL: "/none", Title: "Missing\n\u202eName"},
	} {
		var fields []byte
		field := func(n int, b []byte) {
			fields = binary.AppendUvarint(fields, uint64(n<<3|2))
			fields = binary.AppendUvarint(fields, uint64(len(b)))
			fields = append(fields, b...)
		}
		fields = append(fields, 8, byte(e.Source))
		field(2, []byte(e.URL))
		field(3, []byte(e.Title))
		field(16, []byte{10, 2, '/', 'c', 32, 1})
		if !old {
			fields = binary.AppendUvarint(fields, 106<<3)
			fields = binary.AppendUvarint(fields, 1700000000)
		}
		raw.WriteByte(10)
		raw.Write(binary.AppendUvarint(nil, uint64(len(fields))))
		raw.Write(fields)
	}
	if err := backup.WriteSource(&raw, 88, "Old\u2066 Source"); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	z := gzip.NewWriter(&buf)
	if _, err := z.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
func TestRestoreForeignFilesAndUnmatchedEcho(t *testing.T) {
	for _, name := range []string{"mihon.tachibk", "abyssreader.proto.gz"} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, backupOn)
			h.smallCatalog()
			if _, err := h.db.Exec("INSERT INTO source_tachiyomi_ids SELECT '77',id FROM sources WHERE slug='paper-moon'"); err != nil {
				t.Fatal(err)
			}
			tok, _ := h.signUp("Reader", "reader@example.com", "correct-horse-7")
			restore := func() answer {
				return h.do("POST", restorePath, upload(t, tok, name, foreignFixture(t, strings.HasSuffix(name, "gz"))))
			}
			a := restore()
			h.expect(a, 200, "")
			counts(t, a, 1, 1, 1, 1, 2)
			if !bytes.Contains(a.raw, []byte(`"title":"Missing Name","sourceName":"Old Source"`)) {
				t.Fatalf("unsafe or missing echo %s", a.raw)
			}
			snapshot, c := h.pull(tok, "", 0)
			if len(snapshot) != 3 {
				t.Fatalf("unexpected rows %v", snapshot)
			}
			counts(t, restore(), 1, 1, 1, 0, 3)
			if delta, n := h.pull(tok, c, 0); len(delta) != 0 || n != c {
				t.Fatal("replaying changed sync")
			}
		})
	}
}
func TestAccountBackupBusyIs503(t *testing.T) {
	h := newHarness(t, backupOn)
	h.smallCatalog()
	tok, _ := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	h.db.SetMaxOpenConns(1)
	if _, err := h.db.Exec("PRAGMA busy_timeout=10"); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", "file:"+filepath.Join(h.cfg.DataDir, "abyssreader.db")+"?_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	a := h.do("POST", restorePath, upload(t, tok, "x", foreignFixture(t, true)))
	h.expect(a, 503, "unavailable")
	if a.header.Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
}
func TestRestoreStreamsWithoutTemporaryFilesAndEnforcesLimits(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { backupOn(c); c.AccountBackupMaxMiB = 1 })
	tok, _ := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "no-temp-directory")
	if err := os.WriteFile(sentinel, nil, 0600); err != nil {
		t.Fatal(err)
	}
	// Any attempt to spool even a transient upload to os.TempDir now fails.
	for _, key := range []string{"TMPDIR", "TEMP", "TMP"} {
		t.Setenv(key, sentinel)
	}
	// A large ignored protobuf field, with no manga entries.
	data := binary.AppendUvarint(nil, 45<<3|2)
	data = binary.AppendUvarint(data, 900000)
	data = append(data, make([]byte, 900000)...)
	a := h.do("POST", restorePath, upload(t, tok, "any", data))
	h.expect(a, 200, "")
	counts(t, a, 0, 0, 0, 0, 0)
	h.expect(h.do("POST", restorePath, upload(t, tok, "any", make([]byte, (1<<20)+multipartAllowance))), 413, "too_large")
	for _, many := range []bool{true, false} {
		var b bytes.Buffer
		m := multipart.NewWriter(&b)
		if many {
			for range 11 {
				if err := m.WriteField("skip", ""); err != nil {
					t.Fatal(err)
				}
			}
		} else {
			if err := m.WriteField("skip", strings.Repeat("x", (1<<20)+multipartAllowance)); err != nil {
				t.Fatal(err)
			}
		}
		p, err := m.CreateFormFile("file", "x")
		if err != nil {
			t.Fatal(err)
		}
		p.Write(data[:1])
		m.Close()
		r := req{token: tok, body: b.String(), header: map[string]string{"Content-Type": m.FormDataContentType()}}
		if many {
			h.expect(h.do("POST", restorePath, r), 400, "validation_failed")
		} else {
			h.expect(h.do("POST", restorePath, r), 413, "too_large")
		}
	}
	// The initial restore and the multipart count rejection each spent a token.
	for range 4 {
		h.expect(h.do("POST", restorePath, upload(t, tok, "any", []byte{0x12, 0})), 200, "")
	}
	h.expect(h.do("POST", restorePath, upload(t, tok, "any", []byte{0x12, 0})), 429, "rate_limited")
}
