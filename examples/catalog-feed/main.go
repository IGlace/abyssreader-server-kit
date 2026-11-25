// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// The catalog-feed example reads an invented site's catalog and imports it into a local kit.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/manifest"
)

func main() {
	url := flag.String("catalog-url", "http://127.0.0.1:8095/catalog.json", "sample catalog URL")
	dir := flag.String("manifest-dir", "import/catalog-feed", "directory for the generated import manifest")
	kit := flag.String("kit", "abyssreader-server", "path to the local kit binary")
	flag.Parse()
	if err := feed(context.Background(), *url, *dir, *kit, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

type sampleCatalog struct {
	Series []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Path  string `json:"path"`
	} `json:"series"`
}

func feed(ctx context.Context, url, dir, kit string, log io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := http.Client{Timeout: 10 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("read sample catalog: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("read sample catalog: HTTP %d", res.StatusCode)
	}
	const max = 1 << 20
	body, err := io.ReadAll(io.LimitReader(res.Body, max+1))
	if err != nil {
		return err
	}
	if len(body) > max {
		return fmt.Errorf("sample catalog exceeds 1 MiB")
	}
	var sample sampleCatalog
	if err := json.Unmarshal(body, &sample); err != nil {
		return err
	}
	var titles []map[string]any
	for _, series := range sample.Series {
		titles = append(titles, map[string]any{
			"ref": "ink-harbor-" + series.ID, "name": series.Title,
			"sourceTitles": []map[string]string{{"source": "ink-harbor", "url": series.Path}},
		})
	}
	doc := map[string]any{
		"$comment":        "SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0. Copyright 2026 Abyss Reader.",
		"manifestVersion": 1,
		"sources": []map[string]string{{
			"slug": "ink-harbor", "name": "Ink Harbor", "website": "https://ink-harbor.example.org", "lang": "en",
		}},
		"titles": titles,
	}
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if _, err := manifest.Parse(b); err != nil {
		return fmt.Errorf("generated manifest: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".manifest-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(append(b, '\n'))
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(dir, "manifest.json")); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, kit, "admin", "import", dir)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("kit import: %w", err)
	}
	return nil
}
