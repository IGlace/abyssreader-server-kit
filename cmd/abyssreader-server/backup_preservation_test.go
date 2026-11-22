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

func TestPreservationSnapshotReportsMissingImagesInList(t *testing.T) {
	ctx := context.Background()
	settings := baseEnv(t)
	env := envOf(settings)
	db, err := openDatabase(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	name := strings.Repeat("a", 64) + ".png"
	if _, err = db.Exec("INSERT INTO page_files VALUES ('page',1,1); INSERT INTO page_images VALUES ('page',0,?,1,1)", name); err != nil {
		t.Fatal(err)
	}
	var output, diagnostic bytes.Buffer
	code := maintenance(ctx, []string{"snapshot-preserve", "14", "1.0.0"}, nil, &output, &diagnostic, env)
	gen := strings.TrimSpace(output.String())
	if code != 0 || !backup.ValidGeneration(gen) || !strings.Contains(diagnostic.String(), "pages/"+name) || !strings.Contains(diagnostic.String(), "/backups/"+gen) || !strings.Contains(strings.ToLower(diagnostic.String()), "incomplete") {
		t.Fatalf("preservation snapshot code=%d generation=%q warning=%q", code, gen, diagnostic.String())
	}
	output.Reset()
	if code = adminBackup(ctx, []string{"list"}, &output, &diagnostic, env); code != 0 || !strings.Contains(output.String(), gen) || !strings.Contains(output.String(), "incomplete") || !strings.Contains(output.String(), "pages/"+name) {
		t.Fatalf("operator cannot see damage: %s %s", &output, &diagnostic)
	}
	// The operator's local status command surfaces damage while the service remains healthy.
	_, stop := serveForTest(t, settings)
	defer stop()
	output.Reset()
	diagnostic.Reset()
	if code = status(ctx, &output, &diagnostic, env); code != 0 || !strings.Contains(output.String(), "incomplete") || !strings.Contains(output.String(), "pages/"+name) {
		t.Fatalf("local status hid the latest backup: %s %s", &output, &diagnostic)
	}
	if code = maintenance(ctx, []string{"snapshot-pin", "14", "1.0.0", "operation.abcdef12"}, nil, io.Discard, io.Discard, env); code == 0 {
		t.Fatal("incomplete generation accepted as upgrade pin")
	}
}

func TestPreservationSnapshotFailsOnStorageError(t *testing.T) {
	ctx := context.Background()
	env := envOf(baseEnv(t))
	db, err := openDatabase(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = os.WriteFile(filepath.Join(env("ABYSS_DATA_DIR"), "backups"), []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	var generation bytes.Buffer
	if code := maintenance(ctx, []string{"snapshot-preserve", "14", "1.0.0"}, nil, &generation, io.Discard, env); code != 1 || generation.Len() != 0 {
		t.Fatalf("storage error did not stop snapshot: %d %s", code, &generation)
	}
}
