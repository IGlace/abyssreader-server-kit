// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func BenchmarkDuplicatePlanning(b *testing.B) {
	name := strings.Repeat("Tale & ", 140)
	entries := make([]Entry, 2)
	matches := []match{{1, "paper"}, {1, "paper"}}
	for i := range entries {
		for j := range 2000 {
			entries[i].Chapters = append(entries[i].Chapters, Chapter{URL: fmt.Sprintf("/c/%d", j), Name: name, Read: true})
		}
	}
	now := time.Unix(1700000000, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if duplicatePlanner(b.Context(), entries, matches, now) == nil {
			b.Fatal("missing planner")
		}
	}
}
