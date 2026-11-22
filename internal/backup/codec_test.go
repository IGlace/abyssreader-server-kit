// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

// Small synthetic messages in Mihon's published field layout. The fixtures are
// built independently of the encoder so a round trip cannot mask a wire change.
func rawField(number int, payload ...[]byte) []byte {
	body := bytes.Join(payload, nil)
	tag := binary.AppendUvarint(nil, uint64(number<<3|2))
	return append(binary.AppendUvarint(tag, uint64(len(body))), body...)
}
func rawInt(number int, value uint64) []byte {
	return binary.AppendUvarint(binary.AppendUvarint(nil, uint64(number<<3)), value)
}
func rawText(number int, value string) []byte { return rawField(number, []byte(value)) }

func compressFixture(t *testing.T, p []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	if _, err := w.Write(p); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestCodecFormats(t *testing.T) {
	history := func(url string, at uint64) []byte {
		return rawField(104, rawText(1, url), rawInt(2, at))
	}
	old := rawField(1, rawInt(1, 42), rawText(2, "/series"), rawText(3, "Old"),
		rawText(6, "unused description"), rawField(18, rawText(6, "tracker")))
	mihon := rawField(1, rawInt(100, 0), history("/expired", 11),
		rawField(16, rawText(1, "/expired")), rawField(16, rawText(1, "/position"), []byte{0x4d, 0, 0, 0x20, 0x40}),
		rawField(16, rawText(1, "/marked"), rawText(2, "N"), rawInt(4, 1), rawInt(5, 1), rawInt(6, 7), rawInt(11, 20)),
		history("/position", 42), history("/older", 9), rawInt(106, 15), rawInt(107, 18))
	extra := rawField(1, rawField(9100, rawText(1, "https://example.test"), rawText(2, "planning"), rawText(2, "blacklist"), rawText(3, "down")))
	input := bytes.Join([][]byte{old, mihon, extra, rawField(101, rawText(1, "Old Site"), rawInt(2, 42)), rawField(102, rawText(1, "old-pref")), rawField(104, rawText(1, "new-pref"))}, nil)
	want := &File{SourceNames: map[int64]string{42: "Old Site"}, Entries: []Entry{
		{Source: 42, URL: "/series", Title: "Old", Favorite: true},
		{Modified: 15, FavoriteModified: 18, History: &History{URL: "/position", LastRead: 42}, Chapters: []Chapter{{URL: "/position", Number: 2.5}, {URL: "/marked", Name: "N", Read: true, Bookmark: true, Page: 7, Modified: 20}}},
		{Favorite: true, Extra: &Extra{Website: "https://example.test", Lists: []string{"planning", "blacklist"}, Vote: "down"}},
	}}
	for _, data := range [][]byte{input, compressFixture(t, input)} {
		got, err := Decode(bytes.NewReader(data), int64(len(data)))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("decoded %#v, error %v; want %#v", got, err, want)
		}
	}
	var out bytes.Buffer
	for _, e := range want.Entries {
		if err := WriteEntry(&out, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteSource(&out, 42, "Old Site"); err != nil {
		t.Fatal(err)
	}
	got, err := Decode(&out, 10000)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("export decoded %#v, error %v", got, err)
	}
	// Explicit favorite=true is required even though Mihon defaults it to true.
	out.Reset()
	if err := WriteEntry(&out, Entry{Favorite: true}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte{0xa0, 6, 1}) {
		t.Fatalf("missing explicit favorite in %x", out.Bytes())
	}
}

func TestCodecMalformed(t *testing.T) {
	compressed := compressFixture(t, rawField(1))
	for _, input := range [][]byte{nil, []byte("not a backup"), []byte("{}"), compressed[:len(compressed)-1],
		{0}, {0x0b}, {0x08, 0}, {0x0a, 1}, {0x0a, 1, 0x12, 0}, {0x0a, 2, 0x12, 2}, {0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 2},
	} {
		if _, err := Decode(bytes.NewReader(input), 1000); !errors.Is(err, ErrInvalid) {
			t.Fatalf("input %x: %v", input, err)
		}
	}
	for _, data := range [][]byte{compressFixture(t, nil), rawField(55, rawField(55, []byte{0xff}))} {
		f, err := Decode(bytes.NewReader(data), 1000)
		if err != nil || len(f.Entries) != 0 {
			t.Fatalf("empty file: %v %v", f, err)
		}
	}
}

func TestCodecBounds(t *testing.T) {
	limits := standardLimits
	limits.entries, limits.chapters, limits.history, limits.sources, limits.kept = 1, 1, 1, 1, 1
	limits.text = 12
	limits.decoded = 300
	two := func(field int) []byte { return bytes.Repeat(rawField(field), 2) }
	for _, data := range [][]byte{two(1), two(101), rawField(1, two(16)), rawField(1, two(104))} {
		if _, err := decode(bytes.NewReader(data), 1000, limits); !errors.Is(err, ErrCount) {
			t.Fatalf("count for %x: %v", data, err)
		}
	}
	large := rawField(12, make([]byte, 400))
	for _, d := range [][]byte{large, compressFixture(t, large), rawField(1, rawText(2, "thirteenbytes"))} {
		if _, err := decode(bytes.NewReader(d), 1000, limits); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("size for %d bytes: %v", len(d), err)
		}
	}
	d := rawField(1, rawText(2, "ok"))
	for _, data := range [][]byte{d, compressFixture(t, d)} {
		if _, err := Decode(bytes.NewReader(data), int64(len(data)-1)); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("upload: %v", err)
		}
	}
	limits.chapters = 4
	f, err := decode(bytes.NewReader(rawField(1, rawField(16, rawText(1, "unused-name")), rawField(16, rawInt(4, 1)), rawField(16, rawInt(5, 1)))), 1000, limits)
	if err != nil || f.Dropped != 1 || len(f.Entries[0].Chapters) != 1 {
		t.Fatalf("kept: %v %v", f, err)
	}
}

func TestCodecExportWriteError(t *testing.T) {
	r, w := io.Pipe()
	r.Close()
	if err := WriteEntry(w, Entry{Favorite: true}); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("write error: %v", err)
	}
}

func TestCodecStreamingChapterFields(t *testing.T) {
	var body, out bytes.Buffer
	chapter := Chapter{URL: "/one", Read: true}
	if err := WriteChapter(&body, chapter); err != nil {
		t.Fatal(err)
	}
	if err := WriteEntryHeader(&out, Entry{URL: "/m", Favorite: false}, int64(body.Len())); err != nil {
		t.Fatal(err)
	}
	out.Write(body.Bytes())
	f, err := Decode(&out, 1024)
	if err != nil || !reflect.DeepEqual(f.Entries, []Entry{{URL: "/m", Chapters: []Chapter{chapter}}}) {
		t.Fatalf("streaming export: %v %v", f, err)
	}
}

// The benchmark can be run in isolation under a process RSS meter. One valid
// near-limit file retains 100,000 marked chapters and 48,800,000 bytes of
// chapter text. Generate its gzip incrementally so setup also has bounded RSS.
func BenchmarkCodecNearLimit(b *testing.B) {
	chapter := rawField(16, rawText(1, "/"+strings.Repeat("p", 479)), rawText(2, "Sample 1"), rawInt(4, 1))
	entry := rawField(1, rawInt(1, 1), rawText(2, "/m"), bytes.Repeat(chapter, 50))
	var fixture bytes.Buffer
	zw := gzip.NewWriter(&fixture)
	for i := 0; i < 2000; i++ {
		if _, err := zw.Write(entry); err != nil {
			b.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, err := Decode(bytes.NewReader(fixture.Bytes()), 20<<20)
		if err != nil {
			b.Fatal(err)
		}
		var chapters int
		for _, entry := range f.Entries {
			chapters += len(entry.Chapters)
		}
		if chapters != 100000 || f.Dropped != 0 {
			b.Fatalf("kept %d, dropped %d", chapters, f.Dropped)
		}
	}
	b.ReportMetric(float64(fixture.Len()), "gzip-bytes/file")
}

func TestCodecRejectsIncompleteMessageAndOverflowingFrame(t *testing.T) {
	// The message boundary must stay distinct from end-of-stream even at
	// the largest representable offset.
	file := binary.AppendUvarint([]byte{0x0a}, (1<<63-1)-10)
	if _, err := Decode(bytes.NewReader(file), 100); !errors.Is(err, ErrInvalid) {
		t.Fatalf("incomplete message: %v", err)
	}
	if err := WriteEntryHeader(io.Discard, Entry{Chapters: []Chapter{{URL: "/c"}}}, (1<<63-1)-10); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("frame size: %v", err)
	}
}

func TestCodecUnicodeFieldLimits(t *testing.T) {
	url := strings.Repeat("🦊", 512)        // 2,048 UTF-8 bytes
	name := strings.Repeat("猫", 341) + "x" // 1,024 bytes, including the last ASCII byte
	word := strings.Repeat("魚", 21) + "x"  // 64 bytes
	entry := func(url, name, word string) []byte {
		return rawField(1, rawText(2, url), rawText(3, name),
			rawField(16, rawText(1, url), rawText(2, name), rawInt(4, 1)),
			rawField(104, rawText(1, url), rawInt(2, 1)),
			rawField(9100, rawText(1, name), rawText(2, word), rawText(3, word)))
	}
	input := bytes.Join([][]byte{
		entry(url, name, word), entry(url+"x", name+"x", word+"x"),
		rawField(101, rawText(1, name), rawInt(2, 2)),
		rawField(101, rawText(1, name+"x"), rawInt(2, 3)),
		rawField(1, rawText(3, "Following title")),
	}, nil)
	f, err := Decode(bytes.NewReader(input), int64(len(input)))
	if err != nil {
		t.Fatal(err)
	}
	want := &File{SourceNames: map[int64]string{2: name}, Entries: []Entry{
		{Favorite: true, URL: url, Title: name, Chapters: []Chapter{{URL: url, Name: name, Read: true}}, History: &History{URL: url, LastRead: 1}, Extra: &Extra{Website: name, Lists: []string{word}, Vote: word}},
		{Favorite: true, Chapters: []Chapter{{Read: true}}, History: &History{LastRead: 1}, Extra: &Extra{}},
		{Favorite: true, Title: "Following title"},
	}}
	if !reflect.DeepEqual(f, want) {
		t.Fatal("expected all at-limit fields to survive and each oversized field to be absent")
	}
}
