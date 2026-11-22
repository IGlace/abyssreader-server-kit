// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/accounts"
	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

type pausedUpload struct {
	started, resume chan struct{}
	io.Reader
}

func (u *pausedUpload) Read(p []byte) (int, error) {
	if u.started != nil {
		close(u.started)
		u.started = nil
		<-u.resume
	}
	return u.Reader.Read(p)
}
func TestAccountBackupSlotsAndAttemptBudget(t *testing.T) {
	h := newHarness(t, backupOn)
	if status := h.do("GET", "/api/status", req{}); !strings.Contains(string(status.raw), `"features":["backup"]`) {
		t.Fatal("missing advertised backup feature")
	}
	s := &server{Options: Options{Config: h.cfg, DB: h.db, Logger: h.logger}, backups: newBackupLimits(), sync: usersync.New(h.db)}
	empty := upload(t, "", "any", []byte{0x12, 0})
	do := func(id int64, body io.Reader) *httptest.ResponseRecorder {
		r := httptest.NewRequestWithContext(context.Background(), "POST", restorePath, body)
		r.Header.Set("Content-Type", empty.header["Content-Type"])
		w := httptest.NewRecorder()
		s.restoreBackup(w, r, caller{account: accounts.Account{ID: id}})
		return w
	}
	fresh := func() io.Reader { return strings.NewReader(empty.body.(string)) }
	start, resume := make(chan struct{}), make(chan struct{})
	finished := make(chan int, 1)
	go func() { finished <- do(1, &pausedUpload{start, resume, fresh()}).Code }()
	<-start
	// Another request from that account is refused. A slow upload has taken
	// neither a write slot nor a rate token.
	same := do(1, fresh())
	if same.Code != 429 || same.Header().Get("Retry-After") == "" || len(s.backups.processing) != 0 {
		close(resume)
		t.Fatal("slow upload held a database slot or allowed overlap")
	}
	// The resumed upload and five more restores exhaust the account budget.
	close(resume)
	if code := <-finished; code != 200 {
		t.Fatalf("resumed upload got %d", code)
	}
	for range 5 {
		if w := do(1, fresh()); w.Code != 200 {
			t.Fatalf("busy attempt spent budget: %d", w.Code)
		}
	}
	if w := do(1, fresh()); w.Code != 429 {
		t.Fatalf("rate cap: %d", w.Code)
	}
	// An in-flight export of the same account also produces a contract 429.
	s.backups.exporting.Store(int64(1), true)
	r := httptest.NewRequest("GET", backupPath, nil)
	w := httptest.NewRecorder()
	s.exportBackup(w, r, caller{account: accounts.Account{ID: 1}})
	h.contract.Check(t, r, w.Result(), w.Body.Bytes())
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("concurrent export: %d", w.Code)
	}
}
