// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	MaxChaptersPerEntry = 10_000
	maxURL              = 2048
	maxText             = 1024
	maxWord             = 64
	maxLists            = 16
)

type decodeLimits struct {
	decoded, text                             int64 // decompressed bytes and live UTF-8 bytes
	textUnits                                 int64 // Kotlin String.length budget
	entries, chapters, history, sources, kept int
}

var standardLimits = decodeLimits{
	// Kotlin String.length counts UTF-16 code units. Go retains up to three
	// UTF-8 bytes per unit, including replacement characters.
	decoded: 128 << 20, textUnits: 48 << 20, text: 144 << 20, entries: 20_000, chapters: MaxChaptersPerEntry,
	history: 10_000, sources: 10_000, kept: 100_000,
}

// Decode reads a single raw protobuf or gzip stream. It never stores the upload
// or allocates a buffer based on an unchecked length from the wire.
func Decode(r io.Reader, maxUpload int64) (*File, error) {
	return decode(r, maxUpload, standardLimits)
}

func decode(r io.Reader, maxUpload int64, limits decodeLimits) (*File, error) {
	if maxUpload < 0 {
		return nil, ErrTooLarge
	}
	raw := bufio.NewReader(&cappedReader{r: r, remaining: maxUpload})
	header, err := raw.Peek(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, decodeError(err)
	}
	if len(header) == 0 {
		return nil, ErrInvalid
	}
	var plain io.Reader = raw
	if len(header) == 2 && header[0] == 0x1f && header[1] == 0x8b {
		z, err := gzip.NewReader(raw)
		if err != nil {
			return nil, decodeError(err)
		}
		defer z.Close()
		plain = z
	}
	d := decoder{
		wireReader: wireReader{r: bufio.NewReaderSize(&cappedReader{r: plain, remaining: limits.decoded}, maxURL), end: math.MaxInt64},
		limits:     limits,
	}
	file := File{SourceNames: make(map[int64]string)}
	var sources int
	for field := d.next(); field != 0; field = d.next() {
		switch field {
		case 1:
			if !d.within(len(file.Entries), limits.entries, "entries") {
				break
			}
			var e Entry
			d.message(func() { d.entry(&e) })
			file.Entries = append(file.Entries, e)
		case 101:
			if !d.within(sources, limits.sources, "sources") {
				break
			}
			sources++
			d.message(func() { d.source(file.SourceNames) })
		default:
			d.skip()
		}
	}
	if d.err != nil {
		return nil, decodeError(d.err)
	}
	file.Dropped = d.dropped
	return &file, nil
}

// Keep underlying I/O errors available to the multipart handler (e.g. a
// request deadline or whole-body limit).
func decodeError(err error) error {
	if errors.Is(err, ErrTooLarge) || errors.Is(err, ErrCount) || errors.Is(err, ErrInvalid) {
		return err
	}
	return errors.Join(ErrInvalid, err)
}

type decoder struct {
	wireReader
	limits               decodeLimits
	kept, dropped        int
	textBytes, textUnits int64
	// Reused for one entry at a time. Its newest history may appear after all
	// chapters, so the position chapter cannot be selected until entry end.
	candidates []Chapter
}

func (d *decoder) within(count, limit int, what string) bool {
	if count >= limit {
		d.fail(fmt.Errorf("%w: %s", ErrCount, what))
		return false
	}
	return true
}

// text skips an oversized encoded field, clearing any previous value. A valid
// empty string still occupies a repeated list slot.
func (d *decoder) text(dest *string, maxBytes int) bool {
	d.textBytes -= int64(len(*dest))
	*dest = ""
	n := d.length()
	if n > int64(maxBytes) {
		d.discard(n)
		return false
	}
	data := d.value(n)
	if d.err != nil {
		return false
	}
	s := backupString(data)
	units := utf16Units(s)
	if units > d.limits.textUnits-d.textUnits || int64(len(s)) > d.limits.text-d.textBytes {
		d.fail(ErrTooLarge)
		return false
	}
	*dest = s
	d.textUnits += units
	d.textBytes += int64(len(s))
	return true
}

func utf16Units(s string) int64 {
	var units int64
	for _, r := range s {
		units += int64(utf16.RuneLen(r))
	}
	return units
}

// The official decoder refunds its unit budget only for discarded history,
// chapter candidates and unusable source names. Repeated scalar fields and
// overwritten source names still count. The byte guard tracks live Go strings.
func (d *decoder) release(s string) {
	d.textBytes -= int64(len(s))
	d.textUnits -= utf16Units(s)
}

// Okio reads text through the JVM UTF-8 decoder. On malformed input it
// replaces each invalid prefix with one U+FFFD, including a UTF-8 encoding
// of a surrogate. The caller bounds input to at most one URL field.
func backupString(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	out := make([]byte, 0, len(data))
	for len(data) > 0 {
		r, n := utf8.DecodeRune(data)
		if r == utf8.RuneError && n == 1 {
			n = malformedPrefixLen(data)
		}
		out = utf8.AppendRune(out, r)
		data = data[n:]
	}
	return string(out)
}

func malformedPrefixLen(data []byte) int {
	lead := data[0]
	if len(data) < 2 || lead < 0xe0 || lead > 0xf4 {
		return 1
	}
	second := data[1]
	if second < 0x80 || second > 0xbf || (lead == 0xe0 && second < 0xa0) ||
		(lead == 0xf0 && second < 0x90) || (lead == 0xf4 && second > 0x8f) {
		return 1
	}
	if len(data) >= 3 && data[2] >= 0x80 && data[2] <= 0xbf {
		return 3
	}
	return 2
}

// A set bounded by the chapter limit, independent of wire values and duplicates.
type derivedChapterNames [(MaxChaptersPerEntry + 63) / 64]uint64

func (d *decoder) entry(e *Entry) {
	e.Favorite = true
	var histories int
	var derivedNames derivedChapterNames
	for field := d.next(); field != 0; field = d.next() {
		switch field {
		case 1:
			e.Source = d.integer()
		case 2:
			d.text(&e.URL, maxURL)
		case 3:
			d.text(&e.Title, maxText)
		case 16:
			if !d.within(len(d.candidates), d.limits.chapters, "chapters") {
				break
			}
			var c Chapter
			d.message(func() { d.chapter(&c) })
			d.candidates = append(d.candidates, c)
		case 100:
			e.Favorite = d.integer() != 0
		case 104:
			if !d.within(histories, d.limits.history, "history") {
				break
			}
			histories++
			var h History
			d.message(func() { d.history(&h) })
			if e.History == nil {
				e.History = &History{}
				*e.History = h
			} else if h.LastRead > e.History.LastRead {
				d.release(e.History.URL)
				*e.History = h
			} else {
				d.release(h.URL)
			}
		case 106:
			e.Modified = d.integer()
		case 107:
			e.FavoriteModified = d.integer()
		case 9100:
			if prev := e.Extra; prev != nil {
				d.textBytes -= int64(len(prev.Website) + len(prev.Vote))
				for _, s := range prev.Lists {
					d.textBytes -= int64(len(s))
				}
			}
			e.Extra = &Extra{}
			clear(derivedNames[:])
			d.message(func() { d.extra(e.Extra, &derivedNames) })
		default:
			d.skip()
		}
	}
	d.markPositionOnlyChapter(e)
	for i, c := range d.candidates {
		derived := i < MaxChaptersPerEntry && derivedNames[i/64]&(1<<(i%64)) != 0
		if derived {
			d.release(c.Name)
			c.Name = ""
		}
		keep := hasMark(c) || (e.History != nil && c.URL != "" && c.URL == e.History.URL)
		if keep && d.kept < d.limits.kept {
			d.kept++
			// Keep a saved real name for the same chapter, including a
			// position whose fallback name has just been cleared.
			c.nameIsFallback = derived || isBackupNameFallback(c)
			e.Chapters = append(e.Chapters, c)
			continue
		}
		if keep {
			d.dropped++
		}
		d.release(c.Name)
		d.release(c.URL)
	}
	clear(d.candidates)
	d.candidates = d.candidates[:0]
}

func (d *decoder) markPositionOnlyChapter(e *Entry) {
	h := e.History
	if e.Extra == nil || !e.Extra.PositionOnlyChapter || h == nil || h.LastRead <= 0 || h.URL == "" {
		return
	}
	// Check the complete entry before the retention limit can hide a second
	// match. The flag identifies a single exact wire URL.
	var match *Chapter
	for i := range d.candidates {
		c := &d.candidates[i]
		if c.URL == h.URL {
			if match != nil {
				return
			}
			match = c
		}
	}
	if match != nil && !match.Read && !match.Bookmark {
		match.positionOnly = true
	}
}

func (d *decoder) chapter(c *Chapter) {
	for f := d.next(); f != 0; f = d.next() {
		switch f {
		case 1:
			d.text(&c.URL, maxURL)
		case 2:
			d.text(&c.Name, maxText)
		case 4:
			c.Read = d.integer() != 0
		case 5:
			c.Bookmark = d.integer() != 0
		case 6:
			c.Page = d.integer()
		case 9:
			c.Number = d.float()
		case 11:
			c.Modified = d.integer()
		default:
			d.skip()
		}
	}
}

func (d *decoder) history(h *History) {
	for f := d.next(); f != 0; f = d.next() {
		switch f {
		case 1:
			d.text(&h.URL, maxURL)
		case 2:
			h.LastRead = d.integer()
		default:
			d.skip()
		}
	}
}

func (d *decoder) source(names map[int64]string) {
	var id int64
	var name string
	var hasID bool
	for f := d.next(); f != 0; f = d.next() {
		switch f {
		case 1:
			d.text(&name, maxText)
		case 2:
			id = d.integer()
			hasID = true
		default:
			d.skip()
		}
	}
	if !hasID || strings.TrimSpace(name) == "" {
		d.release(name)
		return
	}
	d.textBytes -= int64(len(names[id]))
	names[id] = name
}

func (d *decoder) extra(x *Extra, derivedNames *derivedChapterNames) {
	for f := d.next(); f != 0; f = d.next() {
		switch f {
		case 1:
			d.text(&x.Website, maxText)
		case 2:
			if len(x.Lists) == maxLists {
				d.skip()
				continue
			}
			var s string
			if d.text(&s, maxWord) {
				x.Lists = append(x.Lists, s)
			}
		case 3:
			d.text(&x.Vote, maxWord)
		case 4:
			x.PositionOnlyChapter = d.integer() != 0
		case derivedChapterNameIndexField:
			if index := d.integer(); index > 0 && index <= int64(min(d.limits.chapters, MaxChaptersPerEntry)) {
				i := int(index - 1)
				derivedNames[i/64] |= 1 << (i % 64)
			}
		default:
			d.skip()
		}
	}
}
