// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package errorreport

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

func TestScrubRemovesEmailsAndAddresses(t *testing.T) {
	for in, want := range map[string]string{
		"no account with the email reader@example.com":        "no account with the email [email]",
		"dial tcp 203.0.113.9:443: connection refused":        "dial tcp [ip]:443: connection refused",
		"from 2001:db8::1 and [fe80::1%eth0]:80":              "from [ip] and [[ip]]:80",
		"peer ::ffff:192.0.2.1 closed":                        "peer [ip] closed",
		"ends with 2001:db8::1. Next":                         "ends with [ip]. Next",
		"main.go:42 +0x1d":                                    "main.go:42 +0x1d",
		"at 12:30:05 the import took 1.5s, version 1.2.3":     "at 12:30:05 the import took 1.5s, version 1.2.3",
		"Mixed Reader.Name+tag@Mail.Example.ORG and 10.0.0.1": "Mixed [email] and [ip]",
	} {
		if got := ScrubString(in); got != want {
			t.Errorf("ScrubString(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestScrubEvent(t *testing.T) {
	ev := &sentry.Event{
		Message:    "sign-in for reader@example.com from 198.51.100.7",
		ServerName: "my-laptop.local",
		User:       sentry.User{Email: "reader@example.com", IPAddress: "198.51.100.7", ID: "42", Username: "reader"},
		Request: &sentry.Request{
			URL: "https://reader.example.org/api/v1/auth/sign-in?email=reader@example.com", Method: "POST",
			Data: `{"email":"reader@example.com","password":"hunter2hunter2"}`, Cookies: "s=1",
			Headers: map[string]string{"X-Access-Key": "k", "Authorization": "Bearer t", "X-Forwarded-For": "198.51.100.7"},
			Env:     map[string]string{"REMOTE_ADDR": "198.51.100.7"},
		},
		Exception:   []sentry.Exception{{Type: "*errors.errorString", Value: "user reader@example.com: 198.51.100.7"}},
		Breadcrumbs: []*sentry.Breadcrumb{{Message: "GET /x by reader@example.com"}},
		Tags:        map[string]string{"who": "reader@example.com"},
		Contexts:    map[string]sentry.Context{"log": {"err": "reader@example.com", "nested": map[string]any{"ip": "198.51.100.7"}, "n": 3}},
	}
	got := Scrub(ev)
	if got.Request != nil || !got.User.IsEmpty() || got.Breadcrumbs != nil {
		t.Fatalf("request, user and breadcrumbs must go: %+v %+v %v", got.Request, got.User, got.Breadcrumbs)
	}
	if got.ServerName != serverName {
		t.Errorf("server name %q", got.ServerName)
	}
	all := got.Message + got.Exception[0].Value + got.Tags["who"] + got.Contexts["log"]["err"].(string) +
		got.Contexts["log"]["nested"].(map[string]any)["ip"].(string)
	if strings.Contains(all, "@example.com") || strings.Contains(all, "198.51.100.7") {
		t.Fatalf("not scrubbed: %s", all)
	}
	if got.Contexts["log"]["n"] != 3 {
		t.Error("non-string values stay")
	}
}

// capture is a fake GlitchTip: it keeps every envelope it receives.
type capture struct {
	mu   sync.Mutex
	got  []string
	seen chan struct{}
}

func (c *capture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		body = zr
	}
	b, _ := io.ReadAll(body)
	c.mu.Lock()
	c.got = append(c.got, r.URL.Path+"\n"+string(b))
	c.mu.Unlock()
	select {
	case c.seen <- struct{}{}:
	default:
	}
	w.WriteHeader(200)
}

func TestErrorLogsReachTheDSNScrubbed(t *testing.T) {
	c := &capture{seen: make(chan struct{}, 10)}
	srv := httptest.NewServer(c)
	defer srv.Close()
	dsn := "http://publickey@" + strings.TrimPrefix(srv.URL, "http://") + "/7"

	var out bytes.Buffer
	base := slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelInfo})
	r, err := newReporter(dsn, "1.2.3", base)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(r.Handler)
	log.Info("request", "method", "GET", "path", "/api/v1/titles") // not reported
	log.With("component", "accounts").Error("session lookup", "err", errors.New("user reader@example.com from 203.0.113.9"),
		"stack", "goroutine 1 [running]:\nmain.main()")
	if !r.Flush(5 * time.Second) {
		t.Fatal("flush timed out")
	}
	select {
	case <-c.seen:
	case <-time.After(5 * time.Second):
		t.Fatal("no event reached the DSN")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.got) != 1 {
		t.Fatalf("want one event, got %d", len(c.got))
	}
	payload := c.got[0]
	if !strings.HasPrefix(payload, "/api/7/envelope/") {
		t.Errorf("sent to %q", strings.SplitN(payload, "\n", 2)[0])
	}
	for _, want := range []string{"session lookup", `"component":"accounts"`, "[email]", "[ip]", `"release":"abyssreader-server@1.2.3"`, "goroutine 1"} {
		if !strings.Contains(payload, want) {
			t.Errorf("payload lacks %q:\n%s", want, payload)
		}
	}
	for _, never := range []string{"reader@example.com", "203.0.113.9", `"request"`, `"user"`, `"ip_address"`} {
		if strings.Contains(payload, never) {
			t.Errorf("payload carries %q:\n%s", never, payload)
		}
	}
	// The log line itself still reads as before.
	if !strings.Contains(out.String(), "session lookup") || !strings.Contains(out.String(), "msg=request") {
		t.Errorf("log output: %s", out.String())
	}
}

func TestNoDSNMeansNoReporter(t *testing.T) {
	base := slog.NewTextHandler(io.Discard, nil)
	h, flush, err := Setup("", "dev", base)
	if err != nil || h != base {
		t.Fatalf("%v %v", h, err)
	}
	flush()
	if _, _, err := Setup("not a dsn", "dev", base); err == nil {
		t.Fatal("a bad DSN must fail")
	}
}

func TestHandlerKeepsEnabledAndGroups(t *testing.T) {
	base := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn})
	r, err := newReporter("http://k@127.0.0.1:1/1", "dev", base)
	if err != nil {
		t.Fatal(err)
	}
	if r.Handler.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("Info must stay disabled when the base handler disables it")
	}
	if !r.Handler.Enabled(context.Background(), slog.LevelError) {
		t.Error("Error must be enabled")
	}
	g := r.Handler.WithGroup("g").WithAttrs([]slog.Attr{slog.String("a", "b")})
	if g.(*handler).prefix != "g." {
		t.Errorf("group prefix %q", g.(*handler).prefix)
	}
}
