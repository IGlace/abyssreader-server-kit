// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

type busyErr struct{}

func (busyErr) Error() string { return "database is locked" }
func (busyErr) Code() int     { return 5 | 2<<8 } // SQLITE_BUSY_SNAPSHOT: an extended busy code

func TestABusyStoreAnswers503WithRetryAfter(t *testing.T) {
	s := &server{}
	s.Logger = newHarness(t, nil).logger
	w := httptest.NewRecorder()
	s.storeFailed(w, "x", errors.Join(errors.New("push"), busyErr{}))
	if w.Code != 503 || w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), `"unavailable"`) {
		t.Fatalf("%d %v %s", w.Code, w.Header(), w.Body)
	}
	w = httptest.NewRecorder()
	s.storeFailed(w, "x", errors.New("disk I/O error"))
	if w.Code != 500 {
		t.Fatalf("another error: %d", w.Code)
	}
}

// Every failure of a route's database read or write goes through storeFailed: the only other
// 500s are a panic and a cover file that cannot be read.
func TestStoreFailuresGoThroughOnePlace(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	call := regexp.MustCompile(`writeServerError\(w\)`)
	allowed := map[string]int{"respond.go": 1, "middleware.go": 1, "covers.go": 1}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		if n := len(call.FindAll(b, -1)); n != allowed[f] {
			t.Errorf("%s calls writeServerError %d times, want %d (use storeFailed)", f, n, allowed[f])
		}
	}
}

// A write that waits past the busy timeout for SQLite's one writer (another connection holds
// it) gets 503 with Retry-After, not 500. Reads never wait for the writer in WAL mode, so the
// pull and the library cannot be made busy this way; they answer through the same storeFailed
// (TestStoreFailuresGoThroughOnePlace).
func TestABusyDatabaseIs503OnAPush(t *testing.T) {
	h := newHarness(t, nil)
	tok, _ := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	other, err := sql.Open("sqlite", "file:"+filepath.Join(h.cfg.DataDir, "abyssreader.db")+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	hold, err := other.BeginTx(context.Background(), nil) // BEGIN IMMEDIATE: the write lock
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback()
	a := h.do("POST", "/api/v1/sync/push", req{token: tok, body: obj{"items": []obj{item("favorite", titleKey(1), nil, "2026-10-10T10:00:00.000Z", false)}}})
	if a.status != 503 || a.code() != "unavailable" || a.header.Get("Retry-After") == "" {
		t.Fatalf("%d %s", a.status, a.raw)
	}
}
