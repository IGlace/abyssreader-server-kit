// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/definitions"
)

func TestDefinitionRestoreRefusesRetiredSigningKey(t *testing.T) {
	data, db, _ := fixture(t)
	ctx := t.Context()
	path := definitions.KeyPath(data)
	if err := definitions.EnsureLocalKey(data); err != nil {
		t.Fatal(err)
	}
	signer, err := definitions.ReadSigner(path, "")
	if err != nil {
		t.Fatal(err)
	}
	revocation, err := definitions.SignRevocation(signer, "paper-lantern", 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := definitions.Publish(ctx, db, revocation, revocation.KeyID); err != nil {
		t.Fatal(err)
	}
	gen, err := Create(ctx, data, db, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := ExportGeneration(ctx, gen, &archive); err != nil {
		t.Fatal(err)
	}
	if _, err := definitions.Rotate(ctx, db, path); err != nil {
		t.Fatal(err)
	}
	currentKey, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Preview(ctx, data, "", bytes.NewReader(archive.Bytes()), 13); err == nil || !strings.Contains(err.Error(), "--allow-definition-rollback") {
		t.Fatalf("expected restore preview to refuse a retired key, got %v", err)
	}
	if err := Stage(ctx, data, bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatal(err)
	}
	if err := Apply(data); err == nil || !strings.Contains(err.Error(), "--allow-definition-rollback") {
		t.Fatalf("expected apply to refuse a retired key, got %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(currentKey, got) {
		t.Fatalf("refused restore changed the live key: %v", err)
	}
	liveRegistry, err := definitions.ActiveKey(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if err := Finish(data); err != nil {
		t.Fatal(err)
	}
	preview, err := PreviewKeepingDefinitionIdentity(ctx, data, "", bytes.NewReader(archive.Bytes()), 13)
	if err != nil {
		t.Fatal(err)
	}
	if err := BeginOperation(data); err != nil {
		t.Fatal(err)
	}
	if err := PromotePreview(data, preview); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Apply(data); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(currentKey, got) {
		t.Fatalf("confirmed restore changed the live key: %v", err)
	}
	restored, err := OpenDB(filepath.Join(data, Database), false)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	kept, err := definitions.ActiveKey(ctx, restored)
	if err != nil || kept.KeyID != liveRegistry.KeyID || kept.Endorsement == nil ||
		*kept.Endorsement != *liveRegistry.Endorsement {
		t.Fatalf("confirmed restore did not retain the live key registry: %v", err)
	}
	entries, err := definitions.Manifest(ctx, restored)
	if err != nil || len(entries) != 0 {
		t.Fatalf("confirmed restore published stale definitions: %v", err)
	}
}

func TestDefinitionRestoreChecksCommunitySchemaTables(t *testing.T) {
	data, db, _ := fixture(t)
	if _, err := db.Exec("DROP TABLE content_reports"); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(t.Context(), data, db, 1, time.Now()); err == nil {
		t.Fatal("created a verified backup without a required community table")
	}
	if _, err := os.Stat(filepath.Join(data, Database)); err != nil {
		t.Fatal(err)
	}
}

func TestDefinitionRestoreRechecksVersionFloorAfterPreview(t *testing.T) {
	data, db, _ := fixture(t)
	ctx := t.Context()
	if err := definitions.EnsureLocalKey(data); err != nil {
		t.Fatal(err)
	}
	signer, err := definitions.ReadSigner(definitions.KeyPath(data), "")
	if err != nil {
		t.Fatal(err)
	}
	publish := func(version int64) {
		t.Helper()
		entry, err := definitions.SignRevocation(signer, "paper-lantern", version)
		if err != nil {
			t.Fatal(err)
		}
		if err := definitions.Publish(ctx, db, entry, entry.KeyID); err != nil {
			t.Fatal(err)
		}
	}
	publish(3)
	gen, err := Create(ctx, data, db, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	id, err := Preview(ctx, data, filepath.Base(gen), nil, 13)
	if err != nil {
		t.Fatal(err)
	}
	publish(4)
	if err := BeginOperation(data); err != nil {
		t.Fatal(err)
	}
	if err := PromotePreview(data, id); err != nil {
		t.Fatal(err)
	}
	if err := Apply(data); err == nil || !strings.Contains(err.Error(), "--allow-definition-rollback") {
		t.Fatalf("apply accepted a newly stale version under the same signing key: %v", err)
	}
}
