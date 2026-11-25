// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/backup"
	"github.com/IGlace/abyssreader-server-kit/internal/manifest"
	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

func TestSampleFeedImportsIntoKit(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "abyssreader-server")
	if out, err := exec.Command("go", "build", "-o", binary, "../../cmd/abyssreader-server").CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, out)
	}
	site := httptest.NewServer(http.FileServer(http.Dir("sample-site")))
	defer site.Close()
	data := filepath.Join(root, "data")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), filepath.Join(data, backup.Database))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ABYSS_DATA_DIR", data)
	t.Setenv("ABYSS_PUBLIC_URL", "http://127.0.0.1")
	output := filepath.Join(root, "feed")
	for _, expected := range []string{"2 added, 0 updated, 0 unchanged", "0 added, 0 updated, 2 unchanged"} {
		var log strings.Builder
		if err := feed(context.Background(), site.URL+"/catalog.json", output, binary, &log); err != nil {
			t.Fatalf("run feed: %v\n%s", err, &log)
		}
		if !strings.Contains(log.String(), expected) {
			t.Fatalf("wanted %q in import output: %s", expected, &log)
		}
	}
	if _, err := manifest.Load(output); err != nil {
		t.Fatalf("generated manifest: %v", err)
	}
	if info, err := os.Stat(filepath.Join(output, "manifest.json")); err != nil || info.Size() == 0 {
		t.Fatalf("missing generated manifest: %v", err)
	}
}
