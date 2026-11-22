// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"math"
)

// cappedReader distinguishes a file exactly at the limit from one byte beyond
// it. In particular, the gzip reader cannot inflate the entire file ahead of
// the decoder.
type cappedReader struct {
	r         io.Reader
	remaining int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.remaining < int64(len(p)) {
		p = p[:c.remaining+1]
	}
	n, err := c.r.Read(p)
	if int64(n) > c.remaining {
		return 0, ErrTooLarge
	}
	c.remaining -= int64(n)
	return n, err
}

// wireReader walks the current length-delimited message in a shared buffered
// stream. An unknown message is opaque and costs no stack or heap proportional
// to its size. end is the absolute byte offset of the current message boundary.
type wireReader struct {
	r        *bufio.Reader
	pos, end int64
	typ      int
	err      error
}

func (w *wireReader) fail(err error) {
	if w.err == nil {
		w.err = err
	}
}

func (w *wireReader) fits(n int64) bool {
	if n < 0 || n > w.end-w.pos {
		w.fail(ErrInvalid)
	}
	return w.err == nil
}

func (w *wireReader) unsigned() uint64 {
	var result uint64
	for i := 0; i < 10; i++ {
		if !w.fits(1) {
			return 0
		}
		b, err := w.r.ReadByte()
		if err != nil {
			w.fail(err)
			return 0
		}
		w.pos++
		if i == 9 && b > 1 {
			break
		}
		result |= uint64(b&127) << uint(7*i)
		if b < 128 {
			return result
		}
	}
	w.fail(ErrInvalid)
	return 0
}

func (w *wireReader) next() int {
	if w.err != nil || w.pos == w.end {
		return 0
	}
	if w.end == math.MaxInt64 {
		if _, err := w.r.Peek(1); err != nil {
			if !errors.Is(err, io.EOF) {
				w.fail(err)
			}
			return 0
		}
	}
	tag := w.unsigned()
	field := tag >> 3
	w.typ = int(tag & 7)
	if field == 0 || field > 1<<29-1 || (w.typ != 0 && w.typ != 1 && w.typ != 2 && w.typ != 5) {
		w.fail(ErrInvalid)
		return 0
	}
	return int(field)
}

func (w *wireReader) expect(typ int) bool {
	if w.typ != typ {
		w.fail(ErrInvalid)
	}
	return w.err == nil
}

func (w *wireReader) integer() int64 {
	if !w.expect(0) {
		return 0
	}
	return int64(w.unsigned())
}

func (w *wireReader) length() int64 {
	if !w.expect(2) {
		return 0
	}
	n := int64(w.unsigned())
	if !w.fits(n) {
		return 0
	}
	return n
}

func (w *wireReader) message(body func()) {
	n := w.length()
	if w.err != nil {
		return
	}
	end := w.end
	w.end = w.pos + n
	body()
	if w.pos != w.end {
		w.fail(ErrInvalid)
	}
	w.end = end
}

func (w *wireReader) discard(n int64) {
	if !w.fits(n) {
		return
	}
	for n > 0 {
		size := int(min(n, int64(w.r.Size())))
		read, err := w.r.Discard(size)
		w.pos += int64(read)
		if err != nil {
			w.fail(err)
			return
		}
		n -= int64(read)
	}
}

func (w *wireReader) skip() {
	switch w.typ {
	case 0:
		w.unsigned()
	case 1:
		w.discard(8)
	case 2:
		w.discard(w.length())
	case 5:
		w.discard(4)
	}
}

// value returns up to one bounded scalar from the buffer. The slice must be
// used before another read.
func (w *wireReader) value(n int64) []byte {
	if !w.fits(n) {
		return nil
	}
	data, err := w.r.Peek(int(n))
	if err != nil {
		w.fail(err)
		return nil
	}
	_, _ = w.r.Discard(len(data))
	w.pos += n
	return data
}

func (w *wireReader) float() float32 {
	if !w.expect(5) {
		return 0
	}
	b := w.value(4)
	if w.err != nil {
		return 0
	}
	return math.Float32frombits(binary.LittleEndian.Uint32(b))
}
