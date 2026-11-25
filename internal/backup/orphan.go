// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

func OrphanStatus(data string) (string, error) {
	active, err := pending(data)
	if err != nil {
		return "", err
	}
	audit, err := LastAbandonment(data)
	if err != nil {
		audit = "Warning: could not read the previous abandonment audit."
	}
	if !active {
		status := "No interrupted operation is pending."
		if audit != "" {
			status += "\n" + audit
		}
		return status, nil
	}
	ctx := context.Background()
	r, err := operationDetails(data)
	if err != nil {
		return "", err
	}
	lines, err := describeOperation(ctx, data, r)
	if err != nil {
		return "", err
	}
	if audit != "" {
		lines = append(lines, audit)
	}
	for _, name := range []string{operation, transaction, Database, filepath.Join(transaction, "old", Database), filepath.Join(transaction, "new", Database), filepath.Join(transaction, "applying"), filepath.Join(transaction, "applied"), filepath.Join(transaction, "undoing"), filepath.Join(transaction, "undone")} {
		present, err := exists(filepath.Join(data, name))
		if err != nil {
			return "", err
		}
		if present {
			lines = append(lines, "Present: /data/"+name)
		}
	}
	return appendInspection(ctx, data, lines)
}

func DescribeUnlockedAbandonment(ctx context.Context, data string) (string, error) {
	if err := Ready(data); err != nil {
		return "", err
	}
	lines, err := describeOperation(ctx, data, operationRecord{})
	if err != nil {
		return "", err
	}
	return appendInspection(ctx, data, lines)
}

func appendInspection(ctx context.Context, data string, lines []string) (string, error) {
	details, err := inspectOrphan(ctx, data)
	if err != nil {
		lines = append(lines, "Applied schema: unavailable. Inspection failed: "+err.Error())
	} else {
		lines = append(lines, details...)
	}
	return strings.Join(lines, "\n"), nil
}

var errChangingView = errors.New("database changed during inspection; repeat with the server stopped")

// Read through private SQLite names: mode=ro alone still writes the SHM index.
// A hot journal must recover against private copies of its DB and sidecars.
func orphanDatabase(ctx context.Context, data, tx string, original, media bool) (schema int64, paths []string, err error) {
	tmp, err := NewScratch(data, ".staging-")
	if err != nil {
		return 0, nil, err
	}
	defer func() { err = errors.Join(err, RemoveScratch(tmp)) }()
	type entry struct {
		name, source string
		info         os.FileInfo
	}
	var files []entry
	journal := false
	var copyBytes int64
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		name := Database + suffix
		source := filepath.Join(data, name)
		if original {
			source, err = originalPath(data, tx, name)
			if err != nil {
				return 0, nil, err
			}
			if source == "" && suffix != "" {
				continue
			}
		}
		info, statErr := os.Lstat(source)
		if os.IsNotExist(statErr) && suffix != "" {
			if suffix != "-shm" {
				files = append(files, entry{name, source, nil})
			}
			continue
		}
		if statErr != nil {
			return 0, nil, statErr
		}
		if !info.Mode().IsRegular() {
			return 0, nil, fmt.Errorf("unexpected file type: %s", source)
		}
		if suffix == "-shm" {
			continue
		}
		if suffix == "-journal" {
			journal = true
		}
		files = append(files, entry{name, source, info})
		copyBytes += info.Size()
	}
	mode := "ro"
	if journal {
		free, spaceErr := Available(tmp)
		if spaceErr != nil {
			return 0, nil, spaceErr
		}
		if copyBytes > free-SpaceReserve {
			return 0, nil, errors.New("insufficient free space to inspect the private rollback-journal copy")
		}
		mode = "rw"
	}
	// A plain inspect can run while a permitted candidate is still serving. Never
	// report a mixture of revisions spanning an active writer's checkpoint.
	defer func() {
		for _, f := range files {
			now, statErr := os.Lstat(f.source)
			if f.info == nil && os.IsNotExist(statErr) {
				continue
			}
			if statErr != nil || f.info == nil || !os.SameFile(now, f.info) || now.Size() != f.info.Size() || !now.ModTime().Equal(f.info.ModTime()) {
				schema, paths, err = 0, nil, errChangingView
				return
			}
		}
	}()
	for _, f := range files {
		if f.info == nil {
			continue
		}
		dest := filepath.Join(tmp, f.name)
		if journal {
			err = copyImage(f.source, dest)
		} else {
			err = os.Link(f.source, dest)
		}
		if err != nil {
			return 0, nil, err
		}
	}
	uri := url.URL{Scheme: "file", Path: filepath.Join(tmp, Database), RawQuery: "mode=" + mode + "&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return 0, nil, err
	}
	defer db.Close()
	var check string
	if err = db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&check); err != nil {
		return 0, nil, err
	}
	if check != "ok" {
		return 0, nil, fmt.Errorf("live database failed integrity_check: %s", check)
	}
	schema, err = store.AppliedVersion(ctx, db)
	if err != nil {
		return 0, nil, err
	}
	if schema < 1 {
		return 0, nil, errors.New("live file is not a migrated kit database")
	}
	if err = verifyTables(ctx, db, schema); err != nil {
		return 0, nil, err
	}
	if media {
		paths, err = imagePaths(ctx, db, schema)
		if err != nil {
			return schema, nil, err
		}
	}
	return schema, paths, nil
}

func liveSchema(ctx context.Context, data string) (int64, error) {
	schema, _, err := orphanDatabase(ctx, data, "", false, false)
	return schema, err
}

func verifyLive(ctx context.Context, data string) error {
	_, err := liveSchema(ctx, data)
	return err
}

// If an original has moved, old/ contains it or its absence marker. Every other
// original is still live or has already been put back by an interrupted undo.
func originalPath(data, tx, name string) (string, error) {
	old := filepath.Join(tx, "old")
	saved := filepath.Join(old, name)
	hasOld, err := exists(saved)
	if err != nil {
		return "", err
	}
	absent, err := exists(filepath.Join(old, "absent-"+name))
	if err != nil {
		return "", err
	}
	if absent && hasOld {
		return "", fmt.Errorf("conflicting recovery records for %s", name)
	}
	if absent {
		return "", nil
	}
	if hasOld {
		return saved, nil
	}
	return filepath.Join(data, name), nil
}

func needsOriginal(ctx context.Context, data, tx string) (bool, error) {
	if err := checkType(tx, true); err != nil {
		return false, err
	}
	if err := checkType(filepath.Join(tx, "old"), true); err != nil {
		return false, err
	}
	if _, err := header(filepath.Join(tx, "new")); err != nil {
		return false, fmt.Errorf("unrecognized restore transaction: %w", err)
	}
	for _, name := range []string{"undone", "abandon-reassembled", "abandon-original", "undoing", "applied"} {
		present, err := exists(filepath.Join(tx, name))
		if err != nil {
			return false, err
		}
		if !present {
			continue
		}
		switch name {
		case "undone", "abandon-reassembled":
			return false, nil
		case "abandon-original", "undoing":
			return true, nil
		case "applied":
			if err = verifyLive(ctx, data); err == nil {
				return false, nil
			}
			if errors.Is(err, errChangingView) || store.IsBusy(err) {
				return false, err
			}
			present, e := exists(filepath.Join(tx, "old", Database))
			if e != nil {
				return false, e
			}
			if !present {
				return false, fmt.Errorf("no saved predecessor for damaged live database: %w", err)
			}
			return true, nil
		}
	}
	return true, nil
}

// Use a private read-only SQLite view when the DB and its WAL straddle a rename.
// SQLite rebuilds its own SHM index here. No live or saved name is changed.
func inspectOrphan(ctx context.Context, data string) ([]string, error) {
	tx := filepath.Join(data, transaction)
	hasTx, err := exists(tx)
	if err != nil {
		return nil, err
	}
	original := false
	if hasTx {
		original, err = needsOriginal(ctx, data, tx)
		if err != nil {
			return nil, err
		}
	}
	schema, paths, err := orphanDatabase(ctx, data, tx, original, true)
	if err != nil && schema == 0 {
		return nil, err
	}
	lines := []string{fmt.Sprintf("Applied schema: %d", schema)}
	if err != nil {
		return append(lines, "Warning: could not enumerate referenced images: "+err.Error()), nil
	}
	for _, rel := range paths {
		folder, name := filepath.Split(rel)
		folder = filepath.Clean(folder)
		source := filepath.Join(data, folder)
		if original {
			source, err = originalPath(data, tx, folder)
			if err != nil {
				return nil, err
			}
		}
		reason := ""
		if source == "" {
			reason = "missing"
		} else {
			err = checkType(source, true)
			if err == nil {
				var sum string
				sum, err = digest(filepath.Join(source, name))
				if err == nil && sum != name[:64] {
					reason = "checksum mismatch"
				}
			}
			if os.IsNotExist(err) {
				reason = "missing"
			} else if err != nil {
				reason = "unreadable or unexpected file type"
			}
		}
		if reason != "" {
			lines = append(lines, fmt.Sprintf("Warning: unavailable image %s (%s)", rel, reason))
		}
	}
	return lines, nil
}

// Reassemble originals after a partial swap or undo. Preserve each displaced live entry
// in the transaction, even when it contains writes from a candidate server.
func reassemble(tx, data string) error {
	displaced := filepath.Join(tx, "displaced")
	if err := os.Mkdir(displaced, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	if err := checkType(displaced, true); err != nil {
		return err
	}
	if err := syncPath(tx); err != nil {
		return err
	}
	old := filepath.Join(tx, "old")
	for _, name := range liveFiles {
		saved := filepath.Join(old, name)
		hasOld, err := exists(saved)
		if err != nil {
			return err
		}
		absent, err := exists(filepath.Join(old, "absent-"+name))
		if err != nil {
			return err
		}
		if !hasOld && !absent {
			continue
		}
		if hasOld && absent {
			return fmt.Errorf("conflicting recovery records for %s", name)
		}
		restoreSaved := hasOld
		if hasOld {
			info, err := os.Lstat(saved)
			if err != nil {
				return err
			}
			if name == "covers" || name == "pages" {
				// Retain damaged directories and symlinks under old/ for salvage.
				// Never install an unsafe link as a live image store.
				restoreSaved = info.IsDir()
			} else if !info.Mode().IsRegular() {
				return fmt.Errorf("unexpected file type: %s", saved)
			}
		}
		if absent {
			if err = checkType(filepath.Join(old, "absent-"+name), false); err != nil {
				return err
			}
		}
		live := restoreLivePath(data, name)
		hasLive, err := exists(live)
		if err != nil {
			return err
		}
		if hasLive {
			keep := filepath.Join(displaced, name)
			occupied, err := exists(keep)
			if err != nil {
				return err
			}
			if occupied {
				return fmt.Errorf("multiple displaced copies of %s; preserve the volume for manual recovery", name)
			}
			if err = moveDurably(live, keep); err != nil {
				return err
			}
		}
		if restoreSaved {
			if err = moveDurably(saved, live); err != nil {
				return err
			}
		}
	}
	return syncPath(data)
}

// Abandon is only called for a confirmed orphan with the server stopped. A complete
// applied transaction stays live. An interrupted swap returns to the original file set.
// Keep the entire transaction outside the staging cleanup namespace for operator salvage.
func Abandon(ctx context.Context, data string) (string, error) {
	active, err := pending(data)
	if err != nil {
		return "", err
	}
	if !active {
		return "", errors.New("no orphaned kit operation")
	}
	if err = BeginOperation(data); err != nil {
		return "", err
	}
	if err = removeIfPresent(filepath.Join(data, permit)); err != nil {
		return "", err
	}
	if err = syncPath(data); err != nil {
		return "", err
	}
	tx := filepath.Join(data, transaction)
	hasTx, err := exists(tx)
	if err != nil {
		return "", err
	}
	if hasTx {
		restore, err := needsOriginal(ctx, data, tx)
		if err != nil {
			return "", err
		}
		if restore {
			if err = mark(tx, "abandon-original"); err != nil {
				return "", err
			}
			if err = reassemble(tx, data); err != nil {
				return "", err
			}
			if err = mark(tx, "abandon-reassembled"); err != nil {
				return "", err
			}
		}
	}
	schema, err := liveSchema(ctx, data)
	if err != nil {
		return "", fmt.Errorf("volume is still locked; cannot verify existing live database: %w", err)
	}
	metadata, err := operationDetails(data)
	if err != nil {
		return "", err
	}
	dir, err := writeAbandonment(data, metadata, schema)
	if err != nil {
		return "", err
	}
	var kept string
	if hasTx {
		kept = filepath.Join(dir, "transaction")
		if err = moveDurably(tx, kept); err != nil {
			return "", err
		}
	}
	return kept, EndOperation(data)
}

func writeAbandonment(data string, metadata operationRecord, schema int64) (string, error) {
	note, err := json.Marshal(abandonment{Operation: metadata, Applied: schema, At: time.Now().UTC(), PinPath: metadata.pinPath()})
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(data, ".kit-abandoned-")
	if err != nil {
		return "", err
	}
	tmp := filepath.Join(dir, "abandon.json.new")
	if err = writeFile(tmp, bytes.NewReader(note)); err != nil {
		return "", err
	}
	if err = moveDurably(tmp, filepath.Join(dir, "abandon.json")); err != nil {
		return "", err
	}
	if err = syncPath(dir); err != nil {
		return "", err
	}
	if err = syncPath(data); err != nil {
		return "", err
	}
	return dir, nil
}
