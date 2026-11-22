// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/pressly/goose/v3"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGenerationReportsActualSchema(t *testing.T) {
	d, db, _ := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// Make the on-disk migration level differ from the creating binary.
	if _, err := db.Exec("INSERT INTO goose_db_version (version_id,is_applied) VALUES (7654,1)"); err != nil {
		t.Fatal(err)
	}
	var applied int64
	if err := db.QueryRow("SELECT max(version_id) FROM goose_db_version WHERE is_applied").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	dir, err := Create(ctx, d, db, 1, now, "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, manifestFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["schemaVersion"] != float64(applied) || fields["kitVersion"] != "1.2.3" {
		t.Fatalf("manifest %v", fields)
	}
	if err = CheckSchema(ctx, dir, applied-1); err == nil {
		t.Fatal("accepted newer schema")
	}
	desc, err := Description(ctx, dir)
	if err != nil || !strings.Contains(desc, now.Format(time.RFC3339)) {
		t.Fatal(desc, err)
	}
	for _, legacy := range []bool{false, true} {
		if legacy {
			delete(fields, "schemaVersion")
			delete(fields, "kitVersion")
		} else {
			fields["schemaVersion"] = applied - 1
		}
		raw, err = json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err = CheckSchema(ctx, dir, applied-1); err == nil {
			t.Fatal("trusted omitted or lowered manifest version")
		}
		if err = Verify(ctx, dir); (err == nil) != legacy {
			t.Fatalf("legacy %t verify %v", legacy, err)
		}
	}
}

func TestDamageFallsBackAndRetainsIncompleteWithoutDisplacingGood(t *testing.T) {
	d, db, name := fixture(t)
	ctx := context.Background()
	now := time.Now()
	first, err := Create(ctx, d, db, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an image damaged in the most recent generation, independent of an older private copy.
	second, err := Create(ctx, d, db, 3, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	prev := filepath.Join(second, "pages", name)
	if err = os.Remove(prev); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(prev, []byte("damage"), 0600); err != nil {
		t.Fatal(err)
	}
	var warnings bytes.Buffer
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&warnings, nil)))
	t.Cleanup(func() { slog.SetDefault(oldLogger) })
	next, err := Create(ctx, d, db, 3, now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("did not recover from live: %v", err)
	}
	if !strings.Contains(warnings.String(), name) {
		t.Fatalf("damaged prior image was not identified: %s", warnings.String())
	}
	if err = Verify(ctx, next); err != nil {
		t.Fatal(err)
	}
	// A new unresolved reference makes the next snapshot incomplete even
	// though every generation still has the original healthy image.
	unavailable := strings.Repeat("e", 64) + ".png"
	if _, err = db.Exec("INSERT INTO page_images VALUES ('page',1,?,1,1)", unavailable); err != nil {
		t.Fatal(err)
	}
	incomplete, err := Create(ctx, d, db, 1, now.Add(3*time.Hour))
	if err == nil || incomplete == "" {
		t.Fatalf("lost incomplete snapshot: %s %v", incomplete, err)
	}
	if err = Verify(ctx, incomplete); err == nil {
		t.Fatal("incomplete generation restorable")
	}
	desc, err := Description(ctx, incomplete)
	if err != nil || !strings.Contains(desc, "incomplete") {
		t.Fatal(desc, err)
	}
	raw, err := os.ReadFile(filepath.Join(incomplete, manifestFile))
	if err != nil || !strings.Contains(string(raw), unavailable) {
		t.Fatal(string(raw), err)
	}
	if due, err := Due(d, "daily", now.Add(3*time.Hour+time.Minute)); err != nil || due {
		t.Fatal("incomplete snapshot retried before its normal interval", err)
	}
	if err = Verify(ctx, first); err != nil {
		t.Fatalf("last complete generation lost: %v", err)
	}
}

func TestClockRollbackCatchupDoesNotFireEveryMinute(t *testing.T) {
	d, db, _ := fixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_, err := Create(ctx, d, db, 2, now.AddDate(2, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if due, err := Due(d, "daily", now); err != nil || !due {
		t.Fatalf("future snapshot suppressed catch-up %v %v", due, err)
	}
	if _, err = Create(ctx, d, db, 2, now); err != nil {
		t.Fatal(err)
	}
	if due, err := Due(d, "daily", now.Add(time.Minute)); err != nil || due {
		t.Fatalf("spurious repeated catch-up %v %v", due, err)
	}
	if due, err := Due(d, "daily", now.Add(24*time.Hour)); err != nil || !due {
		t.Fatalf("next interval suppressed %v %v", due, err)
	}
}

func TestSpaceEstimateIncludesMigrationAndPrivateImages(t *testing.T) {
	d, db, _ := fixture(t)
	ctx := context.Background()
	var pages, size int64
	if err := db.QueryRow("SELECT page_count, page_size FROM pragma_page_count, pragma_page_size").Scan(&pages, &size); err != nil {
		t.Fatal(err)
	}
	first, err := Estimate(ctx, d, db, false)
	if err != nil {
		t.Fatal(err)
	}
	upgrade, err := Estimate(ctx, d, db, true)
	if err != nil {
		t.Fatal(err)
	}
	if upgrade-first != 2*pages*size || first <= pages*size+int64(len("immutable page")) {
		t.Fatalf("wrong snapshot, migration, image or reserve: %d %d", first, upgrade)
	}
	if _, err = Create(ctx, d, db, 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	reused, err := Estimate(ctx, d, db, false)
	if err != nil || first-reused != int64(len("immutable page")) {
		t.Fatalf("hard-link estimate %d %d %v", first, reused, err)
	}
	if err = preflight(ctx, d, db, false, func(string) (int64, error) { return 99, nil }); err == nil || !strings.Contains(err.Error(), "99") || !strings.Contains(err.Error(), fmt.Sprint(reused)) {
		t.Fatal(err)
	}
}

func TestScratchCleanupRequiresOwnedOldMarker(t *testing.T) {
	for _, prefix := range []string{".writing-", ".staging-"} {
		t.Run(prefix, func(t *testing.T) {
			root := t.TempDir()
			old := time.Now().Add(-72 * time.Hour)
			owned, err := NewScratch(root, prefix)
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := NewScratch(root, prefix)
			if err != nil {
				t.Fatal(err)
			}
			foreign := filepath.Join(root, prefix+"mine")
			if err = os.Mkdir(foreign, 0700); err != nil {
				t.Fatal(err)
			}
			trap := filepath.Join(root, prefix+"symlink")
			if err = os.Symlink(owned, trap); err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{filepath.Join(owned, scratchMarker), owned, foreign} {
				if err = os.Chtimes(p, old, old); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = NewScratch(root, prefix); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(owned); !os.IsNotExist(err) {
				t.Fatalf("stale marked directory survived: %v", err)
			}
			for _, p := range []string{fresh, foreign, trap} {
				if _, err = os.Lstat(p); err != nil {
					t.Fatal(fmt.Errorf("removed %s: %w", p, err))
				}
			}
		})
	}
}

func TestSnapshotOlderKitMigrations(t *testing.T) {
	for _, version := range []int64{1, 3, 4} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			ctx := context.Background()
			data := t.TempDir()
			db, err := sql.Open("sqlite", filepath.Join(data, Database))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			g, err := goose.NewProvider(goose.DialectSQLite3, db, os.DirFS("../store/migrations"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = g.UpTo(ctx, version); err != nil {
				t.Fatal(err)
			}
			if err = Preflight(ctx, data, db, true); err != nil {
				t.Fatal(err)
			}
			dir, err := Create(ctx, data, db, 2, time.Now(), "upgrade")
			if err != nil {
				t.Fatal(err)
			}
			if err = CheckSchema(ctx, dir, version); err != nil {
				t.Fatal(err)
			}
			if err = Verify(ctx, dir); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRemoveScratchRequiresMatchingMarker(t *testing.T) {
	root := t.TempDir()
	dir, err := NewScratch(root, ".writing-")
	if err != nil {
		t.Fatal(err)
	}
	renamed := filepath.Join(root, ".writing-else")
	if err = os.Rename(dir, renamed); err != nil {
		t.Fatal(err)
	}
	if err = RemoveScratch(renamed); err == nil {
		t.Fatal("removed scratch with another directory's marker")
	}
	if err = os.Rename(renamed, dir); err != nil {
		t.Fatal(err)
	}
	if err = RemoveScratch(dir); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("scratch retained", err)
	}
	if err = RemoveScratch(dir); err != nil {
		t.Fatal(err)
	}
}

func TestLostLiveAndCorruptBackupImageRetainsOtherData(t *testing.T) {
	data, db, name := fixture(t)
	ctx := context.Background()
	now := time.Now()
	first, err := Create(ctx, data, db, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(first, "pages", name), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(data, "pages", name)); err != nil {
		t.Fatal(err)
	}
	dir, err := Create(ctx, data, db, 2, now.Add(time.Hour))
	if dir == "" || err == nil {
		t.Fatalf("missing image should yield an incomplete generation, got %q %v", dir, err)
	}
	h, err := header(dir)
	if err != nil || !h.Incomplete || len(h.Missing) != 1 || h.Missing[0] != "pages/"+name {
		t.Fatalf("manifest: %+v, %v", h, err)
	}
}

func TestBackupCleansRestoreScratchAfterDowntime(t *testing.T) {
	data, db, _ := fixture(t)
	old := time.Now().Add(-48 * time.Hour)
	scratch, err := NewScratch(data, ".staging-")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(scratch, scratchMarker), scratch} {
		if err = os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = Create(context.Background(), data, db, 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(scratch); !os.IsNotExist(err) {
		t.Fatalf("abandoned restore staging survived a backup: %v", err)
	}
}
