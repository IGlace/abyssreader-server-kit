// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/accounts"
	"github.com/IGlace/abyssreader-server-kit/internal/backup"
	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

func TestSlowUploadsLeaveRoomForReadyRestore(t *testing.T) {
	h := newHarness(t, backupOn)
	s := &server{Options: Options{Config: h.cfg, DB: h.db, Logger: h.logger}, backups: newBackupLimits(), sync: usersync.New(h.db)}
	u := upload(t, "", "backup", []byte{0x12, 0})
	send := func(id int64, body io.Reader) int {
		r := httptest.NewRequest("POST", restorePath, body)
		r.Header.Set("Content-Type", u.header["Content-Type"])
		w := httptest.NewRecorder()
		s.restoreBackup(w, r, caller{account: accounts.Account{ID: id}})
		return w.Code
	}
	resume := make(chan struct{})
	done := make(chan int, 4)
	defer func() {
		close(resume)
		for range 4 {
			<-done
		}
	}()
	for i := range 4 {
		start := make(chan struct{})
		go func() { done <- send(int64(i+1), &pausedUpload{start, resume, strings.NewReader(u.body.(string))}) }()
		<-start
	}
	if code := send(5, strings.NewReader(u.body.(string))); code != 200 {
		t.Fatalf("ready account got HTTP %d while other accounts were still uploading", code)
	}
}

type heldExport struct {
	*httptest.ResponseRecorder
	started chan struct{}
	resume  chan struct{}
}

func (w *heldExport) Write(p []byte) (int, error) {
	if w.started != nil {
		close(w.started)
		w.started = nil
		<-w.resume
	}
	return w.ResponseRecorder.Write(p)
}
func TestExportCapacityAcrossAccounts(t *testing.T) {
	h := newHarness(t, backupOn)
	s := &server{Options: Options{Config: h.cfg, DB: h.db, Logger: h.logger}, backups: newBackupLimits()}
	send := func(id int64, w http.ResponseWriter) {
		s.exportBackup(w, httptest.NewRequest("GET", backupPath, nil), caller{account: accounts.Account{ID: id}})
	}
	resume := make(chan struct{})
	done := make(chan struct{}, 2)
	defer func() {
		close(resume)
		for range 2 {
			<-done
		}
	}()
	for i := range 2 {
		start := make(chan struct{})
		go func() { send(int64(i+1), &heldExport{httptest.NewRecorder(), start, resume}); done <- struct{}{} }()
		<-start
	}
	w := httptest.NewRecorder()
	send(3, w)
	if w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Fatalf("export at server capacity got %d, retry %q", w.Code, w.Header().Get("Retry-After"))
	}
}

func TestUploadBudgetCountsPagesAndReturnsThem(t *testing.T) {
	var pool uploadPool
	const budget = 2 * uploadPageSize
	read := func(size int) (*bufferedUpload, error) {
		r := httptest.NewRequest("POST", restorePath, strings.NewReader(strings.Repeat("a", size)))
		return pool.read(httptest.NewRecorder(), r, 4*budget, budget)
	}
	first, err := read(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := read(uploadPageSize + 1); !errors.Is(err, errUploadCapacity) {
		t.Fatalf("budget: %v", err)
	}
	if got := pool.used.Load(); got != uploadPageSize {
		t.Fatalf("after refusal: %d bytes retained", got)
	}
	first.release()
	exact, err := read(budget)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(io.MultiReader(exact.pages...))
	if err != nil || len(got) != budget {
		t.Fatalf("replayed %d bytes, %v", len(got), err)
	}
	exact.release()
	if got := pool.used.Load(); got != 0 {
		t.Fatalf("after release: %d bytes retained", got)
	}
}

func TestIncompleteRestoreUsesContractError(t *testing.T) {
	h := newHarness(t, backupOn)
	s := &server{}
	for _, tc := range []struct {
		reason        error
		retry, action string
	}{
		{backup.ErrRestoreIncomplete, "30", "retry the same file"},
		{backup.ErrRestoreCapacity, "30", "retry once capacity is available"},
		{backup.ErrRestoreRetention, "30", "restore smaller files"},
	} {
		r := httptest.NewRequest("POST", restorePath, nil)
		w := httptest.NewRecorder()
		var report backup.Report
		report.ChapterMarks.Restored = 27
		s.writeRestoreResult(w, report, tc.reason)
		h.contract.Check(t, r, w.Result(), w.Body.Bytes())
		var body errorBody
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != 503 || w.Header().Get("Retry-After") != tc.retry || body.Code != "unavailable" || !strings.Contains(body.Message, "27 chapter marks committed") || !strings.Contains(body.Message, tc.action) {
			t.Fatalf("%v: %d %q %+v", tc.reason, w.Code, w.Header().Get("Retry-After"), body)
		}
	}
}
