// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var errRawRestore = errors.New("raw, unverified database preservation copy: manual salvage only; choose a verified kit backup to restore")
var errSQLiteIntegrity = errors.New("SQLite integrity_check failed")

func isRawDBFile(name string) bool {
	switch name {
	case Database, Database + "-wal", Database + "-shm", Database + "-journal":
		return true
	}
	return false
}

func corruptSQLite(err error) bool {
	var e *sqlite.Error
	if errors.As(err, &e) {
		switch e.Code() & 255 {
		case sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_NOTADB:
			return true
		}
	}
	return errors.Is(err, errSQLiteIntegrity)
}

// Preflight while the server is still running must not open a damaged live DB
// with SQLite: even read-only SQLite can rewrite SHM. Once stopped, Create
// checks the exact page and image budget against the private SQLite view.
func PreservePreflight(ctx context.Context, data string) (bool, error) {
	path := filepath.Join(data, Database)
	if err := checkType(path, false); err != nil {
		return false, err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if st.Size() < 512 {
		return true, rawSpace(ctx, data)
	}
	free, err := Available(data)
	if err != nil {
		return false, err
	}
	if free-SpaceReserve < st.Size() {
		return false, fmt.Errorf("insufficient free space for database preservation: need %d bytes plus reserve, have %d", st.Size(), free)
	}
	return false, nil
}

// The installer stops the server before calling this function. The private
// pathname gives SQLite its own shared-memory index. The main file and WAL
// are linked read-only, so SQLite cannot change their contents or remove the
// originals. A rollback journal may require undo writes; in that case every
// input SQLite could modify gets a private copy.
func stoppedSQLSnapshot(ctx context.Context, data string, keep int, now time.Time, version string) (dir string, damaged bool, err error) {
	scratch, err := NewScratch(data, ".staging-")
	if err != nil {
		return "", false, err
	}
	defer func() {
		if e := RemoveScratch(scratch); e != nil {
			err = errors.Join(err, e)
			damaged = false
		}
	}()
	journal, err := exists(filepath.Join(data, Database+"-journal"))
	if err != nil {
		return "", false, err
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		name := Database + suffix
		src := filepath.Join(data, name)
		if err := checkType(src, false); err != nil {
			if suffix != "" && os.IsNotExist(err) {
				continue
			}
			return "", false, err
		}
		if suffix == "-shm" {
			continue
		} // SQLite rebuilds this index from the private name.
		dst := filepath.Join(scratch, name)
		if journal {
			err = copyImage(src, dst)
		} else {
			err = os.Link(src, dst)
		}
		if err != nil {
			return "", false, err
		}
	}
	mode := "ro"
	if journal {
		mode = "rw"
	}
	u := url.URL{Scheme: "file", Path: filepath.Join(scratch, Database), RawQuery: "mode=" + mode + "&_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return "", false, err
	}
	defer db.Close()
	dir, err = Create(ctx, data, db, keep, now, version)
	if dir != "" || !corruptSQLite(err) {
		return dir, false, err
	}
	// Prove that the source itself is damaged. Snapshot, history or write
	// failures must not bypass normal verification.
	var result string
	probe := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result)
	return dir, corruptSQLite(probe) || (probe == nil && result != "ok"), err
}

func PreserveStopped(ctx context.Context, data string, keep int, now time.Time, version string) (string, bool, error) {
	short, err := PreservePreflight(ctx, data)
	if err != nil {
		return "", false, err
	}
	if !short {
		dir, damaged, err := stoppedSQLSnapshot(ctx, data, keep, now, version)
		if !damaged {
			return dir, false, err
		}
	}
	dir, err := preserveRaw(ctx, data, now, version)
	return dir, err == nil, err
}

// The raw set is the SQLite database, every existing recovery sidecar and the
// flat image stores. Reject unknown image entries rather than silently lose data.
func rawMembers(ctx context.Context, dir string, visit func(string, os.FileInfo) error) error {
	for _, name := range liveFiles {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(dir, name)
		st, err := os.Lstat(path)
		if os.IsNotExist(err) && name != Database {
			continue
		}
		if err != nil {
			return err
		}
		if isRawDBFile(name) {
			if !st.Mode().IsRegular() {
				return fmt.Errorf("invalid raw member %s", name)
			}
			if err = visit(name, st); err != nil {
				return err
			}
			continue
		}
		if !st.IsDir() {
			return fmt.Errorf("invalid image directory %s", name)
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		for _, ent := range entries {
			if err = ctx.Err(); err != nil {
				return err
			}
			rel := filepath.Join(name, ent.Name())
			info, err := ent.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || !validMember(rel) {
				return fmt.Errorf("unexpected raw image entry %s", rel)
			}
			if err = visit(rel, info); err != nil {
				return err
			}
		}
	}
	return nil
}

func rawSpace(ctx context.Context, data string) error {
	bytes := SpaceReserve
	err := rawMembers(ctx, data, func(_ string, st os.FileInfo) error {
		if st.Size() > math.MaxInt64-bytes {
			return errors.New("raw preservation size overflow")
		}
		bytes += st.Size()
		return nil
	})
	if err != nil {
		return err
	}
	free, err := Available(data)
	if err != nil {
		return err
	}
	if bytes > free {
		return fmt.Errorf("insufficient free space for raw preservation: need %d bytes, have %d", bytes, free)
	}
	return nil
}

func rawChecksum(ctx context.Context, dir string, checks imageChecks) (string, error) {
	h := sha256.New()
	err := rawMembers(ctx, dir, func(rel string, _ os.FileInfo) error {
		sum, err := checks.checksum(filepath.Join(dir, rel))
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%s\n", rel, sum)
		return nil
	})
	return fmt.Sprintf("%x", h.Sum(nil)), err
}

func verifyRaw(ctx context.Context, dir string, m manifest, checks imageChecks) error {
	if m.RawSHA == "" || m.Incomplete || len(m.Missing) != 0 {
		return errors.New("invalid raw preservation manifest")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		n := entry.Name()
		if n != manifestFile && n != "pages" && n != "covers" && !isRawDBFile(n) {
			return fmt.Errorf("unexpected raw generation entry %s", n)
		}
	}
	sum, err := rawChecksum(ctx, dir, checks)
	if err != nil {
		return err
	}
	if sum != m.RawSHA {
		return errors.New("raw preservation checksum mismatch")
	}
	return nil
}

func preserveRaw(ctx context.Context, data string, now time.Time, version string) (string, error) {
	if err := rawSpace(ctx, data); err != nil {
		return "", err
	}
	root := filepath.Join(data, "backups")
	if err := os.Mkdir(root, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	latest, found, err := Latest(data)
	if err != nil {
		return "", err
	}
	stamp := now.UTC()
	if found {
		last, _ := time.Parse(layout, latest.Name)
		if !stamp.After(last) {
			stamp = last.Add(time.Nanosecond)
		}
	}
	target := filepath.Join(root, generation(stamp))
	if occupied, err := exists(target); err != nil || occupied {
		return "", fmt.Errorf("raw preservation destination unavailable: %s", target)
	}
	tmp, err := NewScratch(root, ".writing-")
	if err != nil {
		return "", err
	}
	defer RemoveScratch(tmp)
	stage := filepath.Join(tmp, "new")
	for _, dir := range []string{stage, filepath.Join(stage, "covers"), filepath.Join(stage, "pages")} {
		if err = os.Mkdir(dir, 0700); err != nil {
			return "", err
		}
	}
	err = rawMembers(ctx, data, func(rel string, _ os.FileInfo) error {
		return copyImage(filepath.Join(data, rel), filepath.Join(stage, rel))
	})
	if err != nil {
		return "", err
	}
	// Hash the source independently, then re-read the copies before publishing.
	sum, err := rawChecksum(ctx, data, nil)
	if err != nil {
		return "", err
	}
	m := manifest{Format: format, Raw: true, RawSHA: sum, Created: now.UTC(), KitVersion: version}
	if !stamp.Equal(now) {
		m.GenerationTime = &stamp
	}
	bytes, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	if err = writeFile(filepath.Join(stage, manifestFile), strings.NewReader(string(bytes))); err != nil {
		return "", err
	}
	if err = verifyRaw(ctx, stage, m, nil); err != nil {
		return "", err
	}
	for _, dir := range []string{"covers", "pages", "."} {
		if err = syncPath(filepath.Join(stage, dir)); err != nil {
			return "", err
		}
	}
	if err = moveDurably(stage, target); err != nil {
		return "", err
	}
	return target, nil
}

func IsRawGeneration(dir string) (bool, error) {
	m, err := header(dir)
	return m.Raw, err
}
