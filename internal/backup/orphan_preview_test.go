// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/store"
	"github.com/pressly/goose/v3"
)

func TestPreviewShowsLiveAndArchiveWithoutBlockingRestart(t *testing.T) {
	data, db, _ := fixture(t)
	ctx := context.Background()
	dir, err := Create(ctx, data, db, 1, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err = ExportGeneration(ctx, dir, &archive); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"", filepath.Base(dir)} {
		id, err := Preview(ctx, data, source, &archive, store.MaxVersion())
		if err != nil {
			t.Fatal(err)
		}
		detail, err := DescribePreview(ctx, data, id)
		if err != nil || !strings.Contains(detail, "Archive:") || !strings.Contains(detail, "Live database") || !strings.Contains(detail, "ago") {
			t.Fatal(detail, err)
		}
		if err = CheckBoot(data, ""); err != nil {
			t.Fatal("preview blocked unattended restart", err)
		}
		if source == "" {
			if err = DiscardPreview(data, id); err != nil {
				t.Fatal(err)
			}
		} else {
			if err = BeginOperation(data); err != nil {
				t.Fatal(err)
			}
			if err = PromotePreview(data, id); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(filepath.Join(data, transaction, "new", Database)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestOrphanAbandonPreservesBothSidesOfInterruptedSwap(t *testing.T) {
	ctx := context.Background()
	data, db, _ := fixture(t)
	dir, err := Create(ctx, data, db, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err = BeginOperation(data); err != nil {
		t.Fatal(err)
	}
	if err = StageGeneration(ctx, data, filepath.Base(dir), store.MaxVersion()); err != nil {
		t.Fatal(err)
	}
	tx := filepath.Join(data, transaction)
	if err = os.Rename(filepath.Join(data, Database), filepath.Join(tx, "old", Database)); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(tx, "new", Database), filepath.Join(data, Database)); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.OpenExisting(ctx, filepath.Join(data, Database))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recovered.Exec("CREATE TABLE candidate_write(x)"); err != nil {
		t.Fatal(err)
	}
	recovered.Close()
	status, err := OrphanStatus(data)
	if err != nil || !strings.Contains(status, transaction) {
		t.Fatal(status, err)
	}
	kept, err := Abandon(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	if err = CheckBoot(data, ""); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		base string
		want int
	}{{data, 0}, {filepath.Join(kept, "displaced"), 1}} {
		db, err := OpenDB(filepath.Join(tt.base, Database), true)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		err = db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name='candidate_write'").Scan(&n)
		db.Close()
		if err != nil || n != tt.want {
			t.Fatal(tt.base, n, err)
		}
	}
}

func TestOrphanRefusesToReleaseAbsentOrInvalidLiveDatabase(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		d := t.TempDir()
		if err := BeginOperation(d); err != nil {
			t.Fatal(err)
		}
		if invalid {
			if err := os.WriteFile(filepath.Join(d, Database), []byte("foreign files"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := Abandon(context.Background(), d); err == nil {
			t.Fatal("released invalid volume")
		}
		if err := CheckBoot(d, ""); err == nil {
			t.Fatal("removed boot interlock")
		}
	}
}

func TestAbandonValidHistoricalDatabaseAndReportSymlinkedImages(t *testing.T) {
	ctx := context.Background()
	for _, version := range []int64{1, 2, 3, 4} {
		data := t.TempDir()
		db, err := sql.Open("sqlite", filepath.Join(data, Database))
		if err != nil {
			t.Fatal(err)
		}
		p, err := goose.NewProvider(goose.DialectSQLite3, db, os.DirFS("../store/migrations"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = p.UpTo(ctx, version); err != nil {
			t.Fatal(err)
		}
		db.Close()
		if err = BeginOperation(data); err != nil {
			t.Fatal(err)
		}
		if _, err = Abandon(ctx, data); err != nil {
			t.Errorf("schema %d: %v", version, err)
		}
	}
	data, db, img := fixture(t)
	db.Close()
	saved := filepath.Join(data, "retained-images")
	if err := os.Rename(filepath.Join(data, "pages"), saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(saved, filepath.Join(data, "pages")); err != nil {
		t.Fatal(err)
	}
	if err := BeginOperation(data); err != nil {
		t.Fatal(err)
	}
	status, err := OrphanStatus(data)
	if err != nil || !strings.Contains(status, "pages/"+img) || !strings.Contains(status, "file type") {
		t.Fatal(status, err)
	}
	if _, err = Abandon(ctx, data); err != nil {
		t.Fatal("media damage prevented the confirmed abandonment", err)
	}
	if _, err := os.Stat(filepath.Join(saved, img)); err != nil {
		t.Fatal(err)
	}
}

func TestAbandonReassemblesOldWhenAppliedLiveFileIsCorrupt(t *testing.T) {
	ctx := context.Background()
	data, db, _ := fixture(t)
	gen, err := Create(ctx, data, db, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err = BeginOperation(data); err != nil {
		t.Fatal(err)
	}
	if err = StageGeneration(ctx, data, filepath.Base(gen), store.MaxVersion()); err != nil {
		t.Fatal(err)
	}
	if err = ResumeApply(data); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(data, Database), []byte("corrupt candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	retained, err := Abandon(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(retained, "displaced", Database))
	if err != nil || string(got) != "corrupt candidate" {
		t.Fatal(string(got), err)
	}
	if err = verifyLive(ctx, data); err != nil {
		t.Fatal(err)
	}
}

func TestPromoteRefusesUnownedPreviewTree(t *testing.T) {
	data := t.TempDir()
	const id = ".staging-operator"
	if err := os.MkdirAll(filepath.Join(data, id, transaction), 0700); err != nil {
		t.Fatal(err)
	}
	if err := BeginOperation(data); err != nil {
		t.Fatal(err)
	}
	if err := PromotePreview(data, id); err == nil {
		t.Fatal("accepted a foreign staging directory")
	}
	if _, err := os.Stat(filepath.Join(data, transaction)); !os.IsNotExist(err) {
		t.Fatal("moved a foreign transaction", err)
	}
}
