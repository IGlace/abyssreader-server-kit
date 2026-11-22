// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package catalog

import (
	"strings"
	"testing"
	"time"
)

func TestCatalogSortIndexes(t *testing.T) {
	f := newFixture(t, nil)
	db := f.cat.db
	ctx := t.Context()
	_, err := db.ExecContext(ctx, `
        INSERT INTO sources (slug, name, website, lang, created_at, updated_at)
        VALUES ('source', 'Source', 'https://example.org', 'en', 0, 0);
        WITH RECURSIVE numbers(n) AS (VALUES (1) UNION ALL SELECT n+1 FROM numbers WHERE n < 50000)
        INSERT INTO titles (ref, slug, name, name_key, status, rating, views, manifest_hash, created_at, updated_at)
        SELECT 'ref-' || n, 'ref-' || n, 'Title ' || n, 'title ' || n,
               CASE WHEN n % 2 = 0 THEN 'ongoing' ELSE 'completed' END,
               CASE WHEN n % 5 != 0 THEN (n % 101) / 10.0 END, (n * 13) % 4001, '', 0, 0
        FROM numbers;
        INSERT INTO source_titles (title_id, source_id, url, lang, position)
        SELECT id, 1, '/' || slug, CASE WHEN id % 3 = 0 THEN 'es' ELSE 'en' END, 0 FROM titles;
        ANALYZE;
    `)
	if err != nil {
		t.Fatal(err)
	}

	minimum, maximum := 3.0, 8.0
	for _, tc := range []struct {
		name  string
		query Query
		index string
	}{
		{"popular descending", Query{Sort: SortPopular}, "titles_rating_desc"},
		{"popular ascending", Query{Sort: SortPopular, Asc: true, Statuses: []string{"ongoing"}}, "titles_rating_asc"},
		{"popular range", Query{Sort: SortPopular, MinRating: &minimum, MaxRating: &maximum}, "titles_rating_desc"},
		{"trending descending", Query{Sort: SortTrending}, "titles_views_desc"},
		{"trending ascending", Query{Sort: SortTrending, Asc: true, Statuses: []string{"ongoing"}}, "titles_views_asc"},
		{"trending language", Query{Sort: SortTrending, Langs: []string{"es"}}, "titles_views_desc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := tc.query
			q.Page, q.Size = 3, 50
			_, query, args := f.cat.searchSQL(q)
			args = append(args, q.Size, int64(q.Page-1)*int64(q.Size))
			planRows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, args...)
			if err != nil {
				t.Fatal(err)
			}
			var plan []string
			for planRows.Next() {
				var id, parent, unused int
				var detail string
				if err := planRows.Scan(&id, &parent, &unused, &detail); err != nil {
					planRows.Close()
					t.Fatal(err)
				}
				plan = append(plan, detail)
			}
			planRows.Close()
			if err := planRows.Err(); err != nil {
				t.Fatal(err)
			}

			start := time.Now()
			rows, err := db.QueryContext(ctx, query, args...)
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			count := 0
			for rows.Next() {
				if _, err := scanTitle(rows); err != nil {
					t.Fatal(err)
				}
				count++
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if count != q.Size {
				t.Fatalf("page has %d rows, want %d", count, q.Size)
			}
			t.Logf("50k-title page selected in %s, query plan: %v", time.Since(start), plan)
			if detail := strings.Join(plan, "\n"); !strings.Contains(detail, tc.index) || strings.Contains(detail, "TEMP B-TREE") {
				t.Errorf("want %s with no temporary sort", tc.index)
			}
		})
	}
}
