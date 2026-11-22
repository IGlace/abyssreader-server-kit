// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package usersync

import (
	"database/sql"
	"strings"
	"testing"
)

// Every query the server runs on an account's records (the strings in queries.go) reads an
// index, never the whole table.
func TestQueriesUseTheIndexes(t *testing.T) {
	db := testDB(t)
	for name, q := range map[string]struct {
		sql  string
		args []any
	}{
		"delta pull":    {queryDelta, []any{1, 5, 501}},
		"snapshot pull": {querySnapshot, []any{1, 5, 501}},
		"one key":       {queryStored, []any{1, "chapterMark", 0, "s", []byte("h")}},
		"ceiling count": {queryStoredCount, []any{1}},
		"cap count":     {queryLiveCount, []any{1, "chapterMark"}},
		"library list":  {`SELECT id FROM titles t WHERE t.id IN (` + LibraryTitles + `)`, []any{1, "favorite"}},
		"blacklist":     {`SELECT count(*) FROM titles t WHERE t.id NOT IN (` + BlacklistTitles + `)`, []any{1}},
		"me":            {queryStates(2), []any{1, 1, 2}},
		"title removal": {queryRemoveTitle, []any{1}},
		"purge batch":   {queryPurgeBatch, []any{5, 500}},
	} {
		plan := explain(t, db, q.sql, q.args)
		t.Logf("%s:\n%s", name, plan)
		if strings.Contains(plan, "SCAN sync_records") && !strings.Contains(plan, "USING") {
			t.Errorf("%s scans the table:\n%s", name, plan)
		}
		if !strings.Contains(plan, "sync_records") || !strings.Contains(plan, "INDEX") {
			t.Errorf("%s uses no index of sync_records:\n%s", name, plan)
		}
		if strings.Contains(plan, "TEMP B-TREE") && name != "title removal" {
			t.Errorf("%s sorts in a temporary b-tree:\n%s", name, plan)
		}
	}
}

func explain(t *testing.T, db *sql.DB, q string, args []any) string {
	t.Helper()
	rows, err := db.Query(`EXPLAIN QUERY PLAN `+q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, parent, notused int
		var detail string
		rows.Scan(&id, &parent, &notused, &detail)
		b.WriteString(detail + "\n")
	}
	return b.String()
}
