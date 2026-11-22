// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestDecodeLargeCJKChapterBackup(t *testing.T) {
	// 51,150,000 UTF-8 bytes, but only 17,050,000 UTF-16 code units.
	name := strings.Repeat("猫", 341)
	chapter := rawField(16, rawText(2, name), rawInt(4, 1))
	entry := rawField(1, bytes.Repeat(chapter, 100))
	var fixture bytes.Buffer
	z := gzip.NewWriter(&fixture)
	for range 500 {
		if _, err := z.Write(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := Decode(&fixture, 20<<20)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 500 || f.Dropped != 0 {
		t.Fatalf("entries=%d dropped=%d", len(f.Entries), f.Dropped)
	}
	for _, e := range f.Entries {
		if len(e.Chapters) != 100 || e.Chapters[99].Name != name {
			t.Fatal("lost CJK chapter data")
		}
	}
}

func TestDecodeKotlinUTF16Budget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields []byte
		units  int64
	}{
		{"BMP and supplementary", rawField(1, rawText(3, "猫🦊a")), 4},
		{"replaced scalar also charged", rawField(1, rawText(3, "猫"), rawText(3, "🦊")), 3},
		{"replaced source name also charged", bytes.Join([][]byte{rawField(101, rawText(1, "猫"), rawInt(2, 1)), rawField(101, rawText(1, "🦊"), rawInt(2, 1))}, nil), 3},
		{"discarded chapter released", bytes.Join([][]byte{rawField(1, rawField(16, rawText(2, "🦊"))), rawField(1, rawText(3, "猫猫猫"))}, nil), 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limits := standardLimits
			limits.textUnits = tc.units
			if _, err := decode(bytes.NewReader(tc.fields), 1024, limits); err != nil {
				t.Fatalf("at %d UTF-16 units: %v", tc.units, err)
			}
			limits.textUnits--
			if _, err := decode(bytes.NewReader(tc.fields), 1024, limits); !errors.Is(err, ErrTooLarge) {
				t.Fatalf("one unit over budget: %v", err)
			}
		})
	}
}

func TestCodecInt64WireBoundaries(t *testing.T) {
	var encoded [binary.MaxVarintLen64]byte
	binary.PutUvarint(encoded[:], math.MaxUint64)
	read := func() (uint64, error) {
		r := wireReader{r: bufio.NewReader(bytes.NewReader(encoded[:])), end: int64(len(encoded))}
		value := r.unsigned()
		return value, r.err
	}
	if n, err := read(); err != nil || n != math.MaxUint64 {
		t.Fatalf("maximum uint64: %d %v", n, err)
	}
	encoded[len(encoded)-1]++
	if _, err := read(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("out-of-range terminal byte: %v", err)
	}
}
