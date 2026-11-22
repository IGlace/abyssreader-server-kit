// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Long catalogue keys must be divided across bounded queries even for a file
// that has fewer entries than a typical restore. Verify the result ordering and
// measure the largest actual SQL bind, including JSON escaping.
type measuredQuery struct {
	*countedReader
	maxBytes int
}

func (m *measuredQuery) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	size := 0
	for _, arg := range args {
		if s, ok := arg.(string); ok {
			size += len(s)
		}
	}
	m.maxBytes = max(m.maxBytes, size)
	return m.countedReader.QueryContext(ctx, q, args...)
}

func TestCatalogMatchBoundsLongQueryArguments(t *testing.T) {
	db := &measuredQuery{countedReader: &countedReader{DB: matchCatalog(t)}}
	var entries []Entry
	var want []match
	for i := range 1200 {
		entries = append(entries, Entry{Source: 77, URL: fmt.Sprintf("/%04d/%s", i, strings.Repeat("&", 2042)), Title: "The Forest"})
		want = append(want, match{1, ""})
	}
	entries[1000].URL = "/story?a=1"
	want[1000] = match{2, "ink"}
	got, err := matchEntries(t.Context(), db, entries, false, "")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("matches differ across query boundaries: %v", err)
	}
	t.Logf("%d SELECTs; largest query arguments %d bytes", db.reads, db.maxBytes)
	if db.maxBytes > 7<<20 {
		t.Fatalf("one catalog query bound %d bytes; want at most 7 MiB", db.maxBytes)
	}
}
