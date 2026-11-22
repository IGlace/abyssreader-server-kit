// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package features

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/IGlace/abyssreader-server-kit/internal/catalog"
	"github.com/IGlace/abyssreader-server-kit/internal/covers"
	"github.com/IGlace/abyssreader-server-kit/internal/pages"
)

// Caller holds the admin lock. Commit the database first. A failed or interrupted
// filesystem cleanup is safe to retry, including when the database is already empty.
func purgeHosted(ctx context.Context, db *sql.DB, dir string, out io.Writer) (string, error) {
	images, err := openPages(dir)
	if err != nil {
		return "", err
	}
	if images != nil {
		defer images.Close()
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var chapters, pagesCount, sourceTitles int
	err = tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM chapters),(SELECT count(*) FROM chapter_pages),
 (SELECT count(*) FROM source_titles WHERE source_id IN (SELECT id FROM sources WHERE hosted=1))`).Scan(&chapters, &pagesCount, &sourceTitles)
	if err != nil {
		return "", err
	}
	if err := listLosingLastSource(ctx, tx, out); err != nil {
		return "", err
	}
	// Invalidate the import shortcut so the same manifest recreates its hosted rows.
	for _, query := range []string{
		`UPDATE titles SET manifest_hash='' WHERE id IN (SELECT title_id FROM source_titles WHERE source_id IN (SELECT id FROM sources WHERE hosted=1))`,
		`DELETE FROM source_titles WHERE source_id IN (SELECT id FROM sources WHERE hosted=1)`,
		`DELETE FROM sources WHERE hosted=1`,
		`DELETE FROM page_files`,
	} {
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	summary := fmt.Sprintf("hosted_source_titles=%d chapters=%d pages=%d", sourceTitles, chapters, pagesCount)
	files, temps, err := removePageFiles(ctx, images)
	return fmt.Sprintf("%s image_files=%d temporary_files=%d", summary, files, temps), err
}

// Stream the refs selected by this transaction before removing their sources.
// Only titles that are visible with hosted enabled and hidden with it disabled
// lose their last source. A second purge therefore names none of them again.
func listLosingLastSource(ctx context.Context, tx *sql.Tx, out io.Writer) error {
	rows, err := tx.QueryContext(ctx, `SELECT t.ref FROM titles t WHERE `+catalog.VisibleTitle+` AND NOT `+catalog.VisibleTitle+` ORDER BY t.ref`, true, false)
	if err != nil {
		return err
	}
	defer rows.Close()
	first := true
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			return err
		}
		if first {
			if _, err := fmt.Fprintln(out, "Titles losing their last source in this purge (hidden until reimport). Use admin titles remove <ref> to remove one for good:"); err != nil {
				return err
			}
			first = false
		}
		if _, err := fmt.Fprintf(out, "  %q\n", ref); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Open the actual pages directory once. A link here could point into the cover
// store, whose filenames have the same format. An empty, never-used kit may
// have no pages directory at all.
func openPages(dir string) (*os.Root, error) {
	path := filepath.Join(dir, pages.Dir)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("pages must be a directory, not a link")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, errors.New("pages directory changed while opening")
	}
	return root, nil
}

func removePageFiles(ctx context.Context, root *os.Root) (images, incoming int, err error) {
	if root == nil {
		return 0, 0, nil
	}
	dir, err := root.Open(".")
	if err != nil {
		return 0, 0, err
	}
	defer dir.Close()
	for {
		if err := ctx.Err(); err != nil {
			return images, incoming, err
		}
		entries, err := dir.ReadDir(128)
		if err == io.EOF {
			return images, incoming, nil
		}
		if err != nil {
			return images, incoming, err
		}
		for _, e := range entries {
			name := e.Name()
			temp := strings.HasPrefix(name, ".incoming-")
			if !e.Type().IsRegular() || (!temp && !covers.ValidName(name)) {
				continue
			}
			if err := root.Remove(name); err != nil {
				return images, incoming, err
			}
			if temp {
				incoming++
			} else {
				images++
			}
		}
	}
}
