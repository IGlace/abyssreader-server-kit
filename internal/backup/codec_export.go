// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"encoding/binary"
	"io"
	"math"
	"math/bits"
)

// wireFields holds only the small scalar fields of a single message. Repeated
// chapters are written separately, allowing an entry to span database pages.
type wireFields []byte

func (p *wireFields) integer(field int, value int64) {
	*p = binary.AppendUvarint(*p, uint64(field<<3))
	*p = binary.AppendUvarint(*p, uint64(value))
}

func (p *wireFields) boolean(field int, value bool) {
	var n int64
	if value {
		n = 1
	}
	p.integer(field, n)
}

func (p *wireFields) string(field int, value string) {
	if value == "" {
		return
	}
	*p = appendHeader(*p, field, int64(len(value)))
	*p = append(*p, value...)
}

func (p *wireFields) message(field int, body wireFields) {
	*p = appendHeader(*p, field, int64(len(body)))
	*p = append(*p, body...)
}

func appendHeader(p []byte, field int, length int64) []byte {
	p = binary.AppendUvarint(p, uint64(field<<3|2))
	return binary.AppendUvarint(p, uint64(length))
}

func writeAll(w io.Writer, p []byte) error {
	n, err := w.Write(p)
	if err == nil && n != len(p) {
		return io.ErrShortWrite
	}
	return err
}

func writeHeader(w io.Writer, field int, length int64) error {
	var header [20]byte
	return writeAll(w, appendHeader(header[:0], field, length))
}

func writeMessage(w io.Writer, field int, body wireFields) error {
	if err := writeHeader(w, field, int64(len(body))); err != nil {
		return err
	}
	return writeAll(w, body)
}

func entryFields(e Entry) wireFields {
	var p wireFields
	p.integer(1, e.Source)
	p.string(2, e.URL)
	p.string(3, e.Title)
	p.boolean(100, e.Favorite)
	if e.History != nil {
		var h wireFields
		h.string(1, e.History.URL)
		h.integer(2, e.History.LastRead)
		p.message(104, h)
	}
	if e.Modified != 0 {
		p.integer(106, e.Modified)
	}
	if e.FavoriteModified != 0 {
		p.integer(107, e.FavoriteModified)
	}
	if x := e.Extra; x != nil {
		var extra wireFields
		extra.string(1, x.Website)
		for _, list := range x.Lists {
			extra.string(2, list)
		}
		extra.string(3, x.Vote)
		if x.PositionOnlyChapter {
			extra.boolean(4, true)
		}
		for _, index := range x.derivedChapterNameIndexes {
			extra.integer(derivedChapterNameIndexField, int64(index))
		}
		p.message(9100, extra)
	}
	return p
}

func chapterFields(p wireFields, c Chapter) wireFields {
	p.string(1, c.URL)
	p.string(2, c.Name)
	p.boolean(4, c.Read)
	p.boolean(5, c.Bookmark)
	if c.Page != 0 {
		p.integer(6, c.Page)
	}
	if c.Number != 0 {
		p = append(p, 9<<3|5)
		p = binary.LittleEndian.AppendUint32(p, math.Float32bits(c.Number))
	}
	if c.Modified != 0 {
		p.integer(11, c.Modified)
	}
	return p
}

// WriteChapter writes a complete BackupManga.chapters field (number 16).
func WriteChapter(w io.Writer, c Chapter) error {
	return writeMessage(w, 16, chapterFields(nil, c))
}

// WriteEntryHeader writes a Backup.backupManga field, including all of e's
// own fields. The caller must then append exactly chapterFieldsBytes bytes of
// additional WriteChapter output, inside the already-declared entry boundary.
func WriteEntryHeader(w io.Writer, e Entry, chapterFieldsBytes int64) error {
	base := entryFields(e)
	if chapterFieldsBytes < 0 || chapterFieldsBytes > math.MaxInt64-int64(len(base)) {
		return ErrTooLarge
	}
	size := int64(len(base)) + chapterFieldsBytes
	var scratch wireFields
	for _, c := range e.Chapters {
		scratch = chapterFields(scratch[:0], c)
		// Field 16's tag is two bytes, followed by the varint length.
		fieldSize := int64(2 + max(1, (bits.Len(uint(len(scratch)))+6)/7) + len(scratch))
		if fieldSize > math.MaxInt64-size {
			return ErrTooLarge
		}
		size += fieldSize
	}
	if err := writeHeader(w, 1, size); err != nil {
		return err
	}
	if err := writeAll(w, base); err != nil {
		return err
	}
	for _, c := range e.Chapters {
		scratch = chapterFields(scratch[:0], c)
		if err := writeMessage(w, 16, scratch); err != nil {
			return err
		}
	}
	return nil
}

// WriteEntry writes one complete length-delimited BackupManga to a plain
// protobuf stream. The caller owns gzip framing.
func WriteEntry(w io.Writer, e Entry) error {
	return WriteEntryHeader(w, e, 0)
}

func WriteSource(w io.Writer, id int64, name string) error {
	var p wireFields
	p.string(1, name)
	p.integer(2, id)
	return writeMessage(w, 101, p)
}
