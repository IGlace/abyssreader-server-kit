// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"bytes"
	"errors"
	"github.com/IGlace/abyssreader-server-kit/internal/config"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/backup"
)

func TestBackupMultipartTotalIncludesPartsAndEpilogueOnChunkedRequests(t *testing.T) {
	for _, where := range []string{"before", "after", "epilogue"} {
		t.Run(where, func(t *testing.T) {
			var body bytes.Buffer
			m := multipart.NewWriter(&body)
			const cap = 1024
			large := strings.Repeat("x", cap+multipartAllowance)
			if where == "before" {
				if err := m.WriteField("metadata", large); err != nil {
					t.Fatal(err)
				}
			}
			p, err := m.CreateFormFile("file", "backup")
			if err != nil {
				t.Fatal(err)
			}
			p.Write([]byte{0x12, 0})
			if where == "after" {
				if err := m.WriteField("metadata", large); err != nil {
					t.Fatal(err)
				}
			}
			m.Close()
			if where == "epilogue" {
				body.WriteString(large)
			}
			r := httptest.NewRequest("POST", restorePath, &body)
			r.ContentLength = -1
			r.Header.Set("Content-Type", m.FormDataContentType())
			_, err = readBackup(httptest.NewRecorder(), r, cap)
			var over *http.MaxBytesError
			if !errors.As(err, &over) {
				t.Fatalf("missing streamed whole-body limit: %v", err)
			}
		})
	}
}
func TestBackupNameMatchRestoresOnlyListsAndBoundsEcho(t *testing.T) {
	h := newHarness(t, backupOn)
	h.smallCatalog()
	tok, _ := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	var buf bytes.Buffer
	e := backup.Entry{Title: "El guardián del farol", Extra: &backup.Extra{Lists: []string{"planning", "future-list"}, Vote: "down"}, Chapters: []backup.Chapter{{Read: true, URL: "/c"}}, History: &backup.History{URL: "/c", LastRead: 1700000000000}}
	if err := backup.WriteEntry(&buf, e); err != nil {
		t.Fatal(err)
	}
	for range 505 {
		if err := backup.WriteEntry(&buf, backup.Entry{Title: strings.Repeat("界", 300) + "\u2066"}); err != nil {
			t.Fatal(err)
		}
	}
	a := h.do("POST", restorePath, upload(t, tok, "anything", buf.Bytes()))
	h.expect(a, 200, "")
	counts(t, a, 0, 1, 505, 0, 1)
	lines := a.body["unmatched"].([]any)
	if len(lines) != 500 || len([]rune(lines[0].(map[string]any)["title"].(string))) != 200 {
		t.Fatal("unbounded report")
	}
	if writes, _ := h.pull(tok, "", 0); len(writes) != 1 || writes[0]["kind"] != "planning" {
		t.Fatalf("name-only writes %v", writes)
	}
	// A title left without a visible source and a title absent from the
	// catalog are independently reported as unmatched.
	if _, err := h.db.Exec("UPDATE sources SET hosted=1"); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	backup.WriteEntry(&buf, e)
	backup.WriteEntry(&buf, backup.Entry{Title: "Gone", URL: "/removed"})
	a = h.do("POST", restorePath, upload(t, tok, "anything", buf.Bytes()))
	counts(t, a, 0, 0, 2, 0, 1)
}
func TestBackupAuthenticatesWithServerAccessKey(t *testing.T) {
	h := newHarness(t, backupOn)
	tok, _ := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	h = h.reopen(func(c *config.Config) { c.AccessKey = "demo-access-key-local" })
	for _, op := range []struct{ method, path string }{{"GET", backupPath}, {"POST", restorePath}} {
		h.expect(h.do(op.method, op.path, req{token: tok}), 403, "access_key_required")
		h.expect(h.do(op.method, op.path, req{accessKey: "incorrect"}), 403, "access_key_invalid")
		h.expect(h.do(op.method, op.path, req{accessKey: "demo-access-key-local"}), 401, "session_invalid")
	}
}

func TestBackupStorageFailureBeforeExportIsContractError(t *testing.T) {
	h := newHarness(t, backupOn)
	tok, _ := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	if _, err := h.db.Exec("ALTER TABLE sync_records RENAME TO unavailable_records"); err != nil {
		t.Fatal(err)
	}
	a := h.do("GET", backupPath, req{token: tok})
	h.expect(a, 500, "server_error")
	if a.header.Get("Content-Disposition") != "" {
		t.Fatal("error body offered as a download")
	}
}
