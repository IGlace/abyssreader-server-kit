// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStageChecksFreeSpaceBeforeReadingBody(t *testing.T) {
	dest := t.TempDir()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	if err := w.WriteHeader(&tar.Header{Name: Database, Size: 1 << 60, Mode: 0600}); err != nil {
		t.Fatal(err)
	}
	err := Stage(context.Background(), dest, &b)
	if err == nil || !strings.Contains(err.Error(), "space") {
		t.Fatalf("expected free space check before reading body, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, transaction)); !os.IsNotExist(err) {
		t.Fatal("published invalid stage")
	}
}

func TestRepairPartialApplyAfterMovingDatabase(t *testing.T) {
	src, db, _ := fixture(t)
	ctx := context.Background()
	dir, err := Create(ctx, src, db, 2, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var ar bytes.Buffer
	if err = ExportGeneration(ctx, dir, &ar); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err = os.WriteFile(filepath.Join(dest, Database), []byte("rollback copy"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Stage(ctx, dest, &ar); err != nil {
		t.Fatal(err)
	}
	tx := filepath.Join(dest, transaction)
	if err = os.WriteFile(filepath.Join(tx, "applying"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(dest, Database), filepath.Join(tx, "old", Database)); err != nil {
		t.Fatal(err)
	}
	if err = Apply(dest); err != nil {
		t.Fatalf("resume crashed Apply: %v", err)
	}
	if err = Undo(dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, Database))
	if err != nil || string(got) != "rollback copy" {
		t.Fatal(string(got), err)
	}
}
