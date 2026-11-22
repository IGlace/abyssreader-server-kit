// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package catalog

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/covers"
)

func TestCoverSweepKeepsReferencedAndUnmanagedFiles(t *testing.T) {
	for _, cmd := range []string{"import", "retry"} {
		t.Run(cmd, func(t *testing.T) {
			f := newFixture(t, nil)
			dir := folder(t, sampleManifest(), map[string][]byte{"covers/lantern.png": pngBytes(t, 30, 45)})
			f.importDir(dir)
			title, err := f.cat.TitleBySlug(t.Context(), "the-lantern-keeper")
			if err != nil || title.CoverImage == "" {
				t.Fatalf("expected imported cover: %+v, %v", title, err)
			}
			coverDir := filepath.Join(f.dataDir, covers.Dir)
			orphan := strings.Repeat("a", 64) + ".png"
			folderName := strings.Repeat("b", 64) + ".png"
			pending := ".incoming-123"
			for _, name := range []string{orphan, pending, "readme.txt"} {
				if err := os.WriteFile(filepath.Join(coverDir, name), []byte("data"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Mkdir(filepath.Join(coverDir, folderName), 0o700); err != nil {
				t.Fatal(err)
			}
			if cmd == "import" {
				f.importDir(dir)
			} else if _, err := f.cat.RetryCovers(t.Context()); err != nil {
				t.Fatal(err)
			}

			for _, name := range []string{orphan, pending} {
				if _, err := os.Lstat(filepath.Join(coverDir, name)); !errors.Is(err, fs.ErrNotExist) {
					t.Errorf("%s should be removed, stat returned %v", name, err)
				}
			}
			for _, name := range []string{title.CoverImage, folderName, "readme.txt"} {
				if _, err := os.Lstat(filepath.Join(coverDir, name)); err != nil {
					t.Errorf("%s must remain: %v", name, err)
				}
			}
		})
	}
}
