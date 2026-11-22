// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/backup"
)

func TestCorruptDBPreservedForRestoreAndUninstallOnly(t *testing.T) {
	ctx := context.Background()
	env := envOf(baseEnv(t))
	d := env("ABYSS_DATA_DIR")
	files := map[string]string{backup.Database: "SQLite format 3\x00truncated", backup.Database + "-wal": "wal remains", backup.Database + "-shm": "shm remains", backup.Database + "-journal": "journal remains"}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(d, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var output, diag bytes.Buffer
	call := func(in io.Reader, args ...string) int {
		output.Reset()
		diag.Reset()
		return maintenance(ctx, args, in, &output, &diag, env)
	}
	if code := call(nil, "preflight", "preserve"); code != 0 {
		t.Fatalf("corrupt live database blocked restore preflight: %s", &diag)
	}
	if code := call(nil, "snapshot-preserve", "14", "1.0.0"); code != 0 || !strings.Contains(diag.String(), "raw, unverified") {
		t.Fatalf("salvage failed: %s", &diag)
	}
	gen := strings.TrimSpace(output.String())
	dir := filepath.Join(d, "backups", gen)
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(got) != want {
			t.Fatalf("raw file %s changed: %v", name, err)
		}
	}
	if dsc, err := backup.Description(ctx, dir); err != nil || !strings.Contains(dsc, "raw, unverified") {
		t.Fatalf("description: %s %v", dsc, err)
	}
	if code := adminBackup(ctx, []string{"list"}, &output, &diag, env); code != 0 || !strings.Contains(output.String(), "raw, unverified") {
		t.Fatal("raw generation was not labelled in list")
	}
	output.Reset()
	if status(ctx, &output, &diag, env) == 0 || !strings.Contains(output.String(), "raw, unverified") {
		t.Fatalf("stopped service hid raw preservation status: %s", &output)
	}
	if code := call(nil, "export-preserved", gen); code != 0 {
		t.Fatal(diag.String())
	}
	ar := bytes.Clone(output.Bytes())
	if code := call(bytes.NewReader(ar), "verify-preserved"); code != 0 {
		t.Fatal(diag.String())
	}
	if code := call(bytes.NewReader(ar), "preview", "8"); code == 0 || !strings.Contains(diag.String(), "raw, unverified") {
		t.Fatalf("archive restore accepted raw data or hid the reason: %s", &diag)
	}
	if code := call(nil, "stage-generation", gen, "8"); code == 0 || !strings.Contains(diag.String(), "raw, unverified") {
		t.Fatalf("local restore of raw: %s", &diag)
	}
	if code := call(nil, "snapshot-pin", "14", "1.0.0", "operation.12345678"); code == 0 {
		t.Fatal("raw cannot be used as upgrade pin")
	}
}

func TestDamagedFullPageFileRetainsExistingSidecars(t *testing.T) {
	env := envOf(baseEnv(t))
	ctx := t.Context()
	d := env("ABYSS_DATA_DIR")
	original := map[string][]byte{backup.Database: bytes.Repeat([]byte("not-SQLite"), 512), backup.Database + "-wal": bytes.Repeat([]byte("damaged WAL"), 200), backup.Database + "-shm": bytes.Repeat([]byte("saved SHM"), 200)}
	for file, b := range original {
		if err := os.WriteFile(filepath.Join(d, file), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var gen, warn bytes.Buffer
	if maintenance(ctx, []string{"preflight", "preserve"}, nil, io.Discard, &warn, env) != 0 {
		t.Fatal(warn.String())
	}
	if maintenance(ctx, []string{"snapshot-preserve", "2", "1.0.0"}, nil, &gen, &warn, env) != 0 {
		t.Fatal(warn.String())
	}
	for name, want := range original {
		got, err := os.ReadFile(filepath.Join(d, "backups", strings.TrimSpace(gen.String()), name))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("changed original bytes of %s on corruption detection: %v", name, err)
		}
	}
}
