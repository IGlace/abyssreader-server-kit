// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"testing"
)

func TestDecodeIgnoresDerivedNameIndexesAboveChapterCap(t *testing.T) {
	const last = MaxChaptersPerEntry + 64
	const name = "Stored name"
	limits := standardLimits
	limits.chapters = last + 1
	chapter := rawField(16, rawText(1, "/chapter"), rawText(2, name), rawInt(4, 1))
	data := rawField(1, bytes.Repeat(chapter, last), rawField(9100,
		rawInt(5, MaxChaptersPerEntry),
		rawInt(5, MaxChaptersPerEntry+1), // May still fit in the bitset's padding.
		rawInt(5, last),                  // Beyond even the padded storage.
	))
	file, err := decode(bytes.NewReader(data), int64(len(data)), limits)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Entries) != 1 {
		t.Fatalf("decoded %d entries, want 1", len(file.Entries))
	}
	chapters := file.Entries[0].Chapters
	if len(chapters) != last {
		t.Fatalf("decoded %d chapters, want %d", len(chapters), last)
	}
	if got := chapters[MaxChaptersPerEntry-1].Name; got != "" {
		t.Errorf("chapter at cap kept its derived name %q", got)
	}
	for _, index := range []int{MaxChaptersPerEntry + 1, last} {
		if got := chapters[index-1].Name; got != name {
			t.Errorf("chapter %d name = %q, want %q", index, got, name)
		}
	}
}
