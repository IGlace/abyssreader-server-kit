// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Model the directory entries left by a process killed immediately before or after each
// rename. Every cut uses an independently staged real database and a mixed old volume.
func TestRestoreAtEveryRenameBoundary(t *testing.T) {
	ctx := context.Background()
	src, db, image := fixture(t)
	gen, err := Create(ctx, src, db, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err = ExportGeneration(ctx, gen, &archive); err != nil {
		t.Fatal(err)
	}
	type step func() error
	run := func(t *testing.T, steps []step, count int) {
		t.Helper()
		for _, do := range steps[:count] {
			if err := do(); err != nil {
				t.Fatal(err)
			}
		}
	}
	check := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, direction := range []string{"resume", "undo-partial-apply", "resume-undo"} {
		cuts := len(liveFiles) + 3
		// The undo has three extra renames, one for each original that existed.
		for cut := 0; cut <= cuts; cut++ {
			t.Run(fmt.Sprintf("%s-%d", direction, cut), func(t *testing.T) {
				data := t.TempDir()
				live := func(n string) string { return filepath.Join(data, n) }
				old := func(n string) string { return filepath.Join(data, transaction, "old", n) }
				next := func(n string) string { return filepath.Join(data, transaction, "new", n) }
				check(t, os.WriteFile(live(Database), []byte("old DB"), 0600))
				check(t, os.WriteFile(live(Database+"-wal"), []byte("old WAL"), 0600))
				check(t, os.Mkdir(live("covers"), 0700))
				check(t, os.WriteFile(live("covers/original"), []byte("old cover"), 0600))
				check(t, Stage(ctx, data, bytes.NewReader(archive.Bytes())))
				check(t, mark(live(transaction), "applying"))
				original := func(n string) bool { return n == Database || n == Database+"-wal" || n == "covers" }
				var apply, undo []step
				for _, name := range liveFiles {
					apply = append(apply, func() error {
						if original(name) {
							return os.Rename(live(name), old(name))
						}
						return os.WriteFile(old("absent-"+name), nil, 0600)
					})
					undo = append(undo, func() error { return os.RemoveAll(live(name)) })
					if original(name) {
						undo = append(undo, func() error { return os.Rename(old(name), live(name)) })
					}
				}
				for _, name := range []string{Database, "covers", "pages"} {
					apply = append(apply, func() error { return os.Rename(next(name), live(name)) })
				}
				if direction == "resume-undo" {
					check(t, ResumeApply(data))
					check(t, mark(live(transaction), "undoing"))
					run(t, undo, cut)
				} else {
					run(t, apply, cut)
					if direction == "resume" {
						check(t, ResumeApply(data))
						check(t, ResumeApply(data))
						got, err := os.ReadFile(live(filepath.Join("pages", image)))
						check(t, err)
						if string(got) != "immutable page" {
							t.Fatal("restored page missing")
						}
					}
				}
				check(t, Undo(data))
				check(t, Undo(data))
				for name, want := range map[string]string{Database: "old DB", Database + "-wal": "old WAL", "covers/original": "old cover"} {
					got, err := os.ReadFile(live(name))
					check(t, err)
					if string(got) != want {
						t.Fatalf("%s=%q, want %q", name, got, want)
					}
				}
				for _, name := range []string{"pages", Database + "-shm", Database + "-journal"} {
					if _, err := os.Lstat(live(name)); !os.IsNotExist(err) {
						t.Fatalf("%s survived rollback (%v)", name, err)
					}
				}
			})
		}
	}
}
