// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package manifest

import (
	"strings"
	"testing"
)

func TestManifestPublicationYearIsAnOptionalCalendarYear(t *testing.T) {
	parse := func(year string) (*Manifest, error) {
		if year != "" {
			year = `,"year":` + year
		}
		return Parse([]byte(`{"manifestVersion":1,"titles":[{"ref":"sample","name":"Sample","sourceTitles":[{"source":"s","url":"/a"}]` + year + `}]}`))
	}
	baseline, err := parse("")
	if err != nil {
		t.Fatal(err)
	}
	for _, year := range []string{"null", "1", "1814", "2.026e3", "9999"} {
		t.Run(year, func(t *testing.T) {
			m, err := parse(year)
			if err != nil {
				t.Fatal(err)
			}
			if equal := m.Titles[0].Hash() == baseline.Titles[0].Hash(); equal != (year == "null") {
				t.Fatalf("year %s should change the hash exactly when given", year)
			}
		})
	}
	for _, year := range []string{`"2020"`, "0", "-1", "1999.5", "10000"} {
		t.Run("invalid "+year, func(t *testing.T) {
			if _, err := parse(year); err == nil || !strings.Contains(err.Error(), "/titles/0/year") {
				t.Fatalf("expected a year error for %s, got %v", year, err)
			}
		})
	}
}
