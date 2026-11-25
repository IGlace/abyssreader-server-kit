// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/definitions"
	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

func TestDefinitionKeyBackupAndRestore(t *testing.T) {
	data, db, _ := fixture(t)
	if err := definitions.EnsureLocalKey(data); err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(definitions.KeyPath(data))
	if err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join("secrets", "definition-signing-key")
	backup, err := Create(t.Context(), data, db, 3, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := ExportGeneration(t.Context(), backup, &archive); err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if err := Stage(t.Context(), dest, &archive); err != nil {
		t.Fatal(err)
	}
	if err := Apply(dest); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(dest, rel)
	got, err := os.ReadFile(restored)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("key was not restored: %v", err)
	}
	info, err := os.Stat(restored)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("restored key was not private: %v", err)
	}
}

func TestRestoringLegacyBackupKeepsLocalDefinitionKey(t *testing.T) {
	data, db, _ := fixture(t)
	ctx := t.Context()
	generation, err := Create(ctx, data, db, 3, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := definitions.EnsureLocalKey(data); err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(definitions.KeyPath(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := StageGeneration(ctx, data, filepath.Base(generation), store.MaxVersion()); err != nil {
		t.Fatal(err)
	}
	if err := Apply(data); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(definitions.KeyPath(data))
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("legacy restore changed the live key: %v", err)
	}
}

func TestRawRecoveryArchiveIncludesDefinitionKey(t *testing.T) {
	data, _, _ := fixture(t)
	ctx := t.Context()
	if err := definitions.EnsureLocalKey(data); err != nil {
		t.Fatal(err)
	}
	generation, err := preserveRaw(ctx, data, time.Now(), "test")
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := ExportRecovery(ctx, generation, &archive); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRecoveryArchive(ctx, data, &archive, filepath.Base(generation)); err != nil {
		t.Fatal(err)
	}
	path := definitions.KeyPath(generation)
	if err := os.WriteFile(path, []byte("changed key"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRecovery(ctx, generation); err == nil {
		t.Fatal("raw snapshot accepted a changed signing key")
	}
}
