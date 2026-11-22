// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package pages

import (
	"bytes"
	"encoding/binary"
	"errors"

	"github.com/IGlace/abyssreader-server-kit/internal/covers"
)

// The byte-level work on page files: reading a JPEG's frame, a WebP's kind and an EXIF
// orientation, and removing metadata without touching the image data.

var errStructure = errors.New("its structure is damaged")

// jpegSegments calls fn for every marker segment before the image data (SOS), with the
// marker and the segment's data (after its length). It stops when fn returns false.
func jpegSegments(b []byte, fn func(marker byte, seg []byte, start, end int) bool) error {
	if len(b) < 2 || b[0] != 0xFF || b[1] != 0xD8 {
		return errStructure
	}
	i := 2
	for i+2 <= len(b) {
		if b[i] != 0xFF {
			return errStructure
		}
		m := b[i+1]
		switch {
		case m == 0xFF: // fill byte
			i++
			continue
		case m == 0xD8, m == 0x01, m >= 0xD0 && m <= 0xD7: // markers without a length
			i += 2
			continue
		case m == 0xD9:
			return nil
		}
		if i+4 > len(b) {
			return errStructure
		}
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if n < 2 || i+2+n > len(b) {
			return errStructure
		}
		if !fn(m, b[i+4:i+2+n], i, i+2+n) || m == 0xDA {
			return nil
		}
		i += 2 + n
	}
	return errStructure
}

// jpegFrame returns a JPEG's number of components and whether it is progressive.
func jpegFrame(b []byte) (int, bool, error) {
	n, progressive, found := 0, false, false
	err := jpegSegments(b, func(m byte, seg []byte, _, _ int) bool {
		switch m {
		case 0xC0, 0xC1, 0xC2:
			if len(seg) >= 6 {
				n, progressive, found = int(seg[5]), m == 0xC2, true
			}
			return false
		}
		return true
	})
	if err == nil && !found {
		err = errors.New("no frame header")
	}
	return n, progressive, err
}

// riffChunks calls fn for every chunk of a WebP file, with its fourcc, data and byte range.
func riffChunks(b []byte, fn func(fourcc string, data []byte, start, end int)) error {
	if len(b) < 12 {
		return errStructure
	}
	for i := 12; i < len(b); {
		if i+8 > len(b) {
			return errStructure
		}
		n := int(binary.LittleEndian.Uint32(b[i+4:]))
		end := i + 8 + n
		if n < 0 || end > len(b) {
			return errStructure
		}
		fn(string(b[i:i+4]), b[i+8:end], i, min(end+n%2, len(b)))
		i = end + n%2
	}
	return nil
}

// webpKind reports whether a WebP's image is lossless (VP8L) and whether it has an alpha
// channel (an ALPH chunk).
func webpKind(b []byte) (lossless, alpha bool) {
	riffChunks(b, func(fourcc string, _ []byte, _, _ int) {
		switch fourcc {
		case "VP8L":
			lossless = true
		case "ALPH":
			alpha = true
		}
	})
	return lossless, alpha
}

// orientation returns the EXIF orientation of a page file: 1 (upright) when it has none or
// it cannot be read.
func orientation(b []byte, f covers.Format) int {
	var tiff []byte
	switch f.Ext {
	case covers.JPEG.Ext:
		jpegSegments(b, func(m byte, seg []byte, _, _ int) bool {
			if m == 0xE1 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
				tiff = seg[6:]
				return false
			}
			return true
		})
	case covers.PNG.Ext:
		pngChunks(b, func(kind string, data []byte, _, _ int) {
			if kind == "eXIf" {
				tiff = data
			}
		})
	case covers.WebP.Ext:
		riffChunks(b, func(fourcc string, data []byte, _, _ int) {
			if fourcc == "EXIF" {
				tiff = bytes.TrimPrefix(data, []byte("Exif\x00\x00"))
			}
		})
	}
	return tiffOrientation(tiff)
}

// tiffOrientation reads tag 0x0112 of IFD0 of an EXIF (TIFF) block.
func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	ifd := int(order.Uint32(t[4:]))
	if ifd < 8 || ifd+2 > len(t) {
		return 1
	}
	n := int(order.Uint16(t[ifd:]))
	for e := ifd + 2; e+12 <= len(t) && n > 0; e, n = e+12, n-1 {
		if order.Uint16(t[e:]) == 0x0112 && order.Uint16(t[e+2:]) == 3 {
			return int(order.Uint16(t[e+8:]))
		}
	}
	return 1
}

// pngChunks calls fn for every chunk of a PNG after its signature.
func pngChunks(b []byte, fn func(kind string, data []byte, start, end int)) error {
	for i := 8; i < len(b); {
		if i+12 > len(b) {
			return errStructure
		}
		n := int(binary.BigEndian.Uint32(b[i:]))
		end := i + 12 + n
		if n < 0 || end > len(b) {
			return errStructure
		}
		fn(string(b[i+4:i+8]), b[i+8:i+8+n], i, end)
		i = end
	}
	return nil
}

// stripMetadata removes what a page file says besides its picture:
//   - JPEG: every APPn segment except JFIF (APP0), the ICC colour profile (APP2) and Adobe's
//     colour transform (APP14), and every comment;
//   - PNG: the text chunks (tEXt, zTXt, iTXt), eXIf and tIME;
//   - WebP: the EXIF and XMP chunks, and their flags in VP8X.
//
// The image data is copied byte for byte.
func stripMetadata(b []byte, f covers.Format) ([]byte, error) {
	out := make([]byte, 0, len(b))
	switch f.Ext {
	case covers.JPEG.Ext:
		out = append(out, b[:2]...)
		rest := -1
		err := jpegSegments(b, func(m byte, seg []byte, start, end int) bool {
			keep := true
			switch {
			case m == 0xFE:
				keep = false
			case m >= 0xE0 && m <= 0xEF:
				keep = m == 0xE0 && bytes.HasPrefix(seg, []byte("JFIF\x00")) ||
					m == 0xE2 && bytes.HasPrefix(seg, []byte("ICC_PROFILE\x00")) ||
					m == 0xEE && bytes.HasPrefix(seg, []byte("Adobe"))
			case m == 0xDA:
				rest = start
				return false
			}
			if keep {
				out = append(out, b[start:end]...)
			}
			return true
		})
		if err != nil || rest < 0 {
			return nil, errStructure
		}
		return append(out, b[rest:]...), nil
	case covers.PNG.Ext:
		out = append(out, b[:8]...)
		err := pngChunks(b, func(kind string, _ []byte, start, end int) {
			switch kind {
			case "tEXt", "zTXt", "iTXt", "eXIf", "tIME":
			default:
				out = append(out, b[start:end]...)
			}
		})
		return out, err
	case covers.WebP.Ext:
		out = append(out, b[:12]...)
		err := riffChunks(b, func(fourcc string, _ []byte, start, end int) {
			switch fourcc {
			case "EXIF", "XMP ":
				return
			case "VP8X":
				c := append([]byte{}, b[start:end]...)
				if len(c) > 8 {
					c[8] &^= 0x08 | 0x04 // the EXIF and XMP flags
				}
				out = append(out, c...)
				return
			}
			out = append(out, b[start:end]...)
		})
		binary.LittleEndian.PutUint32(out[4:], uint32(len(out)-8))
		return out, err
	}
	return b, nil
}
