// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMissingLiveUsesOlderVerifiedGenerationAfterCorruptNewest(t *testing.T) {
	data, db, name := fixture(t)
	ctx := context.Background()
	now := time.Now()
	old, err := Create(ctx, data, db, 4, now)
	if err != nil {
		t.Fatal(err)
	}
	newest, err := Create(ctx, data, db, 4, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(newest, "pages", name)
	if err = os.Remove(bad); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(bad, []byte("damage"), 0600); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(data, "pages", name)
	if err = os.Remove(live); err != nil {
		t.Fatal(err)
	}
	recovered, err := Create(ctx, data, db, 4, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err = Verify(ctx, recovered); err != nil {
		t.Fatal(err)
	}
	a, err := os.Stat(filepath.Join(old, "pages", name))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(filepath.Join(recovered, "pages", name))
	if err != nil || !os.SameFile(a, b) {
		t.Fatal("did not reuse older verified image", err)
	}
}

func TestSiblingPathAndPinRescueMissingImage(t *testing.T) {
	data, db, name := fixture(t)
	ctx := context.Background()
	now := time.Now()
	old, err := CreatePinned(ctx, data, db, 2, now, "test", "source-pin")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(old); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(data, "pages", name)
	if err = os.Remove(live); err != nil {
		t.Fatal(err)
	}
	// A later catalog revision uses the same digest with another extension.
	alias := strings.TrimSuffix(name, ".png") + ".jpg"
	if _, err = db.Exec("UPDATE page_images SET image=?", alias); err != nil {
		t.Fatal(err)
	}
	result, err := Create(ctx, data, db, 2, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err = Verify(ctx, result); err != nil {
		t.Fatal(err)
	}
	a, err := os.Stat(filepath.Join(data, pins, "source-pin", "payload", "pages", name))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(filepath.Join(result, "pages", alias))
	if err != nil || !os.SameFile(a, b) {
		t.Fatal("pin alias was not linked", err)
	}
}

func TestEstimateFindsHistoricalBlobInSiblingFolderWithoutReadingBytes(t *testing.T) {
	data, db, name := fixture(t)
	ctx := context.Background()
	now := time.Now()
	if err := os.Mkdir(filepath.Join(data, "covers"), 0700); err != nil {
		t.Fatal(err)
	}
	cover := filepath.Join(data, "covers", name)
	if err := os.Link(filepath.Join(data, "pages", name), cover); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM page_images; INSERT INTO titles(ref,slug,name,name_key,status,manifest_hash,created_at,updated_at,cover_image) VALUES ('t','t','t','t','unknown','',0,0,?)", name); err != nil {
		t.Fatal(err)
	}
	prior, err := Create(ctx, data, db, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("DELETE FROM titles"); err != nil {
		t.Fatal(err)
	}
	if _, err = Create(ctx, data, db, 3, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(cover); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO page_images VALUES ('page',0,?,1,1)", name); err != nil {
		t.Fatal(err)
	}
	original := openContent
	openContent = func(path string) (io.ReadCloser, error) {
		t.Fatalf("metadata estimate read payload %s", path)
		return nil, nil
	}
	t.Cleanup(func() { openContent = original })
	size, err := Estimate(ctx, data, db, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(prior, "covers", name)); err != nil {
		t.Fatal(err)
	}
	without, err := Estimate(ctx, data, db, false)
	if err != nil || without-size != int64(len("immutable page")) {
		t.Fatalf("did not count recoverable sibling: %d %d %v", size, without, err)
	}
}

func TestDeletionGuardIndependentlyFindsLiveGenerationAndPin(t *testing.T) {
	for _, source := range []string{"live", "generation", "pin", "unowned", "gone"} {
		t.Run(source, func(t *testing.T) {
			data, db, name := fixture(t)
			ctx := context.Background()
			now := time.Now()
			original, err := CreatePinned(ctx, data, db, 2, now, "test", "guard-pin")
			if err != nil {
				t.Fatal(err)
			}
			// Move surviving copies aside before generating the incomplete export.
			saved := t.TempDir()
			gen := filepath.Join(saved, "generation")
			pin := filepath.Join(saved, "pin")
			if err = os.Rename(original, gen); err != nil {
				t.Fatal(err)
			}
			pp := filepath.Join(data, pins, "guard-pin")
			if err = os.Rename(pp, pin); err != nil {
				t.Fatal(err)
			}
			live := filepath.Join(data, "pages", name)
			if err = os.Remove(live); err != nil {
				t.Fatal(err)
			}
			partial, err := Create(ctx, data, db, 2, now.Add(time.Hour))
			var diagnostic *IncompleteError
			if !errors.As(err, &diagnostic) {
				t.Fatalf("need genuine incomplete generation: %v", err)
			}
			err = nil
			switch source {
			case "live":
				err = os.WriteFile(live, []byte("immutable page"), 0600)
			case "generation":
				err = os.Rename(gen, original)
			case "pin":
				err = os.Rename(pin, pp)
			case "unowned":
				err = os.Rename(gen, filepath.Join(data, "backups", "operator-copy"))
			}
			if err != nil {
				t.Fatal(err)
			}
			missing, err := DeletionGuard(ctx, data, filepath.Base(partial))
			if source == "gone" || source == "unowned" {
				if err != nil || len(missing) != 1 || missing[0] != "pages/"+name {
					t.Fatalf("missing=%v err=%v", missing, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("approved deletion despite %s copy: %v %v", source, missing, err)
			}
		})
	}
}

func TestRawExpiresAfterKeepNewerCompleteSnapshots(t *testing.T) {
	data, db, _ := fixture(t)
	ctx := context.Background()
	now := time.Now()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := preserveRaw(ctx, data, now, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = DeletionGuard(ctx, data, filepath.Base(raw)); err == nil {
		t.Fatal("normal guard approved raw archive without full volume proof")
	}
	reopened, err := OpenDB(filepath.Join(data, Database), false)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	// Neither an older normal snapshot nor a newer incomplete one supplies
	// a newer complete retention slot for the raw snapshot.
	if _, err = Create(ctx, data, reopened, 2, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(raw); err != nil {
		t.Fatal("lost raw after one complete", err)
	}
	absent := strings.Repeat("a", 64) + ".png"
	if _, err = reopened.Exec("INSERT INTO page_images VALUES ('page',1,?,1,1)", absent); err != nil {
		t.Fatal(err)
	}
	_, err = Create(ctx, data, reopened, 2, now.Add(2*time.Minute))
	var partial *IncompleteError
	if !errors.As(err, &partial) {
		t.Fatal(err)
	}
	if _, err = os.Lstat(raw); err != nil {
		t.Fatal("incomplete publication expired raw", err)
	}
	if _, err = reopened.Exec("DELETE FROM page_images WHERE image=?", absent); err != nil {
		t.Fatal(err)
	}
	if _, err = Create(ctx, data, reopened, 2, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(raw); !os.IsNotExist(err) {
		t.Fatal("raw survived two newer complete generations", err)
	}
	entries, err := List(ctx, data)
	if err != nil || len(entries) != 2 || entries[0].Incomplete || entries[1].Incomplete {
		t.Fatal(entries, err)
	}
}

func TestRawWithForeignContentSurvivesRetention(t *testing.T) {
	data, db, _ := fixture(t)
	ctx := context.Background()
	now := time.Now()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := preserveRaw(ctx, data, now, "test")
	if err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(raw, "operator-note")
	if err = os.WriteFile(foreign, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err = OpenDB(filepath.Join(data, Database), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = Create(ctx, data, db, 1, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(foreign); err != nil || string(got) != "keep" {
		t.Fatal("foreign entry lost", err)
	}
}

func TestDamagedNewerGenerationDoesNotExpireRawCopy(t *testing.T) {
	data, db, _ := fixture(t)
	ctx := context.Background()
	now := time.Now()
	if _, err := Create(ctx, data, db, 5, now); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := preserveRaw(ctx, data, now.Add(time.Minute), "test")
	if err != nil {
		t.Fatal(err)
	}
	db, err = OpenDB(filepath.Join(data, Database), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first, err := Create(ctx, data, db, 2, now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(raw); err != nil {
		t.Fatal("snapshot before raw incorrectly counted as newer", err)
	}
	if err = os.WriteFile(filepath.Join(first, Database), []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Create(ctx, data, db, 2, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(raw); err != nil {
		t.Fatal("counted a damaged snapshot as a complete backup", err)
	}
	if _, err = Create(ctx, data, db, 2, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(raw); !os.IsNotExist(err) {
		t.Fatal("did not retire raw with two intact later snapshots", err)
	}
}

func TestRawChecksumMismatchPreventsPruning(t *testing.T) {
	data, db, _ := fixture(t)
	ctx := context.Background()
	now := time.Now()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := preserveRaw(ctx, data, now, "test")
	if err != nil {
		t.Fatal(err)
	}
	replaced := filepath.Join(raw, Database)
	if err = os.WriteFile(replaced, []byte("operator bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err = OpenDB(filepath.Join(data, Database), false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = Create(ctx, data, db, 1, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(replaced); err != nil || string(got) != "operator bytes" {
		t.Fatal("removed a modified raw snapshot", err)
	}
}

func TestDamagedReceiptsRescueOnlyVerifiedPrivateBytes(t *testing.T) {
	for _, kind := range []string{"generation", "pin"} {
		t.Run(kind, func(t *testing.T) {
			data, db, name := fixture(t)
			ctx := context.Background()
			now := time.Now()
			gen, err := CreatePinned(ctx, data, db, 2, now, "test", "broken-pin")
			if err != nil {
				t.Fatal(err)
			}
			pin := filepath.Join(data, pins, "broken-pin")
			source := gen
			receipt := filepath.Join(gen, manifestFile)
			if kind == "pin" {
				if err = os.RemoveAll(gen); err != nil {
					t.Fatal(err)
				}
				source = filepath.Join(pin, "payload")
				receipt = filepath.Join(pin, "source")
			} else {
				if err = os.RemoveAll(pin); err != nil {
					t.Fatal(err)
				}
			}
			if err = os.WriteFile(receipt, []byte("broken kit receipt"), 0600); err != nil {
				t.Fatal(err)
			}
			if got, err := List(ctx, data); err != nil || len(got) != 0 {
				t.Fatalf("admitted damaged history: %v %v", got, err)
			}
			if _, ok, err := Latest(data); err != nil || ok {
				t.Fatalf("admitted latest: %v %v", ok, err)
			}
			live := filepath.Join(data, "pages", name)
			if err = os.Remove(live); err != nil {
				t.Fatal(err)
			}
			original := filepath.Join(source, "pages", name)
			aside := filepath.Join(t.TempDir(), name)
			if err = os.Rename(original, aside); err != nil {
				t.Fatal(err)
			}
			without, err := Estimate(ctx, data, db, false)
			if err != nil {
				t.Fatal(err)
			}
			partial, err := Create(ctx, data, db, 2, now.Add(time.Hour))
			var missing *IncompleteError
			if !errors.As(err, &missing) {
				t.Fatalf("expected incomplete fixture, got %v", err)
			}
			if err = os.Rename(aside, original); err != nil {
				t.Fatal(err)
			}
			if err = os.Chmod(original, 0644); err != nil {
				t.Fatal(err)
			}
			with, err := Estimate(ctx, data, db, false)
			if err != nil || with-without != int64(len("immutable page")) {
				t.Fatalf("private salvage estimate: %d %d %v", with, without, err)
			}
			if _, err = DeletionGuard(ctx, data, filepath.Base(partial)); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("deletion approved despite salvage source: %v", err)
			}
			recovered, err := Create(ctx, data, db, 1, now.Add(2*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if err = Verify(ctx, recovered); err != nil {
				t.Fatal(err)
			}
			from, err := os.Stat(original)
			if err != nil {
				t.Fatal(err)
			}
			to, err := os.Stat(filepath.Join(recovered, "pages", name))
			if err != nil || os.SameFile(from, to) || to.Mode().Perm() != 0600 {
				t.Fatalf("did not create independent private copy: %v", err)
			}
			if got, err := os.ReadFile(receipt); err != nil || string(got) != "broken kit receipt" {
				t.Fatal("modified damaged source", err)
			}
		})
	}
}

func TestDamagedReceiptSalvageNeverFollowsLinkedImagesOrFolders(t *testing.T) {
	for _, what := range []string{"image", "folder", "generation"} {
		t.Run(what, func(t *testing.T) {
			data, db, name := fixture(t)
			ctx := context.Background()
			now := time.Now()
			old, err := Create(ctx, data, db, 1, now)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(old, manifestFile), []byte("damaged"), 0600); err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(filepath.Join(data, "pages", name)); err != nil {
				t.Fatal(err)
			}
			hidden := filepath.Join(t.TempDir(), "original")
			move := filepath.Join(old, "pages", name)
			if what == "folder" {
				move = filepath.Dir(move)
			} else if what == "generation" {
				move = old
			}
			if err = os.Rename(move, hidden); err != nil {
				t.Fatal(err)
			}
			if err = os.Symlink(hidden, move); err != nil {
				t.Fatal(err)
			}
			partial, err := Create(ctx, data, db, 1, now.Add(time.Hour))
			var expected *IncompleteError
			if !errors.As(err, &expected) {
				t.Fatalf("linked %s used as a salvage source: %s %v", what, partial, err)
			}
			missing, err := DeletionGuard(ctx, data, filepath.Base(partial))
			if err != nil || len(missing) != 1 {
				t.Fatal(missing, err)
			}
		})
	}
}

func TestHealthyImageSkipsUnreadableDamagedSourceFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires filesystem permission checks for an unprivileged process")
	}
	data, db, _ := fixture(t)
	ctx := context.Background()
	now := time.Now()
	broken, err := Create(ctx, data, db, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(broken, manifestFile), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(broken, "pages")
	if err = os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })
	if _, err = Create(ctx, data, db, 1, now.Add(time.Hour)); err != nil {
		t.Fatalf("visited salvage media despite good live image: %v", err)
	}
}
