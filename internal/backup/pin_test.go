// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

func TestPinnedUpgradeRestoresPrivateDatabaseAfterPruning(t *testing.T) {
	data, db, name := fixture(t)
	ctx := context.Background()
	now := time.Now()
	dir, err := Create(ctx, data, db, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	dbStat, err := os.Stat(filepath.Join(dir, Database))
	if err != nil {
		t.Fatal(err)
	}
	imageStat, err := os.Stat(filepath.Join(dir, "pages", name))
	if err != nil {
		t.Fatal(err)
	}
	const pin = "upgrade-test"
	if err = Pin(ctx, data, filepath.Base(dir), pin); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO goose_db_version(version_id,is_applied) VALUES (1000,1)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err = os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err = StagePin(ctx, data, pin, store.MaxVersion()); err != nil {
		t.Fatal(err)
	}
	if err = StagePin(ctx, data, pin, store.MaxVersion()-1); err == nil {
		t.Fatal("staged pin bypassed target schema on retry")
	}
	if err = ResumeApply(data); err != nil {
		t.Fatal(err)
	}
	if err = StagePin(ctx, data, pin, store.MaxVersion()); err != nil {
		t.Fatal(err)
	}
	if err = ResumeApply(data); err != nil {
		t.Fatal(err)
	}
	for _, file := range []struct {
		rel    string
		old    os.FileInfo
		linked bool
	}{{Database, dbStat, false}, {filepath.Join("pages", name), imageStat, true}} {
		got, err := os.Stat(filepath.Join(data, file.rel))
		if err != nil || os.SameFile(got, file.old) != file.linked {
			t.Fatalf("wrong inode reuse for %s: %v", file.rel, err)
		}
	}
	restored, err := store.OpenExisting(ctx, filepath.Join(data, Database))
	if err != nil {
		t.Fatal(err)
	}
	restored.Close()
	if err = Finish(data); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = Unpin(data, pin); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLocalGenerationStagesLinkedImagesAndChecksTargetVersion(t *testing.T) {
	ctx := context.Background()
	data, db, image := fixture(t)
	dir, err := Create(ctx, data, db, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	gen := filepath.Base(dir)
	if err = StageGeneration(ctx, data, gen, store.MaxVersion()-1); err == nil {
		t.Fatal("accepted too-new local generation")
	}
	if err = StageGeneration(ctx, data, gen, store.MaxVersion()); err != nil {
		t.Fatal(err)
	}
	a, err := os.Stat(filepath.Join(dir, "pages", image))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(filepath.Join(data, transaction, "new", "pages", image))
	if err != nil || !os.SameFile(a, b) {
		t.Fatal("local image copied", err)
	}
	if err = Unpin(t.TempDir(), "operation.12345"); err != nil {
		t.Fatal(err)
	}
	if err = Pin(ctx, data, gen, "operation.12345"); err != nil {
		t.Fatal(err)
	}
}
