// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/accounts"
	"github.com/IGlace/abyssreader-server-kit/internal/config"
	"github.com/IGlace/abyssreader-server-kit/internal/contracttest"
	"github.com/IGlace/abyssreader-server-kit/internal/covers"
	"github.com/IGlace/abyssreader-server-kit/internal/pages"
	"github.com/IGlace/abyssreader-server-kit/internal/password"
	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

// harness is one server on its own empty database. Every answer to a contract route goes
// through the contract check.
type harness struct {
	t        *testing.T
	srv      *httptest.Server
	db       *sql.DB
	accounts *accounts.Service
	covers   *covers.Store
	pages    *covers.Store
	contract *contracttest.Contract
	cfg      config.Config
	logger   *slog.Logger
}

func testConfig() config.Config {
	defaults, err := config.Load(func(k string) string {
		return map[string]string{"ABYSS_SERVER_NAME": "x", "ABYSS_OPERATOR_NAME": "x", "ABYSS_OPERATOR_CONTACT": "mailto:x@example.org", "ABYSS_PUBLIC_URL": "https://x.example.org"}[k]
	})
	if err != nil {
		panic(err)
	}
	return config.Config{
		TrustedProxies:           defaults.TrustedProxies, // loopback and the private ranges
		SignInFailuresPerAccount: 1000,
		ServerName:               "Lantern Library",
		PublicURL:                "https://lantern.example.org",
		Operator:                 config.Operator{Name: "Lantern Library team", Contact: "mailto:operator@lantern.example.org"},
		SignUpOpen:               true,
		SessionIdleTimeout:       90 * 24 * time.Hour,
		SignUpsPerHour:           1000,
		SignInsPerMinute:         1000,
	}
}

// logTo, when set, receives the server's log for the next harness.
var logTo io.Writer

func newHarness(t *testing.T, change func(*config.Config)) *harness {
	t.Helper()
	cfg := testConfig()
	if change != nil {
		change(&cfg)
	}
	cfg.DataDir = t.TempDir()
	db, err := store.Open(context.Background(), filepath.Join(cfg.DataDir, "abyssreader.db"))
	if err != nil {
		t.Fatal(err)
	}
	cs, err := covers.OpenStore(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	svc := accounts.New(db, cfg.SessionIdleTimeout)
	logger := slog.New(slog.DiscardHandler)
	if logTo != nil {
		logger = slog.New(slog.NewTextHandler(logTo, nil))
	}
	ps, err := pages.OpenStore(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	h := New(Options{Config: cfg, DB: db, Accounts: svc, Covers: cs, Pages: ps, Logger: logger})
	srv := httptest.NewServer(h)
	t.Cleanup(func() { srv.Close(); db.Close() })
	return &harness{t: t, srv: srv, db: db, accounts: svc, covers: cs, pages: ps, contract: contracttest.Load(t), cfg: cfg, logger: logger}
}

type req struct {
	token     string // sent as "Bearer <token>"
	auth      string // a raw Authorization header instead
	accessKey string
	xff       string // X-Forwarded-For
	body      any    // marshalled to JSON; a string is sent as it is
	header    map[string]string
	noCheck   bool
}

type answer struct {
	status int
	header http.Header
	raw    []byte
	body   map[string]any
}

func (a answer) code() string { s, _ := a.body["code"].(string); return s }

func (h *harness) do(method, path string, r req) answer {
	h.t.Helper()
	var body io.Reader
	switch b := r.body.(type) {
	case nil:
	case string:
		body = strings.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		body = bytes.NewReader(j)
	}
	hr, _ := http.NewRequest(method, h.srv.URL+path, body)
	if body != nil {
		hr.Header.Set("Content-Type", "application/json")
	}
	hr.Header.Set("User-Agent", "AbyssReader/1.0.0 (android)")
	if r.token != "" {
		hr.Header.Set("Authorization", "Bearer "+r.token)
	}
	if r.auth != "" {
		hr.Header.Set("Authorization", r.auth)
	}
	if r.accessKey != "" {
		hr.Header.Set("X-Access-Key", r.accessKey)
	}
	if r.xff != "" {
		hr.Header.Set("X-Forwarded-For", r.xff)
	}
	for k, v := range r.header {
		hr.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(hr)
	if err != nil {
		h.t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	a := answer{status: resp.StatusCode, header: resp.Header, raw: raw}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a.body); err != nil {
			h.t.Errorf("%s %s: the body is not a JSON object: %s", method, path, raw)
		}
	}
	if !r.noCheck {
		h.contract.Check(h.t, hr, resp, raw)
	}
	return a
}

func (h *harness) expect(a answer, status int, code string) {
	h.t.Helper()
	if a.status != status || a.code() != code {
		h.t.Fatalf("got %d %q (%s), want %d %q", a.status, a.code(), a.raw, status, code)
	}
}

// signUp makes an account through the API and returns its token and id.
func (h *harness) signUp(name, email, pw string) (string, int64) {
	h.t.Helper()
	a := h.do("POST", "/api/v1/auth/sign-up", req{body: map[string]any{"displayName": name, "email": email, "password": pw}})
	h.expect(a, 201, "")
	user := a.body["user"].(map[string]any)
	return a.body["token"].(string), int64(user["id"].(float64))
}

func (h *harness) rowsOf(id int64) int {
	h.t.Helper()
	n, err := store.AccountRowCount(context.Background(), h.db, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return n
}

// ── Server status ──

func TestStatus(t *testing.T) {
	h := newHarness(t, nil)
	a := h.do("GET", "/api/status", req{})
	h.expect(a, 200, "")
	want := `{"name":"Lantern Library","accessKeyRequired":false,"signUpOpen":true,` +
		`"operator":{"name":"Lantern Library team","contact":"mailto:operator@lantern.example.org","privacyUrl":null,"termsUrl":null,"copyrightUrl":null},` +
		`"contracts":[{"version":"` + h.contract.Version + `","basePath":"/api/v1","features":[]}]}`
	if strings.TrimSpace(string(a.raw)) != want {
		t.Fatalf("status:\n got %s\nwant %s", a.raw, want)
	}
}

func TestStatusAnnouncesTheSettings(t *testing.T) {
	privacy, terms, copyright := "https://lantern.example.org/privacy", "https://lantern.example.org/terms", "https://lantern.example.org/copyright"
	h := newHarness(t, func(c *config.Config) {
		c.SignUpOpen = false
		c.AccessKey = "lantern-access-key-1"
		c.Operator.PrivacyURL, c.Operator.TermsURL, c.Operator.CopyrightURL = &privacy, &terms, &copyright
	})
	// Always public: no access key needed, and Authorization is ignored here, even a bad one.
	a := h.do("GET", "/api/status", req{token: "not-a-session"})
	h.expect(a, 200, "")
	if a.body["accessKeyRequired"] != true || a.body["signUpOpen"] != false {
		t.Fatalf("%s", a.raw)
	}
	op := a.body["operator"].(map[string]any)
	if op["privacyUrl"] != privacy || op["termsUrl"] != terms || op["copyrightUrl"] != copyright {
		t.Fatalf("operator links: %s", a.raw)
	}
}

func TestContractVersionMatchesTheContractFile(t *testing.T) {
	if c := contracttest.Load(t); ContractVersion != c.Version {
		t.Fatalf("api.ContractVersion = %q, contract/openapi.yaml info.version = %q", ContractVersion, c.Version)
	}
}

// ── Accounts ──

// The whole account life, as the conformance check's sign-up / login and account-deletion
// features walk it.
func TestAccountJourney(t *testing.T) {
	h := newHarness(t, nil)
	token, id := h.signUp("quietreader", "reader@example.com", "correct-horse-7")

	// Sign-up gives a working session at once.
	a := h.do("GET", "/api/v1/me", req{token: token})
	h.expect(a, 200, "")
	if a.body["id"] != float64(id) || a.body["email"] != "reader@example.com" || a.body["displayName"] != "quietreader" || a.body["hasPassword"] != true || a.body["avatarUrl"] != nil {
		t.Fatalf("/me: %s", a.raw)
	}

	// The profile update round-trips.
	a = h.do("PUT", "/api/v1/me", req{token: token, body: map[string]any{"displayName": "Night Owl", "avatarUrl": "https://images.example.org/owl.png"}})
	h.expect(a, 200, "")
	a = h.do("GET", "/api/v1/me", req{token: token})
	if a.body["displayName"] != "Night Owl" || a.body["avatarUrl"] != "https://images.example.org/owl.png" {
		t.Fatalf("profile did not round-trip: %s", a.raw)
	}

	// Sign-in works; a wrong password is 400 wrong_password.
	a = h.do("POST", "/api/v1/auth/sign-in", req{body: map[string]any{"email": "READER@example.com", "password": "correct-horse-7"}})
	h.expect(a, 200, "")
	second := a.body["token"].(string)
	h.expect(h.do("POST", "/api/v1/auth/sign-in", req{body: map[string]any{"email": "reader@example.com", "password": "wrong-horse-7"}}), 400, "wrong_password")

	// A password change returns a new token that works and makes the old ones 401.
	h.expect(h.do("PUT", "/api/v1/me/password", req{token: token, body: map[string]any{"currentPassword": "wrong-horse-7", "newPassword": "battery-staple-8"}}), 400, "wrong_password")
	a = h.do("PUT", "/api/v1/me/password", req{token: token, body: map[string]any{"currentPassword": "correct-horse-7", "newPassword": "battery-staple-8"}})
	h.expect(a, 200, "")
	newToken := a.body["token"].(string)
	if newToken == token || newToken == second {
		t.Fatal("the password change did not return a new token")
	}
	h.expect(h.do("GET", "/api/v1/me", req{token: token}), 401, "session_invalid")
	h.expect(h.do("GET", "/api/v1/me", req{token: second}), 401, "session_invalid")
	h.expect(h.do("GET", "/api/v1/me", req{token: newToken}), 200, "")

	// Deletion with a wrong password fails; with the right one it succeeds.
	h.expect(h.do("POST", "/api/v1/me/deletion", req{token: newToken, body: map[string]any{"password": "correct-horse-7"}}), 400, "wrong_password")
	h.expect(h.do("POST", "/api/v1/me/deletion", req{token: newToken, body: map[string]any{"password": nil}}), 400, "wrong_password")
	a = h.do("POST", "/api/v1/me/deletion", req{token: newToken, body: map[string]any{"password": "battery-staple-8"}})
	h.expect(a, 204, "")

	// After that the token is 401, sign-in fails, and nothing of the account is left.
	h.expect(h.do("GET", "/api/v1/me", req{token: newToken}), 401, "session_invalid")
	h.expect(h.do("POST", "/api/v1/auth/sign-in", req{body: map[string]any{"email": "reader@example.com", "password": "battery-staple-8"}}), 400, "wrong_password")
	if n := h.rowsOf(id); n != 0 {
		t.Fatalf("%d rows of the deleted account are left", n)
	}
}

func TestABadTokenIs401EverywherePublicRoutesIncluded(t *testing.T) {
	h := newHarness(t, nil)
	h.signUp("quietreader", "reader@example.com", "correct-horse-7")
	signUp := map[string]any{"displayName": "nightowl", "email": "owl@example.com", "password": "correct-horse-7"}
	signIn := map[string]any{"email": "reader@example.com", "password": "correct-horse-7"}
	for _, auth := range []string{"Bearer not-a-session", "Bearer ", "Basic cmVhZGVyOmhvcnNl", "not-a-scheme"} {
		h.expect(h.do("POST", "/api/v1/auth/sign-up", req{auth: auth, body: signUp}), 401, "session_invalid")
		h.expect(h.do("POST", "/api/v1/auth/sign-in", req{auth: auth, body: signIn}), 401, "session_invalid")
		h.expect(h.do("GET", "/api/v1/me", req{auth: auth}), 401, "session_invalid")
	}
	// The sign-up above never happened.
	h.expect(h.do("POST", "/api/v1/auth/sign-in", req{body: map[string]any{"email": "owl@example.com", "password": "correct-horse-7"}}), 400, "wrong_password")
	// No token where one is required.
	for _, r := range []struct{ method, path string }{{"GET", "/api/v1/me"}, {"PUT", "/api/v1/me"}, {"PUT", "/api/v1/me/password"}, {"POST", "/api/v1/me/deletion"}} {
		a := h.do(r.method, r.path, req{body: map[string]any{}})
		h.expect(a, 401, "session_invalid")
	}
}

func TestValidation(t *testing.T) {
	h := newHarness(t, nil)
	token, _ := h.signUp("quietreader", "reader@example.com", "correct-horse-7")

	a := h.do("POST", "/api/v1/auth/sign-up", req{body: map[string]any{"displayName": "ab", "email": "not-an-email"}})
	h.expect(a, 400, "validation_failed")
	if got := fieldCodes(a); got != "displayName:too_short,email:invalid,password:required" {
		t.Fatalf("fields: %s", got)
	}
	h.expect(h.do("POST", "/api/v1/auth/sign-up", req{body: map[string]any{"displayName": "other", "email": "Reader@Example.com", "password": "correct-horse-7"}}), 409, "email_taken")
	for _, body := range []string{"", "[]", "not json", `{"displayName": 3}`, `{"displayName":"x"} trailing`} {
		h.expect(h.do("POST", "/api/v1/auth/sign-up", req{body: body}), 400, "validation_failed")
	}
	a = h.do("POST", "/api/v1/auth/sign-up", req{body: `{"displayName": 3, "email": null, "password": "correct-horse-7"}`})
	h.expect(a, 400, "validation_failed")
	if got := fieldCodes(a); got != "displayName:invalid,email:required" {
		t.Fatalf("fields: %s", got)
	}
	// Too large a body is still a 400 here: the account routes declare no 413.
	h.expect(h.do("POST", "/api/v1/auth/sign-up", req{body: `{"displayName":"` + strings.Repeat("x", 70<<10) + `"}`}), 400, "validation_failed")
	// A server ignores request fields it does not know.
	h.expect(h.do("POST", "/api/v1/auth/sign-up", req{body: map[string]any{"displayName": "nightowl", "email": "owl@example.com", "password": "correct-horse-7", "userID": 7, "vip": true}}), 201, "")

	h.expect(h.do("POST", "/api/v1/auth/sign-in", req{body: map[string]any{"password": "x"}}), 400, "validation_failed")
	a = h.do("PUT", "/api/v1/me", req{token: token, body: map[string]any{"displayName": "Night Owl", "avatarUrl": "http://images.example.org/owl.png"}})
	h.expect(a, 400, "validation_failed")
	if got := fieldCodes(a); got != "avatarUrl:invalid" {
		t.Fatalf("fields: %s", got)
	}
	// avatarUrl is required (it may be null).
	a = h.do("PUT", "/api/v1/me", req{token: token, body: map[string]any{"displayName": "Night Owl"}})
	h.expect(a, 400, "validation_failed")
	if got := fieldCodes(a); got != "avatarUrl:required" {
		t.Fatalf("fields: %s", got)
	}
	a = h.do("PUT", "/api/v1/me/password", req{token: token, body: map[string]any{"currentPassword": "correct-horse-7", "newPassword": "short"}})
	h.expect(a, 400, "validation_failed")
	if got := fieldCodes(a); got != "newPassword:too_short" {
		t.Fatalf("fields: %s", got)
	}
	// password is required on deletion; only its value may be null.
	h.expect(h.do("POST", "/api/v1/me/deletion", req{token: token, body: map[string]any{}}), 400, "validation_failed")
	h.expect(h.do("GET", "/api/v1/me", req{token: token}), 200, "")
}

func fieldCodes(a answer) string {
	fs, _ := a.body["fields"].([]any)
	var parts []string
	for _, f := range fs {
		m := f.(map[string]any)
		parts = append(parts, m["field"].(string)+":"+m["code"].(string))
	}
	return strings.Join(parts, ",")
}

func TestSignUpClosed(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.SignUpOpen = false })
	h.expect(h.do("POST", "/api/v1/auth/sign-up", req{body: map[string]any{"displayName": "quietreader", "email": "reader@example.com", "password": "correct-horse-7"}}), 403, "sign_up_closed")
	// Accounts made before sign-up closed still sign in.
	if _, err := h.accounts.SignUp(context.Background(), "quietreader", "reader@example.com", "correct-horse-7"); err != nil {
		t.Fatal(err)
	}
	h.expect(h.do("POST", "/api/v1/auth/sign-in", req{body: map[string]any{"email": "reader@example.com", "password": "correct-horse-7"}}), 200, "")
}

func TestAccessKey(t *testing.T) {
	const key = "lantern-access-key-1"
	h := newHarness(t, func(c *config.Config) { c.AccessKey = key })
	pw := "correct-horse-7"
	routes := []struct {
		method, path string
		body         map[string]any
	}{
		{"POST", "/api/v1/auth/sign-up", map[string]any{"displayName": "nightowl", "email": "owl@example.com", "password": pw}},
		{"POST", "/api/v1/auth/sign-in", map[string]any{"email": "reader@example.com", "password": pw}},
		{"GET", "/api/v1/me", nil},
		{"PUT", "/api/v1/me", map[string]any{"displayName": "Night Owl", "avatarUrl": nil}},
		{"PUT", "/api/v1/me/password", map[string]any{"currentPassword": pw, "newPassword": pw}},
		{"POST", "/api/v1/me/deletion", map[string]any{"password": pw}},
	}
	a := h.do("POST", "/api/v1/auth/sign-up", req{accessKey: key, body: map[string]any{"displayName": "quietreader", "email": "reader@example.com", "password": pw}})
	h.expect(a, 201, "")
	token := a.body["token"].(string)
	for _, r := range routes {
		// Missing and wrong keys never sign out, even with a valid token.
		h.expect(h.do(r.method, r.path, req{token: token, body: r.body}), 403, "access_key_required")
		h.expect(h.do(r.method, r.path, req{token: token, accessKey: "lantern-access-key-2", body: r.body}), 403, "access_key_invalid")
		// The key is checked before the token.
		h.expect(h.do(r.method, r.path, req{token: "not-a-session", body: r.body}), 403, "access_key_required")
	}
	h.expect(h.do("GET", "/api/v1/me", req{token: token, accessKey: key}), 200, "")
	h.expect(h.do("GET", "/api/status", req{}), 200, "")
}

func TestAServerWithoutAKeyIgnoresTheHeader(t *testing.T) {
	h := newHarness(t, nil)
	a := h.do("POST", "/api/v1/auth/sign-up", req{accessKey: "anything-at-all-123", body: map[string]any{"displayName": "quietreader", "email": "reader@example.com", "password": "correct-horse-7"}})
	h.expect(a, 201, "")
}

func TestDisabledAccount(t *testing.T) {
	h := newHarness(t, nil)
	token, _ := h.signUp("quietreader", "reader@example.com", "correct-horse-7")
	if err := h.accounts.Disable(context.Background(), "reader@example.com"); err != nil {
		t.Fatal(err)
	}
	h.expect(h.do("GET", "/api/v1/me", req{token: token}), 401, "session_invalid")
	h.expect(h.do("POST", "/api/v1/auth/sign-in", req{body: map[string]any{"email": "reader@example.com", "password": "correct-horse-7"}}), 403, "account_disabled")
	h.expect(h.do("POST", "/api/v1/auth/sign-in", req{body: map[string]any{"email": "reader@example.com", "password": "wrong-horse-7"}}), 400, "wrong_password")
}

func TestRateLimits(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.SignUpsPerHour = 2; c.SignInsPerMinute = 3 })
	token, _ := h.signUp("quietreader", "reader@example.com", "correct-horse-7")
	h.signUp("nightowl", "owl@example.com", "correct-horse-7")
	a := h.do("POST", "/api/v1/auth/sign-up", req{body: map[string]any{"displayName": "thirdone", "email": "third@example.com", "password": "correct-horse-7"}})
	h.expect(a, 429, "rate_limited")
	if ra := a.header.Get("Retry-After"); ra == "" || ra == "0" {
		t.Fatalf("Retry-After = %q", ra)
	}
	// Sign-in, password change and deletion share one budget of password checks.
	h.expect(h.do("POST", "/api/v1/auth/sign-in", req{body: map[string]any{"email": "reader@example.com", "password": "wrong-horse-7"}}), 400, "wrong_password")
	h.expect(h.do("PUT", "/api/v1/me/password", req{token: token, body: map[string]any{"currentPassword": "wrong-horse-7", "newPassword": "battery-staple-8"}}), 400, "wrong_password")
	h.expect(h.do("POST", "/api/v1/me/deletion", req{token: token, body: map[string]any{"password": "wrong-horse-7"}}), 400, "wrong_password")
	h.expect(h.do("POST", "/api/v1/auth/sign-in", req{body: map[string]any{"email": "reader@example.com", "password": "correct-horse-7"}}), 429, "rate_limited")
	h.expect(h.do("PUT", "/api/v1/me/password", req{token: token, body: map[string]any{"currentPassword": "correct-horse-7", "newPassword": "battery-staple-8"}}), 429, "rate_limited")
	h.expect(h.do("POST", "/api/v1/me/deletion", req{token: token, body: map[string]any{"password": "correct-horse-7"}}), 429, "rate_limited")
	// Reads are not limited.
	h.expect(h.do("GET", "/api/v1/me", req{token: token}), 200, "")
}

// S1: the server sees every request from the Docker bridge gateway (here: loopback). With the
// default trusted proxies, each client behind it has its own budget.
func TestClientsBehindTheProxyHaveTheirOwnBudget(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.SignInsPerMinute = 2 })
	wrong := map[string]any{"email": "reader@example.com", "password": "wrong-horse-7"}
	h.expect(h.do("POST", "/api/v1/auth/sign-in", req{xff: "198.51.100.1", body: wrong}), 400, "wrong_password")
	h.expect(h.do("POST", "/api/v1/auth/sign-in", req{xff: "198.51.100.1", body: wrong}), 400, "wrong_password")
	h.expect(h.do("POST", "/api/v1/auth/sign-in", req{xff: "198.51.100.1", body: wrong}), 429, "rate_limited")
	// Someone else behind the same proxy is not blocked.
	h.expect(h.do("POST", "/api/v1/auth/sign-in", req{xff: "198.51.100.2", body: wrong}), 400, "wrong_password")
	// IPv6 clients count per /64.
	h.expect(h.do("POST", "/api/v1/auth/sign-in", req{xff: "2001:db8:1:2::1", body: wrong}), 400, "wrong_password")
	h.expect(h.do("POST", "/api/v1/auth/sign-in", req{xff: "2001:db8:1:2::2", body: wrong}), 400, "wrong_password")
	h.expect(h.do("POST", "/api/v1/auth/sign-in", req{xff: "2001:db8:1:2::3", body: wrong}), 429, "rate_limited")
}

func TestWarnsWhenNothingIsTrustedBehindAPrivatePeer(t *testing.T) {
	var log bytes.Buffer
	logTo = &log
	h := newHarness(t, func(c *config.Config) { c.TrustedProxies = nil })
	logTo = nil
	h.do("POST", "/api/v1/auth/sign-in", req{body: map[string]any{"email": "reader@example.com", "password": "wrong-horse-7"}})
	h.do("POST", "/api/v1/auth/sign-in", req{body: map[string]any{"email": "reader@example.com", "password": "wrong-horse-7"}})
	if n := strings.Count(log.String(), "ABYSS_TRUSTED_PROXIES trusts nothing"); n != 1 {
		t.Fatalf("want one warning naming ABYSS_TRUSTED_PROXIES, got %d:\n%s", n, log.String())
	}
}

// S2: wrong passwords also count per account, whatever the client address.
func TestWrongPasswordsCountPerAccount(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.SignInFailuresPerAccount = 3 })
	token, _ := h.signUp("quietreader", "reader@example.com", "correct-horse-7")
	h.signUp("nightowl", "owl@example.com", "correct-horse-7")
	for i, ip := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"} {
		a := h.do("POST", "/api/v1/auth/sign-in", req{xff: ip, body: map[string]any{"email": "Reader@example.com", "password": "wrong-horse-" + string(rune('0'+i))}})
		h.expect(a, 400, "wrong_password")
	}
	// Even the right password waits now, from a new address too.
	a := h.do("POST", "/api/v1/auth/sign-in", req{xff: "198.51.100.4", body: map[string]any{"email": "reader@example.com", "password": "correct-horse-7"}})
	h.expect(a, 429, "rate_limited")
	// Password checks behind the session count for the account too.
	h.expect(h.do("PUT", "/api/v1/me/password", req{token: token, body: map[string]any{"currentPassword": "correct-horse-7", "newPassword": "battery-staple-8"}}), 429, "rate_limited")
	// Another account is not touched.
	h.expect(h.do("POST", "/api/v1/auth/sign-in", req{xff: "198.51.100.4", body: map[string]any{"email": "owl@example.com", "password": "correct-horse-7"}}), 200, "")
}

// S2: with every hashing slot taken the server answers 503 unavailable, never a wrong password.
func TestBusyHashingAnswers503(t *testing.T) {
	h := newHarness(t, nil)
	h.signUp("quietreader", "reader@example.com", "correct-horse-7")
	release := password.HoldAllSlotsForTest()
	a := h.do("POST", "/api/v1/auth/sign-in", req{body: map[string]any{"email": "reader@example.com", "password": "correct-horse-7"}})
	b := h.do("POST", "/api/v1/auth/sign-up", req{body: map[string]any{"displayName": "nightowl", "email": "owl@example.com", "password": "correct-horse-7"}})
	release()
	for _, x := range []answer{a, b} {
		h.expect(x, 503, "unavailable")
		if x.header.Get("Retry-After") == "" {
			t.Fatal("a 503 for busy hashing says when to come back")
		}
	}
}

// N2: the Bearer scheme is matched case-insensitively.
func TestBearerSchemeIgnoresCase(t *testing.T) {
	h := newHarness(t, nil)
	token, _ := h.signUp("quietreader", "reader@example.com", "correct-horse-7")
	for _, scheme := range []string{"bearer", "BEARER", "BeArEr"} {
		h.expect(h.do("GET", "/api/v1/me", req{auth: scheme + " " + token}), 200, "")
	}
}

// N5: empty fields and passwords over the contract's 1024-character maximum are
// validation_failed, on every route that takes a password.
func TestPasswordFieldsFollowTheContractLimits(t *testing.T) {
	h := newHarness(t, nil)
	token, _ := h.signUp("quietreader", "reader@example.com", "correct-horse-7")
	a := h.do("POST", "/api/v1/auth/sign-in", req{body: map[string]any{"email": "", "password": ""}})
	h.expect(a, 400, "validation_failed")
	if got := fieldCodes(a); got != "email:required,password:required" {
		t.Fatalf("fields: %s", got)
	}
	long := strings.Repeat("p", 1025)
	a = h.do("POST", "/api/v1/auth/sign-in", req{body: map[string]any{"email": strings.Repeat("e", 250) + "@x.example", "password": long}})
	h.expect(a, 400, "validation_failed")
	if got := fieldCodes(a); got != "email:too_long,password:too_long" {
		t.Fatalf("fields: %s", got)
	}
	a = h.do("PUT", "/api/v1/me/password", req{token: token, body: map[string]any{"currentPassword": long, "newPassword": "battery-staple-8"}})
	h.expect(a, 400, "validation_failed")
	if got := fieldCodes(a); got != "currentPassword:too_long" {
		t.Fatalf("fields: %s", got)
	}
	a = h.do("POST", "/api/v1/me/deletion", req{token: token, body: map[string]any{"password": long}})
	h.expect(a, 400, "validation_failed")
	if got := fieldCodes(a); got != "password:too_long" {
		t.Fatalf("fields: %s", got)
	}
	h.expect(h.do("GET", "/api/v1/me", req{token: token}), 200, "")
}

func TestRoutesOutsideTheContractAnswerWithTheErrorBody(t *testing.T) {
	h := newHarness(t, nil)
	for _, r := range []struct {
		method, path string
		status       int
		code         string
	}{
		{"GET", "/api/v1/nope", 404, "not_found"},
		{"GET", "/", 404, "not_found"},
		{"DELETE", "/api/v1/me", 405, "method_not_allowed"},
		{"POST", "/api/status", 405, "method_not_allowed"},
	} {
		a := h.do(r.method, r.path, req{noCheck: true})
		if a.status != r.status || a.code() != r.code || a.body["message"] == "" {
			t.Errorf("%s %s = %d %s; want %d %s", r.method, r.path, a.status, a.raw, r.status, r.code)
		}
	}
}

func TestHealthz(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.AccessKey = "lantern-access-key-1" })
	a := h.do("GET", "/healthz", req{noCheck: true})
	if a.status != 200 || a.body["status"] != "ok" {
		t.Fatalf("/healthz = %d %s", a.status, a.raw)
	}
}

// Every account route and /api/status answered every status this server can give, each one
// held to the contract. The tests above did the requests; this one runs them all again
// in one place and checks the coverage.
func TestEveryAccountRouteAndStatusMatchesTheContract(t *testing.T) {
	c := contracttest.Load(t)
	for _, test := range []func(*testing.T){
		TestStatus, TestStatusAnnouncesTheSettings, TestAccountJourney, TestABadTokenIs401EverywherePublicRoutesIncluded,
		TestValidation, TestSignUpClosed, TestAccessKey, TestDisabledAccount, TestRateLimits,
		TestWrongPasswordsCountPerAccount, TestBusyHashingAnswers503,
	} {
		test(t)
	}
	want := map[string][]string{
		"getStatus":      {"200"},
		"signUp":         {"201", "400 validation_failed", "401 session_invalid", "403 access_key_required", "403 access_key_invalid", "403 sign_up_closed", "409 email_taken", "429 rate_limited"},
		"signIn":         {"200", "400 validation_failed", "400 wrong_password", "401 session_invalid", "403 access_key_required", "403 access_key_invalid", "403 account_disabled", "429 rate_limited"},
		"getMe":          {"200", "401 session_invalid", "403 access_key_required", "403 access_key_invalid"},
		"updateMe":       {"200", "400 validation_failed", "401 session_invalid", "403 access_key_required", "403 access_key_invalid"},
		"changePassword": {"200", "400 validation_failed", "400 wrong_password", "401 session_invalid", "403 access_key_required", "403 access_key_invalid", "429 rate_limited"},
		"deleteAccount":  {"204", "400 validation_failed", "400 wrong_password", "401 session_invalid", "403 access_key_required", "403 access_key_invalid", "429 rate_limited"},
	}
	// The list above is every account operation of the contract, and /api/status.
	var ops []string
	ops = append(ops, c.OperationIDs("/api/status")...)
	ops = append(ops, c.OperationIDs("/api/v1/auth/sign-")...)
	ops = append(ops, "getMe", "updateMe", "changePassword", "deleteAccount")
	for _, op := range ops {
		if _, ok := want[op]; !ok {
			t.Errorf("operation %s is not in the coverage list", op)
		}
	}
	for op, cases := range want {
		for _, cs := range cases {
			status, code, _ := strings.Cut(cs, " ")
			n := 0
			for _, ch := range status {
				n = n*10 + int(ch-'0')
			}
			if !c.Seen(op, n, code) {
				t.Errorf("%s never answered %s", op, cs)
			}
		}
	}
	if t.Failed() {
		t.Logf("seen:\n%s", c.Summary())
	}
}
