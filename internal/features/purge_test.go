// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package features

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

func TestHostedPurgeRefusesLinkedPageDirectory(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(dir, "kit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	covers := filepath.Join(dir, "covers")
	if err := os.Mkdir(covers, 0700); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(covers, strings.Repeat("a", 64)+".png")
	if err := os.WriteFile(name, []byte("cover"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(covers, filepath.Join(dir, "pages")); err != nil {
		t.Fatal(err)
	}
	if _, err := purgeHosted(ctx, db, dir, io.Discard); err == nil {
		t.Fatal("purged through a directory symlink")
	}
	if data, err := os.ReadFile(name); string(data) != "cover" || err != nil {
		t.Fatal("cover lost", err)
	}
}
