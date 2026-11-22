// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/covers"
)

func admin_(t *testing.T, env map[string]string, args ...string) (int, string) {
	t.Helper()
	var out bytes.Buffer
	code := run(context.Background(), append([]string{"admin"}, args...), &out, &out, envOf(env))
	return code, out.String()
}

func getJSON(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

const example = "../../examples/manifests/sample-catalog"

func TestImportTheExampleTwiceThenRemoveATitle(t *testing.T) {
	env := baseEnv(t)
	base, stop := serveForTest(t, env)
	defer stop()

	code, out := admin_(t, env, "import", example)
	if code != 0 || !strings.Contains(out, "Titles: 6 added, 0 updated, 0 unchanged, 0 failed") ||
		!strings.Contains(out, "Sources: 2 added") || !strings.Contains(out, "Covers: 5 stored") {
		t.Fatalf("first import = %d:\n%s", code, out)
	}
	_, first := getJSON(t, base+"/api/v1/titles?sort=alphabetical&size=100")
	var page struct {
		Items []struct {
			Slug     string  `json:"slug"`
			CoverURL *string `json:"coverUrl"`
		} `json:"items"`
		TotalItems int `json:"totalItems"`
	}
	json.Unmarshal(first, &page)
	if page.TotalItems != 6 || page.Items[0].Slug != "paper-cranes" || page.Items[0].CoverURL == nil {
		t.Fatalf("catalog: %s", first)
	}
	// The cover is served from the server's own storage, under ABYSS_PUBLIC_URL.
	cover := *page.Items[0].CoverURL
	if !strings.HasPrefix(cover, "https://lantern.example.org/covers/") {
		t.Fatalf("cover URL: %s", cover)
	}
	if status, b := getJSON(t, base+strings.TrimPrefix(cover, "https://lantern.example.org")); status != 200 || !bytes.HasPrefix(b, []byte("\x89PNG")) {
		t.Fatalf("cover: %d", status)
	}

	code, out = admin_(t, env, "import", example)
	if code != 0 || !strings.Contains(out, "Titles: 0 added, 0 updated, 6 unchanged, 0 failed") || !strings.Contains(out, "Covers: 0 stored, 5 unchanged") {
		t.Fatalf("second import = %d:\n%s", code, out)
	}
	if _, second := getJSON(t, base+"/api/v1/titles?sort=alphabetical&size=100"); !bytes.Equal(first, second) {
		t.Fatalf("the second import changed the catalog:\n%s\n%s", first, second)
	}

	code, out = admin_(t, env, "titles", "remove", "paper-cranes")
	if code != 0 || !strings.Contains(out, `Removed "A Thousand Paper Cranes Later"`) {
		t.Fatalf("remove = %d: %s", code, out)
	}
	if status, _ := getJSON(t, base+"/api/v1/titles/by-slug/paper-cranes"); status != 404 {
		t.Fatalf("removed title: %d", status)
	}
	if code, out = admin_(t, env, "titles", "remove", "paper-cranes"); code != 1 || !strings.Contains(out, "No title with the ref paper-cranes") {
		t.Fatalf("remove twice = %d: %s", code, out)
	}
	// An import never deletes, and brings a removed title back.
	if code, out = admin_(t, env, "import", example); code != 0 || !strings.Contains(out, "Titles: 1 added, 0 updated, 5 unchanged") {
		t.Fatalf("import after remove = %d: %s", code, out)
	}
}

func TestCoversRetryCommand(t *testing.T) {
	var ready atomic.Bool
	var cover bytes.Buffer
	png.Encode(&cover, image.NewGray(image.Rect(0, 0, 20, 30)))
	host := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() {
			http.Error(w, "later", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(cover.Bytes())
	}))
	defer host.Close()
	// The real fetcher never reaches a loopback address; this test's stands in for a public host.
	saved := coverFetchOptions
	coverFetchOptions = covers.FetchOptions{AllowPrivateAddresses: true, RootCAs: host.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}
	defer func() { coverFetchOptions = saved }()

	env := baseEnv(t)
	_, stop := serveForTest(t, env)
	stop()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"manifestVersion":1,
		"sources":[{"slug":"paper-moon","name":"Paper Moon","website":"https://papermoon.example.com","lang":"en"}],
		"titles":[{"ref":"tidewater","name":"Tidewater","coverUrl":"`+host.URL+`/tidewater.png",
		"sourceTitles":[{"source":"paper-moon","url":"/series/tidewater/"}]}]}`), 0o644)

	code, out := admin_(t, env, "import", dir)
	if code != 0 || !strings.Contains(out, "Covers: 0 stored, 0 unchanged, 1 failed") || !strings.Contains(out, "tidewater: "+host.URL+"/tidewater.png: the server answered 503") ||
		!strings.Contains(out, "admin covers retry") {
		t.Fatalf("import = %d:\n%s", code, out)
	}
	if code, out = admin_(t, env, "covers", "retry"); code != 1 || !strings.Contains(out, "0 stored, 1 still failing") {
		t.Fatalf("retry while failing = %d:\n%s", code, out)
	}
	ready.Store(true)
	if code, out = admin_(t, env, "covers", "retry"); code != 0 || !strings.Contains(out, "1 stored, 0 still failing") {
		t.Fatalf("retry = %d:\n%s", code, out)
	}
	if code, out = admin_(t, env, "covers", "retry"); code != 0 || !strings.Contains(out, "No cover to retry") {
		t.Fatalf("nothing left = %d:\n%s", code, out)
	}
}

func TestImportRefusesABadManifest(t *testing.T) {
	env := baseEnv(t)
	_, stop := serveForTest(t, env)
	stop()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"manifestVersion":1,"titles":[{"ref":"x","name":"X","sourceTitles":[]}]}`), 0o644)
	if code, out := admin_(t, env, "import", dir); code != 1 || !strings.Contains(out, "at /titles/0/sourceTitles") {
		t.Fatalf("bad manifest = %d: %s", code, out)
	}
	// A title that fails is exit 1; the others are imported.
	os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"manifestVersion":1,"titles":[
		{"ref":"x","name":"X","sourceTitles":[{"source":"nowhere","url":"/x"}]}]}`), 0o644)
	if code, out := admin_(t, env, "import", dir); code != 1 || !strings.Contains(out, `x: source "nowhere"`) {
		t.Fatalf("failed title = %d: %s", code, out)
	}
	if code, out := admin_(t, env, "import", filepath.Join(dir, "missing")); code != 1 || !strings.Contains(out, "manifest.json") {
		t.Fatalf("missing folder = %d: %s", code, out)
	}
}

func TestCatalogAdminUsageErrors(t *testing.T) {
	env := baseEnv(t)
	for _, args := range [][]string{{"import"}, {"import", "a", "b"}, {"titles", "remove"}, {"titles", "delete", "x"}, {"covers"}, {"covers", "sweep"}} {
		if code, _ := admin_(t, env, args...); code != 2 {
			t.Errorf("admin %v = %d, want 2", args, code)
		}
	}
}
