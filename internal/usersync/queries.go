// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package usersync

// The reads on sync_records, named so that plans_test.go checks the very strings the server
// runs against the indexes of migration 00005.

// allKinds keys the count of every stored row of an account in a push's count cache.
const allKinds Kind = "*"

const (
	recordColumns = `id, seq, kind, title_id, source_slug, chapter_url, data, client_updated_at, deleted`
	// queryDelta: records and tombstones after a seq (the unique index on user_id, seq).
	queryDelta = `SELECT ` + recordColumns + ` FROM sync_records WHERE user_id = ? AND seq > ? ORDER BY seq LIMIT ?`
	// querySnapshot: live records after an id (sync_records_snapshot).
	querySnapshot = `SELECT ` + recordColumns + ` FROM sync_records WHERE user_id = ? AND deleted = 0 AND id > ? ORDER BY id LIMIT ?`
	// queryStored: one key, live or tombstone (the unique key).
	queryStored = `SELECT data, client_updated_at, deleted FROM sync_records
		WHERE user_id = ? AND kind = ? AND key_title = ? AND source_slug = ? AND url_hash = ?`
	// queryStoredCount: every row of an account, for the ceiling (the unique index on user_id, seq).
	queryStoredCount = `SELECT count(*) FROM sync_records WHERE user_id = ?`
	// queryLiveCount: an account's live records of a kind, for its cap (sync_records_live).
	queryLiveCount = `SELECT count(*) FROM sync_records WHERE user_id = ? AND kind = ? AND deleted = 0`
	// queryRemoveTitle: every live record on a title (sync_records_title).
	queryRemoveTitle = `SELECT id, user_id, client_updated_at FROM sync_records WHERE title_id = ? AND deleted = 0 ORDER BY user_id, id`
	// queryPurgeBatch: the oldest expired tombstones, one batch (sync_records_tombstones).
	queryPurgeBatch = `SELECT id, user_id, seq FROM sync_records WHERE deleted = 1 AND deleted_at < ? ORDER BY deleted_at LIMIT ?`

	// LibraryTitles is the title ids on one of an account's lists (args: user id, kind), for
	// the catalog's search: `t.id IN (` + LibraryTitles + `)` (sync_records_live).
	LibraryTitles = `SELECT key_title FROM sync_records WHERE user_id = ? AND kind = ? AND deleted = 0`
	// BlacklistTitles is the title ids on an account's blacklist (arg: user id).
	BlacklistTitles = `SELECT key_title FROM sync_records WHERE user_id = ? AND kind = 'blacklist' AND deleted = 0`
)

// queryStates is the account's lists and vote on n titles (args: user id, then n title ids).
func queryStates(n int) string {
	return `SELECT key_title, kind, data FROM sync_records
		WHERE user_id = ? AND kind IN ('favorite', 'planning', 'blacklist', 'rating') AND key_title IN ` + placeholders(n) + ` AND deleted = 0`
}
