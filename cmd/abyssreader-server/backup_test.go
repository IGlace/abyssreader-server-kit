// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/backup"
	"github.com/IGlace/abyssreader-server-kit/internal/config"
)

func TestScheduledBackupWaitsForCatalogLock(t *testing.T) {
	env := baseEnv(t)
	env["ABYSS_BACKUP_SCHEDULE"] = "weekly"
	cfg, err := config.Load(envOf(env))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, err := openDatabase(ctx, envOf(env))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	unlock, err := lockData(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	err = backupIfDue(ctx, cfg, db, log)
	unlock()
	if !errors.Is(err, errLocked) {
		t.Fatalf("scheduler bypassed the catalog lock: %v", err)
	}
	if err = backupIfDue(ctx, cfg, db, log); err != nil {
		t.Fatal(err)
	}
	if due, err := backup.Due(cfg.DataDir, "weekly", time.Now()); err != nil || due {
		t.Fatalf("successful catch-up was not recorded: due=%v err=%v", due, err)
	}
}
