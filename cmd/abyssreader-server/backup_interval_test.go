// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/backup"
	"github.com/IGlace/abyssreader-server-kit/internal/config"
)

func TestTwoDaysOfSchedulerTicksWithPermanentImageDamage(t *testing.T) {
	ctx := context.Background()
	env := envOf(baseEnv(t))
	cfg, err := config.Load(env)
	if err != nil {
		t.Fatal(err)
	}
	cfg.BackupSchedule = "daily"
	cfg.BackupKeep = 3
	db, err := openDatabase(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	start := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	var good []string
	for i := 3; i > 0; i-- {
		generation, e := backup.Create(ctx, cfg.DataDir, db, cfg.BackupKeep, start.Add(-time.Duration(i)*24*time.Hour))
		if e != nil {
			t.Fatal(e)
		}
		good = append(good, generation)
	}
	name := strings.Repeat("b", 64) + ".png"
	if _, err = db.Exec("INSERT INTO page_files VALUES ('page',1,1); INSERT INTO page_images VALUES ('page',0,?,1,1)", name); err != nil {
		t.Fatal(err)
	}
	var logText bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logText, nil))
	for tick := start; tick.Before(start.Add(48 * time.Hour)); tick = tick.Add(time.Minute) {
		err := backupIfDueAt(ctx, cfg, db, logger, tick, false)
		if err != nil {
			var partial *backup.IncompleteError
			if !errors.As(err, &partial) {
				t.Fatal(err)
			}
			generations, e := backup.List(ctx, cfg.DataDir)
			if e != nil || len(generations) != len(good)+1 || !generations[0].Incomplete {
				t.Fatalf("retention after incomplete: %v, %v", generations, e)
			}
		}
	}
	if n := strings.Count(logText.String(), "starting scheduled backup"); n != 2 {
		t.Fatalf("%d scheduled backups in 48h, want 2", n)
	}
	for _, g := range good {
		if err := backup.Verify(ctx, g); err != nil {
			t.Fatalf("lost complete generation %s: %v", filepath.Base(g), err)
		}
	}
}
