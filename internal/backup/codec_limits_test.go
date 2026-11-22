// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestCodecListSlotsAndRetainedTextBudget(t *testing.T) {
	input := rawField(1, rawField(9100, rawText(2, strings.Repeat("x", 65)),
		bytes.Repeat(rawText(2, ""), 15), rawText(2, "planning"), rawText(2, "blacklist"), rawText(3, "up")))
	limits := standardLimits
	limits.text = 10 // exactly the retained "planning" and "up"
	f, err := decode(bytes.NewReader(input), int64(len(input)), limits)
	if err != nil {
		t.Fatal(err)
	}
	x := f.Entries[0].Extra
	want := append(make([]string, 15), "planning")
	if !reflect.DeepEqual(x.Lists, want) || x.Vote != "up" {
		t.Fatalf("lists/vote = %#v", x)
	}
	limits.text--
	if _, err := decode(bytes.NewReader(input), int64(len(input)), limits); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("retained text budget: %v", err)
	}
}

func TestCodecReplacesMalformedUTF8WithinByteBudget(t *testing.T) {
	// Okio's JVM readUtf8 replaces a malformed prefix once, and each lone
	// continuation byte independently. The retained Go string can grow.
	input := rawField(1, rawField(3, []byte{0xff, 0x80, 'a', 0xe1, 0x80, 'b', 0xed, 0xa0, 0x80, 'c', 0xe0, 0x80, 0x80}))
	const want = "��a�b�c���"
	limits := standardLimits
	limits.text = int64(len(want))
	f, err := decode(bytes.NewReader(input), int64(len(input)), limits)
	if err != nil || f.Entries[0].Title != want {
		t.Fatalf("invalid UTF-8 decoding: %#v %v", f, err)
	}
	limits.text--
	if _, err := decode(bytes.NewReader(input), int64(len(input)), limits); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expanded text budget: %v", err)
	}
}

func TestCodecOversizedFieldMatching(t *testing.T) {
	db := matchCatalog(t)
	overURL := strings.Repeat("u", 2049)
	overName := strings.Repeat("n", 1025)
	input := bytes.Join([][]byte{
		rawField(1, rawInt(1, 77), rawText(2, "/story?a=1"), rawText(2, overURL), rawText(3, "The Forest")),
		rawField(1, rawInt(1, 77), rawText(2, "/story?a=1"), rawText(3, overName)),
		rawField(1, rawText(2, overURL), rawText(3, overName)),
	}, nil)
	f, err := Decode(bytes.NewReader(input), int64(len(input)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := matchEntries(t.Context(), db, f.Entries, false, "")
	want := []match{{1, ""}, {2, "ink"}, {}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("match after skipped fields: %v, %v; want %v", got, err, want)
	}
}
