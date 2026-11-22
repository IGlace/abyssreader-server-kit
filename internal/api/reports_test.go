// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/config"
)

func reportsOn(c *config.Config) { c.Features = []string{"reports"} }

const reportPath = "/api/v1/titles/1/reports"

func TestTitleReportLifecycle(t *testing.T) {
	h := newHarness(t, reportsOn)
	h.smallCatalog()
	token, id := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	second, _ := h.signUp("Other", "other@example.com", "correct-horse-7")
	post := req{token: token, body: obj{"reason": "wrong_information", "details": strings.Repeat("星", 500)}}
	h.expect(h.do("POST", reportPath, post), 204, "")
	h.expect(h.do("POST", reportPath, req{token: token, body: obj{"reason": "wrong_source"}}), 409, "already_exists")
	h.expect(h.do("POST", reportPath, req{token: second, body: obj{"reason": "other", "details": nil}}), 204, "")
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		h.expect(h.do(method, reportPath, req{token: second, noCheck: true}), 405, "method_not_allowed")
	}
	off := h.reopen(func(c *config.Config) { c.Features = nil })
	off.expect(off.do("POST", reportPath, req{}), 404, "feature_disabled")
	if strings.Contains(string(off.do("GET", "/api/status", req{}).raw), `"reports"`) {
		t.Fatal("reports advertised while off")
	}
	on := off.reopen(reportsOn)
	if !strings.Contains(string(on.do("GET", "/api/status", req{}).raw), `"features":["reports"]`) {
		t.Fatal("missing reports in status")
	}
	on.expect(on.do("POST", reportPath, post), 409, "already_exists")
	on.expect(on.do("POST", "/api/v1/me/deletion", req{token: token, body: obj{"password": "correct-horse-7"}}), 204, "")
	if on.rowsOf(id) != 0 {
		t.Fatal("deleted account still has rows")
	}
	on.expect(on.do("POST", reportPath, req{token: second, body: post.body}), 409, "already_exists")
}

func TestTitleReportValidationAndAccess(t *testing.T) {
	h := newHarness(t, reportsOn)
	h.smallCatalog()
	token, _ := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	protected := h.reopen(func(c *config.Config) { c.AccessKey = "access-key-example" })
	for _, tc := range []struct {
		r      req
		status int
		code   string
	}{
		{req{}, 403, "access_key_required"}, {req{accessKey: "wrong"}, 403, "access_key_invalid"},
		{req{accessKey: "access-key-example"}, 401, "session_invalid"},
	} {
		protected.expect(protected.do("POST", reportPath, tc.r), tc.status, tc.code)
	}
	for _, body := range []any{
		nil, "[]", "{}{}", obj{},
		obj{"reason": "new_reason"}, obj{"reason": []string{"other"}},
		obj{"reason": "other", "details": ""}, obj{"reason": "other", "details": 42},
		obj{"reason": "other", "details": strings.Repeat("星", 501)},
		strings.Repeat(" ", 65537) + "{}",
	} {
		h.expect(h.do("POST", reportPath, req{token: token, body: body}), 400, "validation_failed")
	}
	// A fresh server has a fresh rate budget for the path and visibility cases.
	h = h.reopen(reportsOn)
	post := req{token: token, body: obj{"reason": "chapter_not_working"}}
	h.expect(h.do("POST", "/api/v1/titles/0/reports", post), 400, "validation_failed")
	h.expect(h.do("POST", "/api/v1/titles/99999/reports", post), 404, "not_found")
	if _, err := h.db.Exec("UPDATE sources SET hosted=1"); err != nil {
		t.Fatal(err)
	}
	h.expect(h.do("POST", reportPath, post), 404, "not_found")
	on := h.reopen(func(c *config.Config) { c.Features = []string{"reports", "hosted-chapters"} })
	on.expect(on.do("POST", reportPath, post), 204, "")
}

func TestTitleReportRateLimitPerAccount(t *testing.T) {
	h := newHarness(t, reportsOn)
	h.smallCatalog()
	token, _ := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	for i := 0; i < 10; i++ {
		want, code := 409, "already_exists"
		if i == 0 {
			want, code = 204, ""
		}
		h.expect(h.do("POST", reportPath, req{token: token, body: obj{"reason": "other"}}), want, code)
	}
	a := h.do("POST", reportPath, req{token: token, xff: "192.0.2.45", body: obj{"reason": "other"}})
	h.expect(a, 429, "rate_limited")
	second, _ := h.signUp("Other", "other@example.com", "correct-horse-7")
	h.expect(h.do("POST", reportPath, req{token: second, body: obj{"reason": "other"}}), 204, "")
}

func TestTitleReportBusy(t *testing.T) {
	h := newHarness(t, reportsOn)
	h.smallCatalog()
	token, _ := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	// Keep the authenticated read on a separate connection and shorten its busy timeout.
	h.db.SetMaxOpenConns(1)
	if _, err := h.db.Exec("PRAGMA busy_timeout=10"); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", fmt.Sprintf("file:%s/abyssreader.db?_txlock=immediate", h.cfg.DataDir))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	hold, err := other.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback()
	a := h.do("POST", reportPath, req{token: token, body: obj{"reason": "other"}})
	h.expect(a, 503, "unavailable")
	if a.header.Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
}

func TestTitleReportStorageFailure(t *testing.T) {
	h := newHarness(t, reportsOn)
	h.smallCatalog()
	token, _ := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	if _, err := h.db.Exec("ALTER TABLE title_reports RENAME TO unavailable_reports"); err != nil {
		t.Fatal(err)
	}
	h.expect(h.do("POST", reportPath, req{token: token, body: obj{"reason": "other"}}), 500, "server_error")
}
