// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package backup stores independently restorable SQLite snapshots on the simple setup's volume.
// Callers serialize creation and export with the catalog's admin lock.
package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/covers"
	"github.com/IGlace/abyssreader-server-kit/internal/store"
	_ "modernc.org/sqlite"
)

const Database = "abyssreader.db"
const manifestFile = "backup.json"
const format = "abyssreader-server-kit/1"
const layout = "kit-20060102T150405.000000000Z"

type manifest struct {
	Format         string     `json:"format"`
	Created        time.Time  `json:"created"`
	DatabaseSHA    string     `json:"databaseSha256"`
	Images         int        `json:"images"`
	KitVersion     string     `json:"kitVersion,omitempty"`
	SchemaVersion  *int64     `json:"schemaVersion,omitempty"`
	GenerationTime *time.Time `json:"generationTime,omitempty"`
	Raw            bool       `json:"raw,omitempty"`
	RawSHA         string     `json:"rawSha256,omitempty"`
	Incomplete     bool       `json:"incomplete,omitempty"`
	Missing        []string   `json:"missing,omitempty"`
}

func generation(t time.Time) string { return t.UTC().Format(layout) }

var errUnexpectedType = errors.New("unexpected file type")

func checkType(path string, directory bool) error {
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if directory && st.IsDir() || !directory && st.Mode().IsRegular() {
		return nil
	}
	return fmt.Errorf("%w: %s", errUnexpectedType, path)
}
func header(dir string) (manifest, error) {
	var m manifest
	if err := checkType(dir, true); err != nil {
		return m, err
	}
	path := filepath.Join(dir, manifestFile)
	if err := checkType(path, false); err != nil {
		return m, err
	}
	f, err := os.Open(path)
	if err != nil {
		return m, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil {
		return m, err
	}
	if len(data) > 16<<20 || json.Unmarshal(data, &m) != nil || m.Format != format || m.Created.IsZero() {
		return m, errors.New("invalid kit backup manifest")
	}
	return m, nil
}

func (m manifest) generationTime() time.Time {
	if m.GenerationTime != nil {
		return *m.GenerationTime
	}
	return m.Created
}

// At startup a fresh installation waits for its first idle minute. Overdue
// installations catch up immediately, including after a wall-clock correction.
func Due(data, schedule string, now time.Time, startup ...bool) (bool, error) {
	if schedule == "off" {
		return false, nil
	}
	every := 24 * time.Hour
	if schedule == "weekly" {
		every *= 7
	}
	m, hasHistory, err := Latest(data)
	if err != nil {
		return false, err
	}
	if !hasHistory {
		return len(startup) == 0 || !startup[0], nil
	}
	if now.Before(m.Created) {
		slog.Warn("backup creation time is in the future; scheduling catch-up", "created", m.Created, "now", now)
		return true, nil
	}
	return !now.Before(m.Created.Add(every)), nil
}

func OpenDB(path string, readOnly bool) (*sql.DB, error) {
	if err := checkType(path, false); err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	u.RawQuery = "mode=rw&_pragma=busy_timeout(5000)"
	if readOnly {
		u.RawQuery = "mode=ro&immutable=1"
	}
	return sql.Open("sqlite", u.String())
}

// imagePaths uses the snapshot's references so an absent image fails the backup.
func imagePaths(ctx context.Context, db *sql.DB, schema int64) ([]string, error) {
	// The first two migrations only stored accounts. Catalog covers arrived in
	// migration 3 and hosted pages in 4. Snapshot the installed layout as-is.
	if schema < 3 {
		return nil, nil
	}
	query := `SELECT DISTINCT 'covers', cover_image FROM titles WHERE cover_image IS NOT NULL`
	if schema >= 4 {
		query += " UNION SELECT 'pages', image FROM page_images"
	}
	rows, err := db.QueryContext(ctx, query+" ORDER BY 1,2")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var folder, name string
		if err := rows.Scan(&folder, &name); err != nil {
			return nil, err
		}
		if !covers.ValidName(name) {
			return nil, errors.New("invalid stored image name in snapshot")
		}
		paths = append(paths, filepath.Join(folder, name))
	}
	return paths, rows.Err()
}

// All snapshot image reads go through this opener, including copy-and-hash.
var openContent = func(path string) (io.ReadCloser, error) { return os.Open(path) }

func digest(path string) (string, error) {
	if err := checkType(path, false); err != nil {
		return "", err
	}
	f, err := openContent(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	return fmt.Sprintf("%x", h.Sum(nil)), err
}
func writeFile(path string, r io.Reader) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, r)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
func copyImage(src, dst string) error {
	f, err := openContent(src)
	if err != nil {
		return err
	}
	defer f.Close()
	return writeFile(dst, f)
}

func Create(ctx context.Context, data string, db *sql.DB, keep int, now time.Time, version ...string) (string, error) {
	kitVersion := "dev"
	if len(version) > 0 {
		kitVersion = version[0]
	}
	return create(ctx, data, db, keep, now, kitVersion, "")
}

// CreatePinned snapshots and pins under the caller's single admin lock. A
// published generation is immutable, so linking it immediately reuses verification.
func CreatePinned(ctx context.Context, data string, db *sql.DB, keep int, now time.Time, kitVersion, id string) (string, error) {
	if !pinID.MatchString(id) {
		return "", errors.New("invalid backup pin name")
	}
	return create(ctx, data, db, keep, now, kitVersion, id)
}

func create(ctx context.Context, data string, db *sql.DB, keep int, now time.Time, kitVersion, id string) (string, error) {
	if keep < 1 {
		return "", errors.New("backup keep must be positive")
	}
	headroom, err := spaceHeadroom(ctx, data, db, id != "", Available)
	if err != nil {
		return "", err
	}
	root := filepath.Join(data, "backups")
	if err := os.Mkdir(root, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	prior, err := List(ctx, data)
	if err != nil {
		return "", err
	}
	if err := os.Chmod(root, 0700); err != nil {
		return "", err
	}
	created := now.UTC()
	if len(prior) > 0 {
		last, _ := time.Parse(layout, prior[0].Name)
		if !now.After(last) {
			now = last.Add(time.Nanosecond)
		}
	}
	dir := filepath.Join(root, generation(now))
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		return "", fmt.Errorf("backup destination occupied: %s", dir)
	}
	scratch, err := NewScratch(root, ".writing-")
	if err != nil {
		return "", err
	}
	defer RemoveScratch(scratch)
	tmp := filepath.Join(scratch, "new")
	if err = os.Mkdir(tmp, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(tmp, Database)
	// Pre-create privately. SQLite accepts an empty destination for VACUUM INTO.
	if err := writeFile(path, bytes.NewReader(nil)); err != nil {
		return "", err
	}
	if _, err = db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return "", fmt.Errorf("SQLite snapshot: %w", err)
	}
	snap, err := OpenDB(path, true)
	if err != nil {
		return "", err
	}
	schema, err := store.AppliedVersion(ctx, snap)
	if err != nil {
		snap.Close()
		return "", err
	}
	images, err := imagePaths(ctx, snap, schema)
	snap.Close()
	if err != nil {
		return "", err
	}
	for _, folder := range []string{"covers", "pages"} {
		if err = os.Mkdir(filepath.Join(tmp, folder), 0700); err != nil {
			return "", err
		}
	}
	sources, err := findSources(ctx, data, prior, images)
	if err != nil {
		return "", err
	}
	checks := make(imageChecks)
	var missing []string
	for _, rel := range images {
		if err = ctx.Err(); err != nil {
			return "", err
		}
		ok, e := sources.capture(ctx, rel, filepath.Join(tmp, rel), checks, &headroom)
		if e != nil {
			return "", e
		}
		if !ok {
			missing = append(missing, rel)
			slog.Error("no verified local copy of referenced image", "image", rel)
		}
	}
	sha, err := digest(path)
	if err != nil {
		return "", err
	}
	m := manifest{Format: format, Created: created, DatabaseSHA: sha, Images: len(images), KitVersion: kitVersion, SchemaVersion: &schema, Incomplete: len(missing) > 0, Missing: missing}
	if !now.Equal(created) {
		stamp := now.UTC()
		m.GenerationTime = &stamp
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	if err = writeFile(filepath.Join(tmp, manifestFile), bytes.NewReader(b)); err != nil {
		return "", err
	}
	if err = verify(ctx, tmp, verifyPreserved, checks); err != nil {
		return "", err
	}
	for _, p := range []string{path, filepath.Join(tmp, "covers"), filepath.Join(tmp, "pages"), tmp} {
		if err = syncPath(p); err != nil {
			return "", err
		}
	}
	if err = os.Rename(tmp, dir); err != nil {
		return "", err
	}
	// Publish before pruning. In particular ENOSPC cannot cost an existing generation.
	if err = syncPath(root); err != nil {
		return dir, err
	}
	if !m.Incomplete && id != "" {
		// The new directory has just been verified and has not left this lock.
		if err = pin(ctx, data, filepath.Base(dir), id, true); err != nil {
			return dir, err
		}
	}
	if err = prune(ctx, root, prior, keep, m.Incomplete, checks); err != nil {
		return dir, err
	}
	if m.Incomplete {
		return dir, &IncompleteError{Generation: filepath.Base(dir), Missing: missing}
	}
	return dir, nil
}

// IncompleteError identifies a published generation with unavailable assets.
type IncompleteError struct {
	Generation string
	Missing    []string
}

func (e *IncompleteError) Error() string {
	return fmt.Sprintf("incomplete generation %s retained; unavailable images: %s", e.Generation, strings.Join(e.Missing, ", "))
}

var errImageChecksum = errors.New("image checksum mismatch")

func linkVerifiedImage(old, dst string, checks imageChecks) error {
	if err := checkType(filepath.Dir(old), true); err != nil {
		return err
	}
	if err := checkType(old, false); err != nil {
		return err
	}
	// Hash the linked inode itself, so the checksum always describes the file
	// in the new snapshot. A bad link remains private until we remove it.
	if err := os.Link(old, dst); err != nil {
		return err
	}
	sum, err := checks.checksum(dst)
	if err != nil {
		return err
	}
	if sum == filepath.Base(dst)[:64] {
		return checks.recordCopy(dst, sum)
	}
	if err = os.Remove(dst); err != nil {
		return err
	}
	return errImageChecksum
}

func copyVerifiedImage(src, dst string, checks imageChecks, forcePrivate bool) error {
	before, err := os.Lstat(src)
	if err != nil {
		return err
	}
	expected := filepath.Base(src)[:64]
	if prior, ok := checks.lookup(src, before); ok {
		if prior.digest != expected {
			return errImageChecksum
		}
		// Link the verified backup copy, never the mutable live inode.
		if !forcePrivate && prior.privateCopy != "" {
			return os.Link(prior.privateCopy, dst)
		}
	}
	f, err := openContent(src)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if err = writeFile(dst, io.TeeReader(f, h)); err != nil {
		return err
	}
	after, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !sameImageVersion(before, after) {
		return fmt.Errorf("live image changed while copying: %s", src)
	}
	sum := fmt.Sprintf("%x", h.Sum(nil))
	if sum != expected {
		checks.remember(src, after, sum, "")
		if err = os.Remove(dst); err != nil {
			return err
		}
		return errImageChecksum
	}
	// Local restores can share this live inode with an expiring generation.
	checks.remember(src, after, sum, dst)
	return checks.recordCopy(dst, sum)
}

func prune(ctx context.Context, root string, prior []Generation, keep int, newIncomplete bool, checks imageChecks) error {
	completeSlots := keep - 1 // the new complete snapshot occupies one slot
	rawRemaining := 0
	for _, m := range prior {
		if m.Raw {
			rawRemaining++
		}
	}
	for _, m := range prior {
		if m.Raw {
			rawRemaining--
			// Keep manual salvage until enough strictly newer, verified complete
			// generations exist. Incomplete attempts never expire raw data.
			if newIncomplete || completeSlots > 0 {
				continue
			}
		} else if !m.Incomplete {
			if newIncomplete {
				continue
			}
			if completeSlots > 0 && rawRemaining == 0 {
				completeSlots--
				continue
			}
		}
		// Check the full set before removing any files. While an older raw
		// copy awaits expiry, only verified, retained complete copies count.
		old := filepath.Join(root, m.Name)
		if err := verify(ctx, old, verifyPreserved, checks); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		if !m.Raw && !m.Incomplete && completeSlots > 0 {
			completeSlots--
			continue
		}
		if err := os.RemoveAll(old); err != nil {
			return fmt.Errorf("backup published, pruning: %w", err)
		}
	}
	return nil
}

func syncPath(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func Verify(ctx context.Context, dir string) error {
	return verify(ctx, dir, verifyComplete, nil)
}

type verification int

const (
	verifyComplete verification = iota
	verifyPreserved
)

func verify(ctx context.Context, dir string, mode verification, checks imageChecks) error {
	m, err := header(dir)
	if err != nil {
		return err
	}
	if m.Raw {
		if mode == verifyPreserved {
			return verifyRaw(ctx, dir, m, checks)
		}
		return errRawRestore
	}
	if m.Incomplete != (len(m.Missing) > 0) {
		return errors.New("invalid incomplete backup manifest")
	}
	if m.Incomplete && mode == verifyComplete {
		return errors.New("incomplete backup: referenced images unavailable")
	}
	path := filepath.Join(dir, Database)
	sha, err := digest(path)
	if err != nil {
		return err
	}
	if sha != m.DatabaseSHA {
		return errors.New("snapshot checksum mismatch")
	}
	db, err := OpenDB(path, true)
	if err != nil {
		return err
	}
	defer db.Close()
	schema, err := checkedVersion(ctx, db, m)
	if err != nil {
		return err
	}
	var result string
	if err = db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return errSQLiteIntegrity
	}
	if err = verifyTables(ctx, db, schema); err != nil {
		return err
	}
	paths, err := imagePaths(ctx, db, schema)
	if err != nil {
		return err
	}
	if len(paths) != m.Images {
		return errors.New("image count mismatch")
	}
	want := map[string]bool{Database: true, manifestFile: true, "covers": true, "pages": true}
	for _, rel := range paths {
		want[rel] = true
	}
	for _, rel := range m.Missing {
		if !want[rel] || filepath.Dir(rel) == "." {
			return fmt.Errorf("invalid missing image: %s", rel)
		}
		delete(want, rel)
	}
	err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if !want[rel] {
			return fmt.Errorf("unexpected backup entry %s", rel)
		}
		delete(want, rel)
		if rel == "covers" || rel == "pages" {
			if !entry.IsDir() {
				return fmt.Errorf("invalid image directory %s", rel)
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("invalid backup entry %s", rel)
		}
		if filepath.Dir(rel) != "." {
			sha, err := checks.checksum(path)
			if err != nil {
				return err
			}
			if sha != entry.Name()[:64] {
				return fmt.Errorf("image checksum mismatch: %s", rel)
			}
		}
		return ctx.Err()
	})
	if err == nil && len(want) > 0 {
		return errors.New("missing backup entries")
	}
	return err
}

func verifyTables(ctx context.Context, db *sql.DB, schema int64) error {
	// Require the identifiable tables of the migration level actually recorded.
	// A dropped table fails validation even if its image collection is empty.
	var missingTables int
	if err := db.QueryRowContext(ctx, `
 WITH required(name, kind, since) AS (VALUES
 ('users','table',1), ('sessions','table',1), ('titles','table',3), ('page_images','table',4),
 ('sync_records','table',5), ('title_reports','table',6), ('sync_records_user_title','index',7), ('backup_hosted_identity','table',8),
 ('sync_applied_operations','table',11))
 SELECT count(*) FROM required WHERE since <= ? AND NOT EXISTS
 (SELECT 1 FROM sqlite_schema WHERE sqlite_schema.name=required.name AND sqlite_schema.type=required.kind)`, schema).Scan(&missingTables); err != nil {
		return err
	}
	if missingTables != 0 {
		return errors.New("not a kit database: expected tables missing")
	}
	return nil
}

func checkedVersion(ctx context.Context, db *sql.DB, m manifest) (int64, error) {
	applied, err := store.AppliedVersion(ctx, db)
	if err != nil {
		return 0, err
	}
	if applied < 1 {
		return 0, errors.New("not a migrated kit database")
	}
	if m.SchemaVersion != nil && *m.SchemaVersion != applied {
		return 0, fmt.Errorf("backup schema mismatch: manifest %d, database %d", *m.SchemaVersion, applied)
	}
	return applied, nil
}

// CheckSchema reads the actual snapshot, including for older manifests which
// predate schemaVersion. A candidate image must support that migration level.
func CheckSchema(ctx context.Context, dir string, max int64) error {
	m, err := header(dir)
	if err != nil {
		return err
	}
	if m.Raw {
		return errRawRestore
	}
	if m.Incomplete || len(m.Missing) > 0 {
		return errors.New("incomplete backup")
	}
	db, err := OpenDB(filepath.Join(dir, Database), true)
	if err != nil {
		return err
	}
	defer db.Close()
	actual, err := checkedVersion(ctx, db, m)
	if err != nil {
		return err
	}
	if actual > max {
		return fmt.Errorf("backup schema %d is newer than supported schema %d", actual, max)
	}
	return nil
}

// Description summarizes a verified staging manifest for the restore prompt.
func Description(ctx context.Context, dir string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	m, err := header(dir)
	if err != nil {
		return "", err
	}
	status := "complete"
	if m.Incomplete || len(m.Missing) > 0 {
		status = fmt.Sprintf("incomplete (%d missing images)", len(m.Missing))
	}
	if m.Raw {
		status = "raw, unverified"
	}
	age := time.Since(m.Created).Truncate(time.Second)
	when := age.String() + " ago"
	if age < 0 {
		when = (-age).String() + " in the future"
	}
	return fmt.Sprintf("%s (%s) %s", m.Created.UTC().Format(time.RFC3339), when, status), nil
}

func ValidGeneration(name string) bool {
	t, err := time.Parse(layout, name)
	return err == nil && generation(t) == name
}
