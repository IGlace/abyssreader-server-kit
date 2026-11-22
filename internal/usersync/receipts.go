// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package usersync

import (
	"context"
	"database/sql"
)

// This holds a full 100,000-mark client queue plus 25,000 other changes.
// Keep the newest applied operations, counting apply order even when the clock moves.
const maxAppliedReceipts = 125_000

// appliedReceipts loads only this account and the IDs in this batch. Rejections
// and conflicts still use the normal rules on retry. An applied ID identifies the
// first result even if a later request changes its payload.
func appliedReceipts(ctx context.Context, tx *sql.Tx, userID int64, ids []string) (map[string]Result, error) {
	args := make([]any, len(ids)+1)
	args[0] = userID
	for i, id := range ids {
		args[i+1] = id
	}
	rows, err := tx.QueryContext(ctx, `SELECT op_id, status, reason FROM sync_applied_operations
		WHERE user_id = ? AND op_id IN `+placeholders(len(ids)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := make(map[string]Result)
	for rows.Next() {
		var result Result
		if err := rows.Scan(&result.OpID, &result.Status, &result.Reason); err != nil {
			return nil, err
		}
		found[result.OpID] = result
	}
	return found, rows.Err()
}

// The hourly purge keeps receipts for 97 days, a week beyond tombstones.
// The per-account size cap can evict them earlier under sustained writes.
func (s *Service) purgeReceipts(ctx context.Context, cutoff int64) error {
	for {
		res, err := s.db.ExecContext(ctx, `DELETE FROM sync_applied_operations WHERE rowid IN (
			SELECT rowid FROM sync_applied_operations WHERE applied_at < ?
			ORDER BY applied_at LIMIT ?)`, cutoff, s.purgeBatch)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil || n < int64(s.purgeBatch) {
			return err
		}
	}
}
