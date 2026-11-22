// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/accounts"
	"github.com/IGlace/abyssreader-server-kit/internal/backup"
)

func queuedRequest(id int64) *http.Request {
	r := httptest.NewRequest("POST", restorePath, strings.NewReader("body"))
	r.Header.Set("X-Test-Request", strconv.FormatInt(id, 10))
	return r
}
func tryRestore(s *server, id int64, r *http.Request, decode func(http.ResponseWriter, *http.Request, int64) (*backup.File, error)) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.restoreUpload(w, r, caller{account: accounts.Account{ID: id}}, decode)
	return w
}

func TestBackupPoolAdmitsTwoMaximumUploads(t *testing.T) {
	for _, mib := range []int{20, 128} {
		t.Run(fmt.Sprint(mib), func(t *testing.T) {
			s := &server{backups: newBackupLimits()}
			s.Config.AccountBackupMaxMiB = mib
			maxBody := (int64(mib) << 20) + multipartAllowance
			// Simulate one complete upload and all but the final page of a second.
			// The handler should still be able to admit that last page.
			s.backups.uploads.used.Store(2*maxBody - uploadPageSize)
			decode := func(http.ResponseWriter, *http.Request, int64) (*backup.File, error) { return nil, backup.ErrInvalid }
			if got := tryRestore(s, 1, queuedRequest(1), decode); got.Code != 400 {
				t.Fatalf("two maximum uploads do not fit: last page got %d", got.Code)
			}
			s.backups.uploads.used.Store(2 * maxBody)
			if got := tryRestore(s, 2, queuedRequest(2), decode); got.Code != 429 || got.Header().Get("Retry-After") == "" {
				t.Fatalf("expected refusal at pool capacity, got %d", got.Code)
			}
		})
	}
}

func TestFinishedUploadsGetProcessingInOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &server{backups: newBackupLimits()}
		admitted := make(chan string, 4)
		release := make(chan struct{})
		done := make(chan int, 4)
		decode := func(_ http.ResponseWriter, r *http.Request, _ int64) (*backup.File, error) {
			admitted <- r.Header.Get("X-Test-Request")
			<-release
			return nil, backup.ErrInvalid
		}
		for id := int64(1); id <= 4; id++ {
			go func() { done <- tryRestore(s, id, queuedRequest(id), decode).Code }()
			synctest.Wait()
		}
		if early := len(done); early != 0 {
			close(release)
			t.Fatalf("%d fully uploaded requests refused instead of queued", early)
		}
		for id := 1; id <= 4; id++ {
			if next := <-admitted; next != strconv.Itoa(id) {
				t.Fatalf("got %s, want %d", next, id)
			}
			release <- struct{}{}
			if code := <-done; code != 400 {
				t.Fatalf("got %d, want decoder response", code)
			}
		}
		if used := s.backups.uploads.used.Load(); used != 0 {
			t.Fatalf("%d bytes still reserved", used)
		}
	})
}

func TestFinishedUploadWaitExpiresAndCancellationReturnsPages(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &server{backups: newBackupLimits()}
		release := make(chan struct{})
		decode := func(http.ResponseWriter, *http.Request, int64) (*backup.File, error) {
			<-release
			return nil, backup.ErrInvalid
		}
		go func() { tryRestore(s, 1, queuedRequest(1), decode) }()
		synctest.Wait()
		r := queuedRequest(2)
		ctx, cancel := context.WithCancel(r.Context())
		lost := make(chan struct{})
		go func() { tryRestore(s, 2, r.WithContext(ctx), decode); close(lost) }()
		synctest.Wait()
		cancel()
		<-lost
		if used := s.backups.uploads.used.Load(); used != uploadPageSize {
			close(release)
			t.Fatalf("canceled request's pages still held: %d", used)
		}
		start := time.Now()
		w := tryRestore(s, 3, queuedRequest(3), decode)
		close(release)
		if elapsed := time.Since(start); elapsed != time.Minute || w.Code != 429 || w.Header().Get("Retry-After") == "" {
			t.Fatalf("busy queue: waited %s, got %d", elapsed, w.Code)
		}
	})
}

func TestRestoreReleasesSlotIfDecoderPanics(t *testing.T) {
	s := &server{backups: newBackupLimits()}
	func() {
		defer func() {
			if recover() != "injected decoder panic" {
				t.Fatal("expected injected panic")
			}
		}()
		tryRestore(s, 1, queuedRequest(1), func(http.ResponseWriter, *http.Request, int64) (*backup.File, error) { panic("injected decoder panic") })
	}()
	if used := s.backups.uploads.used.Load(); used != 0 {
		t.Fatalf("panic retained %d bytes", used)
	}
	w := tryRestore(s, 2, queuedRequest(2), func(http.ResponseWriter, *http.Request, int64) (*backup.File, error) { return nil, backup.ErrInvalid })
	if w.Code != 400 {
		t.Fatalf("decoder panic stranded the processing slot: %d", w.Code)
	}
}

func TestRejectedDecodesConsumeAttempts(t *testing.T) {
	s := &server{backups: newBackupLimits()}
	bad := upload(t, "", "truncated.gz", []byte{0x1f, 0x8b, 0x00})
	for attempt := 1; attempt <= 7; attempt++ {
		r := httptest.NewRequest("POST", restorePath, strings.NewReader(bad.body.(string)))
		r.Header.Set("Content-Type", bad.header["Content-Type"])
		w := tryRestore(s, 4, r, readBackup)
		want := 400
		if attempt == 7 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("attempt %d: got %d, want %d", attempt, w.Code, want)
		}
	}
}

type zeroBody struct{}

func (zeroBody) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type delayedEOF chan struct{}

func (e delayedEOF) Read([]byte) (int, error) { <-e; return 0, io.EOF }

func TestTwoFullSizedUploadsCanWaitForEOF(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &server{backups: newBackupLimits()}
		const maxBody = 20<<20 + multipartAllowance
		sendEOF := make(delayedEOF)
		done := make(chan int, 2)
		decode := func(http.ResponseWriter, *http.Request, int64) (*backup.File, error) { return nil, backup.ErrInvalid }
		for id := int64(1); id <= 2; id++ {
			r := httptest.NewRequest("POST", restorePath, io.MultiReader(io.LimitReader(zeroBody{}, maxBody), sendEOF))
			go func() { done <- tryRestore(s, id, r, decode).Code }()
		}
		synctest.Wait()
		if got := s.backups.uploads.used.Load(); got != 2*maxBody {
			close(sendEOF)
			t.Fatalf("two uploads at EOF reserved %d of %d bytes", got, 2*maxBody)
		}
		w := tryRestore(s, 3, queuedRequest(3), decode)
		close(sendEOF)
		if w.Code != 429 || w.Header().Get("Retry-After") == "" {
			t.Fatalf("third upload at capacity got %d", w.Code)
		}
		for range 2 {
			if code := <-done; code != 400 {
				t.Fatalf("expected admitted upload to reach decoder, got %d", code)
			}
		}
		if used := s.backups.uploads.used.Load(); used != 0 {
			t.Fatalf("finished uploads retained %d bytes", used)
		}
	})
}
