// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/backup"
	"github.com/IGlace/abyssreader-server-kit/internal/config"
)

func TestStartupSkipsFreshBackupButCatchesOverdue(t *testing.T) {
	env := baseEnv(t)
	cfg, err := config.Load(envOf(env))
	if err != nil {
		t.Fatal(err)
	}
	cfg.BackupSchedule = "daily"
	ctx := context.Background()
	db, err := openDatabase(ctx, envOf(env))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := backupIfDue(ctx, cfg, db, log, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "backups")); !os.IsNotExist(err) {
		t.Fatal("fresh startup wrote a backup")
	}
	if _, err = backup.Create(ctx, cfg.DataDir, db, 2, time.Now().Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = backupIfDue(ctx, cfg, db, log, true); err != nil {
		t.Fatal(err)
	}
	if due, err := backup.Due(cfg.DataDir, "daily", time.Now()); err != nil || due {
		t.Fatal("overdue restart missed catch-up", err)
	}
}
