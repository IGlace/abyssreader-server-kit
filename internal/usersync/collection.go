// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package usersync

import (
	"context"
	"database/sql"
	"errors"
)

var (
	ErrCollectionMissing     = errors.New("collection is missing")
	ErrCollectionImportLimit = errors.New("collection would exceed the library limit")
)

// ImportCollection adds visible collection titles to favorites in one normal sync
// write. Existing favorites keep their timestamps and sequence, and a cap failure
// leaves the entire library untouched.
func (s *Service) ImportCollection(ctx context.Context, user, collection int64, hosted bool) error {
	return s.write(ctx, user, func(tx *sql.Tx, seq *int64) error {
		var present bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM collections WHERE id=?)", collection).Scan(&present); err != nil {
			return err
		}
		if !present {
			return ErrCollectionMissing
		}
		rows, err := tx.QueryContext(ctx, `SELECT ct.title_id FROM collection_titles ct
		 WHERE ct.collection_id=? AND EXISTS(SELECT 1 FROM source_titles st JOIN sources src ON src.id=st.source_id
		  WHERE st.title_id=ct.title_id AND (? OR src.hosted=0)) ORDER BY ct.position`, collection, hosted)
		if err != nil {
			return err
		}
		defer rows.Close()
		var titles []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return err
			}
			titles = append(titles, id)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		now := s.now().UnixMilli()
		counts := map[Kind]int{}
		var records []record
		for _, id := range titles {
			r := record{kind: Favorite, key: Key{MainMangaID: id}, at: now}
			cur, err := stored(ctx, tx, user, r)
			if err != nil {
				return err
			}
			if cur != nil {
				if !cur.deleted {
					continue
				}
				r.at = max(now, cur.at+1)
			}
			status, _, err := s.decide(ctx, tx, user, r, cur, counts)
			if err != nil {
				return err
			}
			if status != "applied" {
				return ErrCollectionImportLimit
			}
			records = append(records, r)
		}
		if len(records) == 0 {
			return nil
		}
		if err := put(ctx, tx, user, records, *seq+1, now); err != nil {
			return err
		}
		*seq += int64(len(records))
		return nil
	})
}
