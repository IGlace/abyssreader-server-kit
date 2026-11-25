// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Preview fully verifies a private staging tree. The running server ignores the outer
// scratch directory. A killed installer leaves the same time-limited scratch as extraction.
func Preview(ctx context.Context, data, generation string, in io.Reader, maxSchema int64) (string, error) {
	return preview(ctx, data, generation, in, maxSchema, false)
}

// PreviewKeepingDefinitionIdentity explicitly allows an archive from a different
// signing state. The approval follows the staged operation through recovery.
func PreviewKeepingDefinitionIdentity(ctx context.Context, data, generation string, in io.Reader, maxSchema int64) (string, error) {
	return preview(ctx, data, generation, in, maxSchema, true)
}

func preview(ctx context.Context, data, generation string, in io.Reader, maxSchema int64, allow bool) (string, error) {
	if err := Ready(data); err != nil {
		return "", err
	}
	temp, err := NewScratch(data, ".staging-")
	if err != nil {
		return "", err
	}
	if generation == "" {
		err = StageWithLimit(ctx, temp, in, maxSchema)
	} else if !ValidGeneration(generation) {
		err = errors.New("invalid generation name")
	} else {
		root := filepath.Join(data, "backups")
		if err = checkType(root, true); err == nil {
			err = stageLocal(ctx, temp, filepath.Join(root, generation), "", maxSchema)
		}
	}
	tx := filepath.Join(temp, transaction)
	if err == nil {
		_, err = planDefinitionRestore(ctx, data, filepath.Join(tx, "new"), allow)
	}
	if err == nil && allow {
		err = mark(tx, definitionRestoreOverride)
	}
	if err != nil {
		return "", errors.Join(err, RemoveScratch(temp))
	}
	return filepath.Base(temp), nil
}

func previewPath(data, id string) (string, error) {
	if filepath.Base(id) != id || !strings.HasPrefix(id, ".staging-") {
		return "", errors.New("invalid preview name")
	}
	dir := filepath.Join(data, id)
	if err := checkType(dir, true); err != nil {
		return "", err
	}
	owned, err := ownedScratch(dir, time.Time{})
	if err != nil {
		return "", err
	}
	if !owned {
		return "", errors.New("unrecognized restore preview directory")
	}
	if err := checkType(filepath.Join(dir, transaction), true); err != nil {
		return "", err
	}
	return dir, nil
}

func liveModified(data string) (string, error) {
	db, err := os.Lstat(filepath.Join(data, Database))
	if err != nil {
		return "", err
	}
	if !db.Mode().IsRegular() {
		return "", errors.New("invalid live database file")
	}
	result := "Live database modified " + db.ModTime().UTC().Format(time.RFC3339)
	wal, err := os.Lstat(filepath.Join(data, Database+"-wal"))
	if os.IsNotExist(err) {
		return result + " (no WAL file).", nil
	}
	if err != nil {
		return "", err
	}
	if !wal.Mode().IsRegular() {
		return "", errors.New("invalid SQLite WAL file")
	}
	return fmt.Sprintf("%s; WAL modified %s.", result, wal.ModTime().UTC().Format(time.RFC3339)), nil
}

func DescribePreview(ctx context.Context, data, id string) (string, error) {
	dir, err := previewPath(data, id)
	if err != nil {
		return "", err
	}
	archived, err := DescribeStage(ctx, dir)
	if err != nil {
		return "", err
	}
	modified, err := liveModified(data)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Archive: %s.\n%s", archived, modified), nil
}

func PromotePreview(data, id string) error {
	dir, err := previewPath(data, id)
	if err != nil {
		return err
	}
	if err = checkType(filepath.Join(data, operation), false); err != nil {
		return fmt.Errorf("restore operation must be armed before promotion: %w", err)
	}
	if present, err := exists(filepath.Join(data, transaction)); err != nil {
		return err
	} else if present {
		return errors.New("another restore is already staged")
	}
	if err = moveDurably(filepath.Join(dir, transaction), filepath.Join(data, transaction)); err != nil {
		return err
	}
	return RemoveScratch(dir)
}

func DiscardPreview(data, id string) error {
	if filepath.Base(id) != id || !strings.HasPrefix(id, ".staging-") {
		return errors.New("invalid preview name")
	}
	return RemoveScratch(filepath.Join(data, id))
}
