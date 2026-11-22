// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

func TestOrphanDetailsAndAuditForEachOperationKind(t *testing.T) {
	for _, kind := range []string{"upgrade", "restore", "rollback", "uninstall"} {
		t.Run(kind, func(t *testing.T) {
			data, db, img := fixture(t)
			now := time.Now()
			ctx := context.Background()
			gen, err := Create(ctx, data, db, 1, now)
			if err != nil {
				t.Fatal(err)
			}
			db.Close()
			if err = os.Remove(filepath.Join(data, "pages", img)); err != nil {
				t.Fatal(err)
			}
			const pin = "operation.abcdefgh"
			if err = RecordOperation(data, kind, 12, pin); err != nil {
				t.Fatal(err)
			}
			status, err := OrphanStatus(data)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"Operation: " + kind, "Applied schema: " + strconv.FormatInt(store.MaxVersion(), 10), "Pinned image supports schema: 12", "/data/.kit-pins/" + pin, "/data/backups/" + filepath.Base(gen) + "/backup.json", "pages/" + img, "Abandoning this " + kind} {
				if !strings.Contains(status, want) {
					t.Errorf("missing %q in %s", want, status)
				}
			}
			if _, err = Abandon(ctx, data); err != nil {
				t.Fatal(err)
			}
			status, err = OrphanStatus(data)
			if err != nil || !strings.Contains(status, "No interrupted operation") {
				t.Fatal(status, err)
			}
			note, err := LastAbandonment(data)
			if err != nil || !strings.Contains(note, kind) || !strings.Contains(note, time.Now().UTC().Format("2006-01-02")) || !strings.Contains(note, "/data/.kit-pins/"+pin) || !strings.Contains(status, note) {
				t.Fatal(note, status, err)
			}
		})
	}
}

func TestOrphanInspectsSavedImagesAndSplitDatabaseBeforeReassembly(t *testing.T) {
	ctx := context.Background()
	data, db, img := fixture(t)
	gen, err := Create(ctx, data, db, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err = StageGeneration(ctx, data, filepath.Base(gen), store.MaxVersion()); err != nil {
		t.Fatal(err)
	}
	if err = BeginOperation(data); err != nil {
		t.Fatal(err)
	}
	tx := filepath.Join(data, transaction)
	for _, n := range []string{Database, "pages"} {
		if err = os.Rename(filepath.Join(data, n), filepath.Join(tx, "old", n)); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.WriteFile(filepath.Join(tx, "old", "pages", img), []byte("corrupt original"), 0600); err != nil {
		t.Fatal(err)
	}
	status, err := OrphanStatus(data)
	if err != nil || !strings.Contains(status, "pages/"+img) || !strings.Contains(status, "checksum") {
		t.Fatal(status, err)
	}
	if _, err = os.Stat(filepath.Join(data, Database)); !os.IsNotExist(err) {
		t.Fatal("inspection changed live DB")
	}
	if _, err = Abandon(ctx, data); err != nil {
		t.Fatal(err)
	}
}

func TestOrphanReadsAppliedVersionFromWALAcrossRename(t *testing.T) {
	ctx := context.Background()
	data, db, _ := fixture(t)
	gen, err := Create(ctx, data, db, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO goose_db_version(version_id,is_applied) VALUES (99999,1)"); err != nil {
		t.Fatal(err)
	}
	// Copy a quiescent live DB and WAL separately, just as a stopped/killed server
	// left them, without closing the writer and checkpointing away the fixture.
	orphan := t.TempDir()
	var archive bytes.Buffer
	if err = ExportGeneration(ctx, gen, &archive); err != nil {
		t.Fatal(err)
	}
	if err = Stage(ctx, orphan, &archive); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{Database, Database + "-wal"} {
		raw, err := os.ReadFile(filepath.Join(data, name))
		if err != nil || len(raw) == 0 {
			t.Fatal(name, err)
		}
		dest := filepath.Join(orphan, name)
		if name == Database {
			dest = filepath.Join(orphan, transaction, "old", name)
		}
		if err = os.WriteFile(dest, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	status, err := OrphanStatus(orphan)
	if err != nil || !strings.Contains(status, "Applied schema: 99999") {
		t.Fatal(status, err)
	}
}

func TestOrphanOffersNewestVerifiedCompleteGeneration(t *testing.T) {
	data, db, _ := fixture(t)
	ctx := context.Background()
	now := time.Now()
	first, err := Create(ctx, data, db, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Create(ctx, data, db, 3, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(second, Database), []byte("corrupt generation with complete manifest"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = BeginOperation(data); err != nil {
		t.Fatal(err)
	}
	status, err := OrphanStatus(data)
	if err != nil || !strings.Contains(status, "Newest complete generation: /data/backups/"+filepath.Base(first)+"/backup.json") {
		t.Fatal(status, err)
	}
}

func TestOrphanInspectionAndConfirmationPreserveOriginalSQLiteSidecars(t *testing.T) {
	source, db, _ := fixture(t)
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE orphan_probe(x)"); err != nil {
		t.Fatal(err)
	}
	orphan := t.TempDir()
	originals := map[string][]byte{}
	for _, name := range []string{Database, Database + "-wal", Database + "-shm"} {
		raw, err := os.ReadFile(filepath.Join(source, name))
		if err != nil {
			t.Fatal(err)
		}
		if name == Database+"-shm" {
			for i := range raw {
				raw[i] = 0x5d
			}
		}
		originals[name] = raw
		if err = os.WriteFile(filepath.Join(orphan, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := BeginOperation(orphan); err != nil {
		t.Fatal(err)
	}
	if status, err := OrphanStatus(orphan); err != nil || !strings.Contains(status, "Applied schema: "+strconv.FormatInt(store.MaxVersion(), 10)) {
		t.Fatal(status, err)
	}
	if _, err := Abandon(context.Background(), orphan); err != nil {
		t.Fatal(err)
	}
	for name, want := range originals {
		got, err := os.ReadFile(filepath.Join(orphan, name))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("recovery modified the original %s (%v)", name, err)
		}
	}
}

func TestUpgradePinRemembersSourceImageSchema(t *testing.T) {
	data, db, _ := fixture(t)
	ctx := context.Background()
	gen, err := Create(ctx, data, db, 2, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	const id = "operation.abcdefgh"
	if err = RecordOperation(data, "upgrade", 8, id); err != nil {
		t.Fatal(err)
	}
	pending, err := OrphanStatus(data)
	if err != nil || !strings.Contains(pending, "planned") {
		t.Fatal(pending, err)
	}
	if err = Pin(ctx, data, filepath.Base(gen), id); err != nil {
		t.Fatal(err)
	}
	if err = EndOperation(data); err != nil {
		t.Fatal(err)
	}
	old, err := PinImageSchema(data, id)
	if err != nil || old != 8 {
		t.Fatal(old, err)
	}
	if other, err := PinImageSchema(data, "operation.ijklmnop"); err != nil || other != 0 {
		t.Fatal(other, err)
	}
	if err = RecordOperation(data, "restore", old, id); err != nil {
		t.Fatal(err)
	}
	report, err := OrphanStatus(data)
	if err != nil || !strings.Contains(report, "Pinned image supports schema: 8") {
		t.Fatal(report, err)
	}
}

func TestUninstallCanRecordUnknownLegacyImageSchema(t *testing.T) {
	data, db, _ := fixture(t)
	db.Close()
	if err := RecordOperation(data, "uninstall", 0, ""); err != nil {
		t.Fatal(err)
	}
	report, err := OrphanStatus(data)
	if err != nil || !strings.Contains(report, "Pinned image supports schema: unknown (installed image did not report its schema)") {
		t.Fatal(report, err)
	}
}

func TestAbandonPartialSwapPreservesDamagedImageEntryWithoutFollowingIt(t *testing.T) {
	ctx := context.Background()
	data, db, img := fixture(t)
	gen, err := Create(ctx, data, db, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err = StageGeneration(ctx, data, filepath.Base(gen), store.MaxVersion()); err != nil {
		t.Fatal(err)
	}
	foreign := t.TempDir()
	if err = os.Symlink(foreign, filepath.Join(data, transaction, "old", "pages")); err != nil {
		t.Fatal(err)
	}
	status, err := OrphanStatus(data)
	if err != nil || !strings.Contains(status, "pages/"+img+" (unreadable or unexpected file type)") {
		t.Fatal(status, err)
	}
	keep, err := Abandon(ctx, data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(keep, "displaced", "pages", img)); err != nil {
		t.Fatal(err)
	}
	if link, err := os.Readlink(filepath.Join(keep, "old", "pages")); err != nil || link != foreign {
		t.Fatal(link, err)
	}
	entries, err := os.ReadDir(foreign)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
}
