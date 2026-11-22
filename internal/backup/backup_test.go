// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

func fixture(t *testing.T) (string, *sql.DB, string) {
	t.Helper()
	d := t.TempDir()
	db, e := store.Open(context.Background(), filepath.Join(d, Database))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	content := []byte("immutable page")
	name := fmt.Sprintf("%x.png", sha256.Sum256(content))
	if e = os.Mkdir(filepath.Join(d, "pages"), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(d, "pages", name), content, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("INSERT INTO page_files VALUES ('page',1,1); INSERT INTO page_images VALUES ('page',0,?,1,1)", name); e != nil {
		t.Fatal(e)
	}
	return d, db, name
}

func TestSnapshotRetentionAndRestore(t *testing.T) {
	d, db, name := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	first, e := Create(ctx, d, db, 2, now)
	if e != nil {
		t.Fatal(e)
	}
	root := filepath.Dir(first)
	foreign := filepath.Join(root, "user-file")
	if e = os.WriteFile(foreign, []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(root, generation(now.Add(-time.Hour)))
	if e = os.Symlink(first, link); e != nil {
		t.Fatal(e)
	}
	second, e := Create(ctx, d, db, 2, now.Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	a, e := os.Stat(filepath.Join(first, "pages", name))
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.Stat(filepath.Join(second, "pages", name))
	if e != nil || !os.SameFile(a, b) || a.Mode().Perm() != 0600 {
		t.Fatalf("image not reused privately: %v", e)
	}
	last, e := Create(ctx, d, db, 2, now.Add(2*time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(first); !os.IsNotExist(e) {
		t.Fatal("oldest not pruned")
	}
	for _, p := range []string{second, last, foreign, link} {
		if _, e = os.Lstat(p); e != nil {
			t.Fatal(e)
		}
	}
	var out bytes.Buffer
	if e = ExportGeneration(ctx, last, &out); e != nil {
		t.Fatal(e)
	}
	dest := t.TempDir()
	if e = Stage(ctx, dest, &out); e != nil {
		t.Fatal(e)
	}
	if e = Apply(dest); e != nil {
		t.Fatal(e)
	}
	restored, e := store.OpenExisting(ctx, filepath.Join(dest, Database))
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	var got string
	if e = restored.QueryRow("SELECT image FROM page_images").Scan(&got); e != nil || got != name {
		t.Fatal(got, e)
	}
	if got, e := os.ReadFile(filepath.Join(dest, "pages", name)); e != nil || string(got) != "immutable page" {
		t.Fatal(e)
	}
}

func TestFailedBackupLeavesHistoryAndSkipsForeignContents(t *testing.T) {
	d, db, name := fixture(t)
	now := time.Now().UTC()
	ctx := context.Background()
	first, e := Create(ctx, d, db, 1, now)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(first, "operator-note"), nil, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Create(ctx, d, db, 1, now.Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(first, "operator-note")); e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(d, "pages", name)); e != nil {
		t.Fatal(e)
	}
	recovered, e := Create(ctx, d, db, 1, now.Add(time.Hour))
	if e != nil {
		t.Fatal(e)
	}
	if e = Verify(ctx, recovered); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(first, "operator-note")); e != nil {
		t.Fatal("foreign generation was pruned", e)
	}
}

func TestRejectCorruptAndForeignBackup(t *testing.T) {
	d, db, name := fixture(t)
	ctx := context.Background()
	dir, e := Create(ctx, d, db, 1, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "pages", name), []byte("bad"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = Verify(ctx, dir); e == nil {
		t.Fatal("accepted damaged image")
	}
	if e = Stage(ctx, d, bytes.NewBufferString("other kit")); e == nil {
		t.Fatal("accepted foreign input")
	}
	if _, e = os.Stat(filepath.Join(d, Database)); e != nil {
		t.Fatal(e)
	}
}

func TestScheduleAcrossRestart(t *testing.T) {
	d, db, _ := fixture(t)
	now := time.Now().UTC()
	for _, s := range []string{"daily", "weekly"} {
		if due, e := Due(d, s, now); e != nil || !due {
			t.Fatal(s, due, e)
		}
	}
	if _, e := Create(context.Background(), d, db, 1, now); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		s    string
		days int
		want bool
	}{{"daily", 0, false}, {"daily", 1, true}, {"weekly", 6, false}, {"weekly", 8, true}, {"off", 90, false}} {
		due, e := Due(d, tc.s, now.Add(time.Duration(tc.days)*24*time.Hour))
		if e != nil || due != tc.want {
			t.Fatal(tc, due, e)
		}
	}
}

func TestCannotReplaceForeignGenerationOrFollowBackupsSymlink(t *testing.T) {
	d, db, _ := fixture(t)
	ctx := context.Background()
	now := time.Now()
	root := filepath.Join(d, "backups")
	foreign := t.TempDir()
	if err := os.Symlink(foreign, root); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, d, db, 1, now); err == nil {
		t.Fatal("followed backups symlink")
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, generation(now)), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, d, db, 1, now); err == nil {
		t.Fatal("replaced a foreign directory")
	}
}

func TestUndoRestoresReplacedDatabaseAndWAL(t *testing.T) {
	src, db, _ := fixture(t)
	ctx := context.Background()
	dir, err := Create(ctx, src, db, 2, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err = ExportGeneration(ctx, dir, &archive); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	for _, name := range []string{Database, Database + "-wal", Database + "-shm"} {
		if err = os.WriteFile(filepath.Join(dest, name), []byte("original "+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err = Stage(ctx, dest, &archive); err != nil {
		t.Fatal(err)
	}
	if err = Apply(dest); err != nil {
		t.Fatal(err)
	}
	if err = Undo(dest); err != nil {
		t.Fatal(err)
	}
	if err = Undo(dest); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{Database, Database + "-wal", Database + "-shm"} {
		got, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil || string(got) != "original "+name {
			t.Fatal(name, err)
		}
	}
}

func TestPublishAfterClockMovesBack(t *testing.T) {
	d, db, _ := fixture(t)
	t1 := time.Now()
	ctx := context.Background()
	first, e := Create(ctx, d, db, 1, t1)
	if e != nil {
		t.Fatal(e)
	}
	next, e := Create(ctx, d, db, 1, t1.Add(-time.Hour))
	if e != nil || next <= first {
		t.Fatal("generation order reversed", e)
	}
	if e = Verify(ctx, next); e != nil {
		t.Fatal(e)
	}
}

func TestApplyRefusesSecondSwap(t *testing.T) {
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
	if err = os.WriteFile(filepath.Join(dest, Database), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Stage(ctx, dest, &ar); err != nil {
		t.Fatal(err)
	}
	if err = Apply(dest); err != nil {
		t.Fatal(err)
	}
	if err = Apply(dest); err == nil {
		t.Fatal("second swap succeeded")
	}
	if err = Undo(dest); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dest, Database))
	if err != nil || string(b) != "original" {
		t.Fatal("lost previous database", err)
	}
}
