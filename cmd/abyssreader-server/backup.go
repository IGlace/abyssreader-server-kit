// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/backup"
	"github.com/IGlace/abyssreader-server-kit/internal/config"
	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

func scheduleBackups(parent context.Context, cfg config.Config, db *sql.DB, log *slog.Logger) func() {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if cfg.BackupSchedule == "off" {
			return
		}
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		for startup := true; ; startup = false {
			err := backupIfDue(ctx, cfg, db, log, startup)
			if err != nil && ctx.Err() == nil {
				var incomplete *backup.IncompleteError
				if errors.As(err, &incomplete) {
					log.Error("scheduled backup incomplete; next attempt at the normal interval", "generation", incomplete.Generation, "missing", incomplete.Missing)
				} else if errors.Is(err, errLocked) {
					log.Info("admin lock busy; retrying scheduled backup in one minute")
				} else {
					log.Error("scheduled backup failed; retrying in one minute", "err", err)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}

func backupIfDue(ctx context.Context, cfg config.Config, db *sql.DB, log *slog.Logger, startup ...bool) error {
	return backupIfDueAt(ctx, cfg, db, log, time.Now(), len(startup) > 0 && startup[0])
}

func backupIfDueAt(ctx context.Context, cfg config.Config, db *sql.DB, log *slog.Logger, now time.Time, startup bool) error {
	due, err := backup.Due(cfg.DataDir, cfg.BackupSchedule, now, startup)
	if err != nil || !due {
		return err
	}
	unlock, err := lockData(cfg.DataDir)
	if err != nil {
		return err
	}
	defer unlock()
	due, err = backup.Due(cfg.DataDir, cfg.BackupSchedule, now, startup)
	if err != nil || !due {
		return err
	}
	log.Info("starting scheduled backup")
	dir, err := backup.Create(ctx, cfg.DataDir, db, cfg.BackupKeep, now, version)
	if err == nil {
		log.Info("scheduled backup complete", "generation", filepath.Base(dir))
	}
	return err
}

func adminBackup(ctx context.Context, args []string, out, stderr io.Writer, getenv func(string) string) int {
	cfg, err := config.Load(getenv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(args) == 1 && args[0] == "list" {
		generations, err := backup.List(ctx, cfg.DataDir)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		for _, gen := range generations {
			state := "complete"
			if gen.Raw {
				state = "raw, unverified; manual salvage only"
			}
			if gen.Incomplete {
				state = "incomplete; missing " + strings.Join(gen.Missing, ", ")
			}
			fmt.Fprintf(out, "%s  %s  %s\n", gen.Name, gen.Created.UTC().Format(time.RFC3339), state)
		}
		return 0
	}
	if len(args) == 1 && args[0] == "now" {
		args = []string{"snapshot", strconv.Itoa(cfg.BackupKeep), version}
	} else if len(args) != 2 || args[0] != "export" {
		fmt.Fprint(stderr, usage)
		return 2
	}
	return maintenance(ctx, args, nil, out, stderr, getenv)
}

// Installer helpers use the stamped image, even when the old or failed image lacks the CLI.
// Snapshotting never runs a migration, including against an older installed database.
func maintenance(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer, getenv func(string) string) int {
	dir := config.DataDir(getenv)
	// Staging is private to the installer lock and does not touch live files.
	// A catalog import during the post-health-check cleanup must not block it.
	var err error
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "snapshot", "snapshot-pin", "snapshot-preserve", "pin", "stage-generation", "preview-generation", "abandon", "export", "export-preserved", "export-volume", "verify-volume", "deletion-guard", "apply", "undo":
		unlock, lockErr := lockData(dir)
		if lockErr != nil {
			fmt.Fprintln(stderr, "admin lock:", lockErr)
			return 1
		}
		defer unlock()
	}
	switch {
	case len(args) == 1 && args[0] == "schema-version":
		fmt.Fprintln(out, store.MaxVersion())
	case len(args) == 2 && args[0] == "preflight" && args[1] == "preserve":
		var raw bool
		raw, err = backup.PreservePreflight(ctx, dir)
		if err == nil && raw {
			fmt.Fprintln(out, "raw")
		}
	case len(args) == 2 && (args[0] == "check-live" || args[0] == "preflight"):
		var db *sql.DB
		db, err = backup.OpenDB(filepath.Join(dir, backup.Database), false)
		if err != nil {
			break
		}
		defer db.Close()
		if args[0] == "check-live" {
			var applied, max int64
			if max, err = schemaNumber(args[1]); err != nil {
				break
			}
			applied, err = store.AppliedVersion(ctx, db)
			if err == nil && applied > max {
				err = fmt.Errorf("database schema %d is newer than the target image supports (%d)", applied, max)
			}
		} else {
			if args[1] != "backup" && args[1] != "upgrade" {
				err = errors.New("invalid preflight operation")
				break
			}
			err = backup.Preflight(ctx, dir, db, args[1] == "upgrade")
		}
	case len(args) == 4 && args[0] == "begin":
		var supported int64
		if args[2] != "0" {
			if supported, err = schemaNumber(args[2]); err != nil {
				break
			}
		}
		pin := args[3]
		if pin == "-" {
			pin = ""
		}
		err = backup.RecordOperation(dir, args[1], supported, pin)
	case len(args) == 1 && args[0] == "describe-unlocked":
		var details string
		details, err = backup.DescribeUnlockedAbandonment(ctx, dir)
		if err == nil {
			fmt.Fprintln(out, details)
		}
	case len(args) == 1 && args[0] == "audit-unlocked":
		err = backup.RecordUnlockedAbandonment(ctx, dir)
	case len(args) == 1 && args[0] == "begin":
		err = backup.BeginOperation(dir)
	case len(args) == 1 && args[0] == "end":
		err = backup.EndOperation(dir)
	case len(args) == 1 && args[0] == "permit":
		var token string
		token, err = backup.PermitBoot(dir)
		if err == nil {
			fmt.Fprintln(out, token)
		}
	case len(args) == 1 && args[0] == "orphan-status":
		var status string
		status, err = backup.OrphanStatus(dir)
		if err == nil {
			fmt.Fprintln(out, status)
		}
	case len(args) == 1 && args[0] == "abandon":
		var kept string
		kept, err = backup.Abandon(ctx, dir)
		if err == nil {
			fmt.Fprintln(out, "Verified the existing live database and released the boot interlock.")
			if kept != "" {
				fmt.Fprintln(out, "Preserved recovery files at", kept)
			}
		}
	case args[0] == "preview", args[0] == "preview-generation":
		allow := args[len(args)-1] == "--allow-definition-rollback"
		if allow {
			args = args[:len(args)-1]
		}
		want := 2
		if args[0] == "preview-generation" {
			want = 3
		}
		if len(args) != want {
			err = errors.New("invalid restore preview arguments")
			break
		}
		var max int64
		if max, err = schemaNumber(args[len(args)-1]); err != nil {
			break
		}
		source := ""
		if args[0] == "preview-generation" {
			if !backup.ValidGeneration(args[1]) {
				err = errors.New("invalid generation name")
				break
			}
			source = args[1]
		}
		var id string
		if allow {
			id, err = backup.PreviewKeepingDefinitionIdentity(ctx, dir, source, in, max)
		} else {
			id, err = backup.Preview(ctx, dir, source, in, max)
		}
		if err == nil {
			fmt.Fprintln(out, id)
		}
	case len(args) == 2 && args[0] == "describe-preview":
		var description string
		description, err = backup.DescribePreview(ctx, dir, args[1])
		if err == nil {
			fmt.Fprintln(out, description)
		}
	case len(args) == 2 && args[0] == "promote-preview":
		err = backup.PromotePreview(dir, args[1])
	case len(args) == 2 && args[0] == "discard-preview":
		err = backup.DiscardPreview(dir, args[1])
	case len(args) == 1 && args[0] == "describe-stage":
		var description string
		description, err = backup.DescribeStage(ctx, dir)
		if err == nil {
			fmt.Fprintln(out, description)
		}
	case len(args) == 3 && args[0] == "pin":
		err = backup.Pin(ctx, dir, args[1], args[2])
	case len(args) == 2 && args[0] == "pin-schema":
		var schema int64
		schema, err = backup.PinImageSchema(dir, args[1])
		if err == nil {
			fmt.Fprintln(out, schema)
		}
	case len(args) == 2 && args[0] == "unpin":
		err = backup.Unpin(dir, args[1])
	case len(args) == 3 && (args[0] == "stage-pin" || args[0] == "stage-generation"):
		var max int64
		if max, err = schemaNumber(args[2]); err != nil {
			break
		}
		if args[0] == "stage-pin" {
			err = backup.StagePin(ctx, dir, args[1], max)
		} else {
			err = backup.StageGeneration(ctx, dir, args[1], max)
		}
	case len(args) == 2 && args[0] == "stage":
		var max int64
		if max, err = schemaNumber(args[1]); err != nil {
			break
		}
		err = backup.StageWithLimit(ctx, dir, in, max)

	case len(args) == 3 && (args[0] == "snapshot" || args[0] == "snapshot-preserve"),
		len(args) == 4 && args[0] == "snapshot-pin":
		err = snapshot(ctx, dir, args, out, stderr)
	case len(args) == 2 && args[0] == "export" && backup.ValidGeneration(args[1]):
		err = backup.ExportGeneration(ctx, filepath.Join(dir, "backups", args[1]), out)
	case len(args) == 2 && args[0] == "describe-generation" && backup.ValidGeneration(args[1]):
		var description string
		description, err = backup.Description(ctx, filepath.Join(dir, "backups", args[1]))
		if err == nil {
			fmt.Fprintln(out, description)
		}
	case len(args) == 2 && args[0] == "deletion-guard" && backup.ValidGeneration(args[1]):
		var missing []string
		missing, err = backup.DeletionGuard(ctx, dir, args[1])
		if err == nil {
			for _, rel := range missing {
				fmt.Fprintln(out, rel)
			}
		}
	case len(args) == 1 && args[0] == "export-volume":
		err = backup.ExportStoppedVolume(ctx, dir, out)
	case len(args) == 1 && args[0] == "verify-volume":
		err = backup.CheckStoppedVolumeArchive(ctx, dir, in)
	case len(args) == 2 && args[0] == "preservation-kind" && backup.ValidGeneration(args[1]):
		var raw bool
		raw, err = backup.IsRawGeneration(filepath.Join(dir, "backups", args[1]))
		if err == nil {
			if raw {
				fmt.Fprintln(out, "raw")
			} else {
				fmt.Fprintln(out, "snapshot")
			}
		}
	case len(args) == 2 && args[0] == "export-preserved" && backup.ValidGeneration(args[1]):
		err = backup.ExportRecovery(ctx, filepath.Join(dir, "backups", args[1]), out)
	case (len(args) == 1 || len(args) == 2) && args[0] == "verify-preserved":
		err = backup.VerifyRecoveryArchive(ctx, dir, in, args[1:]...)
	case len(args) == 1 && args[0] == "apply":
		err = backup.ResumeApply(dir)
	case len(args) == 1 && args[0] == "undo":
		err = backup.Undo(dir)
	case len(args) == 1 && args[0] == "finish":
		err = backup.Finish(dir)
	case len(args) == 1 && args[0] == "ready":
		err = backup.Ready(dir)
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "backup:", err)
		return 1
	}
	return 0
}

// Only a snapshot published with declared image damage is usable for preservation.
func snapshot(ctx context.Context, dir string, args []string, out, stderr io.Writer) error {
	keep, err := strconv.Atoi(args[1])
	if err != nil || keep < 1 || keep > config.MaxBackupKeep {
		return errors.New("invalid backup count")
	}
	var generation string
	if args[0] == "snapshot-preserve" {
		var raw bool
		generation, raw, err = backup.PreserveStopped(ctx, dir, keep, time.Now(), args[2])
		if raw {
			fmt.Fprintf(stderr, "Warning: raw, unverified database and image files preserved at %s/backup.json. Use a verified kit backup to restore.\n", generation)
		}
	} else {
		db, openErr := backup.OpenDB(filepath.Join(dir, backup.Database), false)
		if openErr != nil {
			return openErr
		}
		defer db.Close()
		if args[0] == "snapshot-pin" {
			generation, err = backup.CreatePinned(ctx, dir, db, keep, time.Now(), args[2], args[3])
		} else {
			generation, err = backup.Create(ctx, dir, db, keep, time.Now(), args[2])
		}
	}
	if err != nil {
		var partial *backup.IncompleteError
		if args[0] != "snapshot-preserve" || generation == "" || !errors.As(err, &partial) {
			return err
		}
		fmt.Fprintf(stderr, "Warning: incomplete snapshot at %s/backup.json. Unavailable images: %s\n", generation, strings.Join(partial.Missing, ", "))
	}
	_, err = fmt.Fprintln(out, filepath.Base(generation))
	return err
}

func schemaNumber(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 1 {
		return 0, errors.New("invalid target schema version")
	}
	return n, nil
}
