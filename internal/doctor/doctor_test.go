// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package doctor

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/backup"
	"github.com/IGlace/abyssreader-server-kit/internal/config"
)

func TestDoctorFindsWrongDNSFullDiskAndMissingBackup(t *testing.T) {
	want := netip.MustParseAddr("192.0.2.1")
	checks := Checks{
		Lookup: func(_ context.Context, _ string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("192.0.2.2")}, nil
		},
		Available: func(_ string) (int64, error) { return 0, nil },
		Backups:   func(_ context.Context, _ string) ([]backup.Generation, error) { return nil, nil },
		Images:    func(_ context.Context, _ string) ([]string, error) { return nil, nil },
		Client:    &http.Client{Transport: responseTransport{http.StatusOK, "application/json", "{}"}},
	}
	results := Inspect(context.Background(), config.Config{PublicURL: "https://reader.example.org"}, []netip.Addr{want}, time.Now(), checks)
	for _, check := range []string{"DNS", "Disk", "Backups"} {
		found := false
		for _, result := range results {
			if result.Name == check && result.Failed {
				found = true
			}
		}
		if !found {
			t.Errorf("%s should fail: %+v", check, results)
		}
	}
}

func TestDoctorChecksTLSStatusAndImagesWithoutCredentials(t *testing.T) {
	imgPath := "/covers/" + strings.Repeat("a", 64) + ".png"
	paths := make(chan string, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.Path
		if r.Header.Get("X-Access-Key") != "" || r.Header.Get("Authorization") != "" {
			t.Error("setup checks sent credentials")
		}
		switch r.URL.Path {
		case "/api/status":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, "{}")
		case imgPath:
			w.Header().Set("Content-Type", "image/png")
			fmt.Fprint(w, "image")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	now := time.Now()
	checks := Checks{
		Lookup: func(_ context.Context, _ string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		},
		Available: func(_ string) (int64, error) { return backup.SpaceReserve, nil },
		Backups: func(_ context.Context, _ string) ([]backup.Generation, error) {
			return []backup.Generation{{Created: now}}, nil
		},
		Images: func(_ context.Context, _ string) ([]string, error) { return []string{imgPath}, nil },
		Client: server.Client(),
	}
	cfg := config.Config{PublicURL: server.URL, AccessKey: "keep-on-the-server"}
	results := Inspect(context.Background(), cfg, nil, now, checks)
	for _, r := range results {
		if r.Failed {
			t.Errorf("setup check failed: %+v", r)
		}
	}
	if len(paths) != 2 || <-paths != "/api/status" || <-paths != imgPath {
		t.Fatal("expected one status read and one image request")
	}
}

type responseTransport struct {
	status int
	mime   string
	body   string
}

func (r responseTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: r.status,
		TLS:        &tls.ConnectionState{},
		Header:     http.Header{"Content-Type": []string{r.mime}},
		Body:       io.NopCloser(strings.NewReader(r.body)),
	}, nil
}
