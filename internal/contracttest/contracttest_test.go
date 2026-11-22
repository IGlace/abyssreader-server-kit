// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package contracttest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recorder catches the errors Check reports, so the self-test can prove each break is caught.
type recorder struct {
	testing.TB
	errs []string
}

func (r *recorder) Errorf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}
func (r *recorder) Fatalf(format string, args ...any) { r.Errorf(format, args...) }
func (r *recorder) Helper()                           {}

const okStatus = `{"name":"Lantern Library","accessKeyRequired":false,"signUpOpen":true,
"operator":{"name":"Lantern Library team","contact":"mailto:operator@lantern.example.org","privacyUrl":null,"termsUrl":null,"copyrightUrl":null},
"contracts":[{"version":"1.0","basePath":"/api/v1","features":[]}]}`

func check(t *testing.T, method, path string, status int, header http.Header, body string) []string {
	t.Helper()
	c := Load(t)
	req := httptest.NewRequest(method, "http://server.example.org"+path, nil)
	if header == nil {
		header = http.Header{"Content-Type": {"application/json"}}
	}
	resp := &http.Response{StatusCode: status, Header: header, Request: req}
	r := &recorder{TB: t}
	c.Check(r, req, resp, []byte(body))
	return r.errs
}

func TestAValidAnswerPasses(t *testing.T) {
	if errs := check(t, "GET", "/api/status", 200, nil, okStatus); len(errs) != 0 {
		t.Fatalf("a valid status was refused: %v", errs)
	}
	if errs := check(t, "POST", "/api/v1/me/deletion", 204, http.Header{}, ""); len(errs) != 0 {
		t.Fatalf("a valid 204 was refused: %v", errs)
	}
	if errs := check(t, "GET", "/api/v1/me", 401, nil, `{"code":"session_invalid","message":"Sign in again."}`); len(errs) != 0 {
		t.Fatalf("a valid 401 was refused: %v", errs)
	}
}

// Every way an answer can break the contract is caught.
func TestEveryBreakIsCaught(t *testing.T) {
	cases := []struct {
		name, method, path string
		status             int
		header             http.Header
		body               string
	}{
		{"missing required field", "GET", "/api/status", 200, nil, strings.Replace(okStatus, `"signUpOpen":true,`, "", 1)},
		{"undeclared field", "GET", "/api/status", 200, nil, strings.Replace(okStatus, `"name":"Lantern Library",`, `"name":"Lantern Library","vipAvailable":true,`, 1)},
		{"nested undeclared field", "GET", "/api/status", 200, nil, strings.Replace(okStatus, `"privacyUrl":null`, `"privacyUrl":null,"donationUrl":null`, 1)},
		{"null for a non-null field", "GET", "/api/status", 200, nil, strings.Replace(okStatus, `"name":"Lantern Library",`, `"name":null,`, 1)},
		{"wrong type", "GET", "/api/status", 200, nil, strings.Replace(okStatus, `"signUpOpen":true`, `"signUpOpen":"yes"`, 1)},
		{"plain http link", "GET", "/api/status", 200, nil, strings.Replace(okStatus, `"privacyUrl":null`, `"privacyUrl":"http://lantern.example.org/p"`, 1)},
		{"undeclared status", "GET", "/api/status", 404, nil, `{"code":"not_found","message":"x"}`},
		{"error code not listed for the status", "GET", "/api/v1/me", 403, nil, `{"code":"sign_up_closed","message":"x"}`},
		{"error without the Error body", "GET", "/api/v1/me", 401, nil, `{"error":"unauthorized"}`},
		{"429 without Retry-After", "POST", "/api/v1/auth/sign-in", 429, nil, `{"code":"rate_limited","message":"x"}`},
		{"204 with a body", "POST", "/api/v1/me/deletion", 204, http.Header{"Content-Type": {"application/json"}}, `{}`},
		{"bad email in an account", "GET", "/api/v1/me", 200, nil, `{"id":1,"displayName":"reader","email":"not-an-email","avatarUrl":null,"hasPassword":true}`},
		{"id out of range", "GET", "/api/v1/me", 200, nil, `{"id":0,"displayName":"reader","email":"a@example.com","avatarUrl":null,"hasPassword":true}`},
		{"route not in the contract", "GET", "/api/v1/nope", 200, nil, `{}`},
	}
	for _, c := range cases {
		if errs := check(t, c.method, c.path, c.status, c.header, c.body); len(errs) == 0 {
			t.Errorf("%s: not caught", c.name)
		}
	}
}

func TestOperationIDs(t *testing.T) {
	ids := Load(t).OperationIDs("/api/v1/me")
	want := []string{"changePassword", "deleteAccount", "exportBackup", "getLibrary", "getMe", "getMyStats", "listMyCollections", "restoreBackup", "updateMe"}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("OperationIDs(/api/v1/me) = %v", ids)
	}
}
