// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const hostedExample = "../../examples/manifests/sample-hosted"

type pageList struct {
	Items []struct {
		Index    int    `json:"index"`
		ImageURL string `json:"imageUrl"`
		Width    int    `json:"width"`
		Height   int    `json:"height"`
	} `json:"items"`
}

// The example hosted title, imported twice into a running server with hosted-chapters on: its
// long strip is read as slices, every page image loads, and removing the title takes its
// page files with it.
func TestImportTheHostedExampleReadItThenRemoveIt(t *testing.T) {
	env := baseEnv(t)
	env["ABYSS_FEATURES"] = "hosted-chapters"
	base, stop := serveForTest(t, env)
	defer stop()

	code, out := admin_(t, env, "import", hostedExample)
	if code != 0 || !strings.Contains(out, "Titles: 1 added") || !strings.Contains(out, "Hosted chapters: 3 added, 0 updated, 0 unchanged, 0 removed, 0 failed") ||
		!strings.Contains(out, "Page files: 4 stored, 0 already stored") {
		t.Fatalf("first import = %d:\n%s", code, out)
	}
	_, title := getJSON(t, base+"/api/v1/titles/by-slug/the-night-ferry?countView=false")
	var details struct {
		Sources []struct {
			ID          int64  `json:"id"`
			SourceSlug  string `json:"sourceSlug"`
			URL         string `json:"url"`
			ReadingMode string `json:"readingMode"`
		} `json:"sources"`
	}
	json.Unmarshal(title, &details)
	if len(details.Sources) != 1 || details.Sources[0].SourceSlug != "hosted" || details.Sources[0].ReadingMode != "long_strip" {
		t.Fatalf("title: %s", title)
	}
	status, chapters := getJSON(t, fmt.Sprintf("%s/api/v1/source-titles/%d/chapters?order=asc", base, details.Sources[0].ID))
	var list struct {
		Items []struct {
			ID        int64 `json:"id"`
			PageCount int   `json:"pageCount"`
		} `json:"items"`
	}
	json.Unmarshal(chapters, &list)
	if status != 200 || len(list.Items) != 3 || list.Items[0].PageCount != 2 || list.Items[1].PageCount != 2 || list.Items[2].PageCount != 1 {
		t.Fatalf("chapters %d: %s", status, chapters)
	}
	// Episode 2 is one 800×9000 strip, served as two slices of 800×4500.
	_, b := getJSON(t, fmt.Sprintf("%s/api/v1/chapters/%d/pages", base, list.Items[1].ID))
	var strip pageList
	json.Unmarshal(b, &strip)
	if len(strip.Items) != 2 {
		t.Fatalf("the strip: %s", b)
	}
	for _, p := range strip.Items {
		if p.Width != 800 || p.Height != 4500 || !strings.HasPrefix(p.ImageURL, "https://lantern.example.org/pages/") {
			t.Fatalf("slice: %+v", p)
		}
		status, img := getJSON(t, base+strings.TrimPrefix(p.ImageURL, "https://lantern.example.org"))
		cfg, err := png.DecodeConfig(bytes.NewReader(img))
		if status != 200 || err != nil || cfg.Width != 800 || cfg.Height != 4500 {
			t.Fatalf("slice image %d: %v %+v", status, err, cfg)
		}
	}

	code, out = admin_(t, env, "import", hostedExample)
	if code != 0 || !strings.Contains(out, "Titles: 0 added, 0 updated, 1 unchanged") ||
		!strings.Contains(out, "Hosted chapters: 0 added, 0 updated, 3 unchanged") || !strings.Contains(out, "Page files: 0 stored, 4 already stored") {
		t.Fatalf("second import = %d:\n%s", code, out)
	}
	if _, again := getJSON(t, fmt.Sprintf("%s/api/v1/chapters/%d/pages", base, list.Items[1].ID)); !bytes.Equal(again, b) {
		t.Fatal("the second import changed the pages")
	}

	pagesDir := filepath.Join(env["ABYSS_DATA_DIR"], "pages")
	if files, _ := os.ReadDir(pagesDir); len(files) != 5 { // 3 pages + 2 slices
		t.Fatalf("%d stored page images", len(files))
	}
	code, out = admin_(t, env, "titles", "remove", "night-ferry")
	if code != 0 || !strings.Contains(out, "with its source titles and hosted chapters") {
		t.Fatalf("remove = %d: %s", code, out)
	}
	if files, _ := os.ReadDir(pagesDir); len(files) != 0 {
		t.Fatalf("%d page images left after the removal", len(files))
	}
	if status, _ := getJSON(t, fmt.Sprintf("%s/api/v1/chapters/%d/pages", base, list.Items[1].ID)); status != 404 {
		t.Fatalf("pages of a removed title: %d", status)
	}
}

// A page outside the limits fails its chapter, with a report line, and exit code 1.
func TestImportReportsAChapterOutsideTheLimits(t *testing.T) {
	env := baseEnv(t)
	_, stop := serveForTest(t, env) // makes the database
	stop()

	dir := t.TempDir()
	var wide bytes.Buffer
	png.Encode(&wide, image.NewGray(image.Rect(0, 0, 2400, 100)))
	os.WriteFile(filepath.Join(dir, "wide.png"), wide.Bytes(), 0o644)
	os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(`{"manifestVersion":1,"titles":[{"ref":"w","name":"W",
		"hosted":[{"lang":"en","chapters":[{"ref":"1","pages":["wide.png"]}]}]}]}`), 0o644)
	code, out := admin_(t, env, "import", dir)
	if code != 1 || !strings.Contains(out, "Hosted chapters: 0 added, 0 updated, 0 unchanged, 0 removed, 1 failed") ||
		!strings.Contains(out, "  w/en/1: page 1, wide.png: 2400×100 pixels: a page is at most 2000 px wide; scale it down before the import") {
		t.Fatalf("import = %d:\n%s", code, out)
	}
}
