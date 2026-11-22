// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package catalog

import (
	"slices"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/manifest"
)

func TestImportRefusesAliasClaimedByAnotherStoredSource(t *testing.T) {
	for _, tc := range []struct {
		name, stored    string
		legacyAmbiguity bool
	}{
		{"exact match", "77", false},
		{"padded match", "00077", false},
		{"already ambiguous before reimport", "00077", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, nil)
			ctx := t.Context()
			f.importDir(folder(t, obj{"manifestVersion": 1, "sources": sources(), "titles": []obj{}}, nil))
			_, err := f.cat.db.ExecContext(ctx, `INSERT INTO source_tachiyomi_ids
                SELECT ?, id FROM sources WHERE slug = 'paper-moon'`, tc.stored)
			if err != nil {
				t.Fatal(err)
			}
			if tc.legacyAmbiguity {
				_, err := f.cat.db.ExecContext(ctx, `INSERT INTO source_tachiyomi_ids
					SELECT '77', id FROM sources WHERE slug = 'luna-es'`)
				if err != nil {
					t.Fatal(err)
				}
			}
			dir := folder(t, obj{"manifestVersion": 1, "sources": []obj{
				{"slug": "luna-es", "name": "Renamed", "website": "https://luna.example.net", "lang": "es", "tachiyomiIds": []string{"77"}},
			}, "titles": []obj{}}, nil)
			m, err := manifest.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.cat.Import(ctx, m); err == nil || !strings.Contains(err.Error(), "77") ||
				!strings.Contains(err.Error(), "paper-moon") || !strings.Contains(err.Error(), "luna-es") {
				t.Fatalf("expected conflict naming the alias and both owners, got %v", err)
			}
			ids, err := f.cat.TachiyomiIDs(ctx, "paper-moon")
			if err != nil || !slices.Contains(ids, tc.stored) {
				t.Fatalf("original claim lost: %v, %v", ids, err)
			}
			source, err := f.cat.q.GetSourceBySlug(ctx, "luna-es")
			if err != nil || source.Name != "Luna Cómics" {
				t.Fatalf("refused import changed source: %v, %v", source, err)
			}
		})
	}
}

func TestImportNormalizesAStoredAliasForTheSameSource(t *testing.T) {
	f := newFixture(t, nil)
	ctx := t.Context()
	m := obj{"manifestVersion": 1, "sources": sources(), "titles": []obj{}}
	f.importDir(folder(t, m, nil))
	_, err := f.cat.db.ExecContext(ctx, `UPDATE source_tachiyomi_ids
        SET tachiyomi_id = '0004815162342' WHERE tachiyomi_id = '4815162342'`)
	if err != nil {
		t.Fatal(err)
	}
	f.importDir(folder(t, m, nil))
	if got, err := f.cat.TachiyomiIDs(ctx, "paper-moon"); err != nil || !slices.Equal(got, []string{"4815162342"}) {
		t.Fatalf("normalized alias = %v, %v", got, err)
	}
}
