// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package definitions

import (
	"context"
	"database/sql"
	"regexp"
)

type HealthItem struct {
	DefinitionVersion int64  `json:"definitionVersion"`
	ErrorKind         string `json:"errorKind"`
	Count             int64  `json:"count"`
}

type HealthCount struct {
	Slug, Kind string
	Count      int64
}

var healthKind = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func (i HealthItem) Valid() bool {
	return i.DefinitionVersion > 0 && i.Count > 0 && healthKind.MatchString(i.ErrorKind)
}

func aggregateKind(kind string) string {
	switch kind {
	case "network", "cloudflare", "selector_miss", "parse", "decrypt", "outdated",
		"rate_limited", "signature_invalid", "version_rollback":
		return kind
	default:
		return "other"
	}
}

func KnownSource(ctx context.Context, db reader, slug string) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM operator_definitions WHERE source_slug=?)`, slug).Scan(&exists)
	return exists, err
}

// RecordHealth keeps aggregate counts only. Future kinds share a bucket so an
// anonymous sender cannot continuously add rows by making up new kinds.
func RecordHealth(ctx context.Context, db *sql.DB, slug string, items []HealthItem) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	exists, err := KnownSource(ctx, tx, slug)
	if err != nil {
		return err
	}
	if !exists {
		return sql.ErrNoRows
	}
	const maxInt = int64(9223372036854775807)
	totals := map[string]int64{}
	for _, item := range items {
		kind := aggregateKind(item.ErrorKind)
		totals[kind] += min(item.Count, maxInt-totals[kind])
	}
	for kind, count := range totals {
		if _, err := tx.ExecContext(ctx, `INSERT INTO definition_health (source_slug, error_kind, count) VALUES (?,?,?)
			ON CONFLICT(source_slug, error_kind) DO UPDATE SET count=count+min(excluded.count, ?-count)`,
			slug, kind, count, maxInt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func Health(ctx context.Context, db reader, slug string) ([]HealthCount, error) {
	rows, err := db.QueryContext(ctx, `SELECT source_slug, error_kind, count FROM definition_health
		WHERE ?='' OR source_slug=? ORDER BY source_slug, error_kind`, slug, slug)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []HealthCount
	for rows.Next() {
		var item HealthCount
		if err := rows.Scan(&item.Slug, &item.Kind, &item.Count); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
