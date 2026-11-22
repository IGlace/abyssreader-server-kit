// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

// SpaceReserve leaves room for continuing writes and filesystem metadata.
const SpaceReserve int64 = 64 << 20

// Estimate includes a full VACUUM INTO database, images requiring private copies,
// the operator reserve and, on upgrade, two additional SQLite-sized work areas
// for migration and a writable rollback database. Image estimates use metadata only.
func Estimate(ctx context.Context, data string, db *sql.DB, upgrade bool) (int64, error) {
	var sqliteBytes int64
	if err := db.QueryRowContext(ctx, "SELECT page_count * page_size FROM pragma_page_count, pragma_page_size").Scan(&sqliteBytes); err != nil {
		return 0, err
	}
	needed := sqliteBytes + SpaceReserve
	if upgrade {
		needed += 2 * sqliteBytes
	}
	schema, err := store.AppliedVersion(ctx, db)
	if err != nil {
		return 0, err
	}
	images, err := imagePaths(ctx, db, schema)
	if err != nil {
		return 0, err
	}
	prior, err := List(ctx, data)
	if err != nil {
		return 0, err
	}
	sources, err := findSources(ctx, data, prior, images)
	if err != nil {
		return 0, err
	}
	for _, rel := range images {
		if err = ctx.Err(); err != nil {
			return 0, err
		}
		reusable, _, err := sources.visit(ctx, sources.history, rel, nil)
		if err != nil {
			return 0, err
		}
		if reusable {
			continue
		}
		candidates, err := sources.live.candidates(ctx, rel, sources.wanted)
		if err != nil {
			return 0, err
		}
		// Probe damaged receipts only when neither live nor owned locations
		// offer the image. Salvage always requires a new private inode.
		if len(candidates) == 0 {
			roots, err := sources.salvageRoots(ctx)
			if err != nil {
				return 0, err
			}
			for _, root := range roots {
				paths, err := root.candidates(ctx, rel, sources.wanted)
				if err != nil {
					return 0, err
				}
				candidates = append(candidates, paths...)
			}
		}
		// Corrupt candidates are discarded before trying the next. Budget the
		// largest live alternative without reading image bytes.
		var largest int64
		for _, path := range candidates {
			st, err := os.Lstat(path)
			if err != nil {
				return 0, err
			}
			if st.Mode().IsRegular() {
				largest = max(largest, st.Size())
			}
		}
		if largest > math.MaxInt64-needed {
			return 0, fmt.Errorf("backup size exceeds %d bytes", int64(math.MaxInt64))
		}
		needed += largest
	}
	return needed, nil
}

func Preflight(ctx context.Context, data string, db *sql.DB, upgrade bool) error {
	return preflight(ctx, data, db, upgrade, Available)
}

func preflight(ctx context.Context, data string, db *sql.DB, upgrade bool, available func(string) (int64, error)) error {
	_, err := spaceHeadroom(ctx, data, db, upgrade, available)
	return err
}

// Headroom can pay for a private fallback if a probable hard link fails its checksum.
func spaceHeadroom(ctx context.Context, data string, db *sql.DB, upgrade bool, available func(string) (int64, error)) (int64, error) {
	root := filepath.Join(data, "backups")
	if err := CleanScratch(root, ".writing-"); err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	// During installer staging the server can still be serving. Its active
	// transaction owns these scratch directories until recovery has finished.
	active, err := pending(data)
	if err != nil {
		return 0, err
	}
	if !active {
		for _, parent := range []string{data, filepath.Join(data, pins)} {
			if err := CleanScratch(parent, ".staging-"); err != nil && !os.IsNotExist(err) {
				return 0, err
			}
		}
	}
	needed, err := Estimate(ctx, data, db, upgrade)
	if err != nil {
		return 0, err
	}
	free, err := available(data)
	if err != nil {
		return 0, err
	}
	if free < needed {
		return 0, fmt.Errorf("insufficient free space: need %d bytes, have %d bytes available", needed, free)
	}
	return free - needed, nil
}
