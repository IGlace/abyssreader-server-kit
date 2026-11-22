// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package covers downloads, checks and stores title covers. Every cover
// is stored in the server's own storage and served under an absolute, header-free URL that
// never changes: a file's name is the SHA-256 of its bytes, so a changed cover is a new URL.
//
// A cover URL is hostile input. See Fetcher for the download limits and Check for the image
// rules.
package covers

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/webp"
)

// MaxBytes caps one cover, matching the limit for page images.
const MaxBytes = 5 << 20

// MaxWidth and MaxHeight cap a cover's pixels, so that a small file cannot unpack into a huge
// image on the reader's phone.
const (
	MaxWidth  = 4000
	MaxHeight = 6000
)

// Format is an accepted image format.
type Format struct {
	Ext         string // the stored file's extension, with the dot
	ContentType string
	decode      func([]byte) (image.Config, error)
}

var (
	JPEG = Format{".jpg", "image/jpeg", func(b []byte) (image.Config, error) { return jpeg.DecodeConfig(bytes.NewReader(b)) }}
	PNG  = Format{".png", "image/png", func(b []byte) (image.Config, error) { return png.DecodeConfig(bytes.NewReader(b)) }}
	WebP = Format{".webp", "image/webp", func(b []byte) (image.Config, error) { return webp.DecodeConfig(bytes.NewReader(b)) }}
)

// Formats lists the accepted formats: JPEG, PNG and static WebP, as for page images
// (no GIF, no AVIF, nothing animated).
var Formats = []Format{JPEG, PNG, WebP}

// FormatOfExt returns the format a stored file's extension names.
func FormatOfExt(ext string) (Format, bool) {
	for _, f := range Formats {
		if f.Ext == ext {
			return f, true
		}
	}
	return Format{}, false
}

// Image is a checked cover.
type Image struct {
	Bytes         []byte
	Format        Format
	Width, Height int
}

// ErrNotAnImage and the others say why Check refused a file.
var (
	ErrNotAnImage = errors.New("not a JPEG, PNG or WebP image")
	ErrAnimated   = errors.New("an animated image; only still images are taken")
	ErrTooLarge   = fmt.Errorf("larger than %d MiB", MaxBytes>>20)
)

// Check sniffs b's format from its first bytes (never from a name or a header), reads its size
// from the image header, applies the limits and refuses incomplete containers without decoding pixels.
func Check(b []byte) (Image, error) {
	if len(b) > MaxBytes {
		return Image{}, ErrTooLarge
	}
	f, cfg, err := Sniff(b)
	if err != nil {
		return Image{}, err
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxWidth || cfg.Height > MaxHeight {
		return Image{}, fmt.Errorf("%d×%d pixels; a cover is at most %d×%d", cfg.Width, cfg.Height, MaxWidth, MaxHeight)
	}
	if !complete(b, f) {
		return Image{}, fmt.Errorf("an incomplete %s image", f.ContentType)
	}
	return Image{Bytes: b, Format: f, Width: cfg.Width, Height: cfg.Height}, nil
}

// Sniff tells b's format from its first bytes and reads the image header only (format, size,
// colour model): a still JPEG, PNG or WebP, nothing animated. It applies no size limit.
func Sniff(b []byte) (Format, image.Config, error) {
	var f Format
	switch {
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		f = JPEG
	case len(b) >= 8 && bytes.Equal(b[:8], []byte("\x89PNG\r\n\x1a\n")):
		f = PNG
		if apng(b) {
			return Format{}, image.Config{}, ErrAnimated
		}
	case len(b) >= 16 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		f = WebP
		// VP8X's flags byte has bit 1 set for an animation.
		if string(b[12:16]) == "VP8X" && len(b) >= 21 && b[20]&0x02 != 0 {
			return Format{}, image.Config{}, ErrAnimated
		}
	default:
		return Format{}, image.Config{}, ErrNotAnImage
	}
	cfg, err := f.decode(b)
	if err != nil {
		return Format{}, image.Config{}, fmt.Errorf("a broken %s image: %v", f.ContentType, err)
	}
	return f, cfg, nil
}

// apng reports whether a PNG has an animation control chunk (acTL) before its image data.
func apng(b []byte) bool {
	for i := 8; i+8 <= len(b); {
		n := int(binary.BigEndian.Uint32(b[i:]))
		switch string(b[i+4 : i+8]) {
		case "acTL":
			return true
		case "IDAT", "IEND":
			return false
		}
		if n < 0 || n > len(b) {
			return false
		}
		i += 12 + n
	}
	return false
}

// complete checks container boundaries, so an otherwise readable header cannot make a
// truncated response into a stored cover. It does not decode or validate pixel data.
func complete(b []byte, f Format) bool {
	switch f.Ext {
	case ".jpg":
		return completeJPEG(b)
	case ".png":
		for off := 8; len(b)-off >= 12; {
			n := uint64(binary.BigEndian.Uint32(b[off:]))
			if n > uint64(len(b)-off-12) {
				return false
			}
			if string(b[off+4:off+8]) == "IEND" {
				return n == 0
			}
			off += int(n) + 12
		}
		return false
	case ".webp":
		return uint64(binary.LittleEndian.Uint32(b[4:8]))+8 <= uint64(len(b))
	default:
		return false
	}
}

// completeJPEG finds the real EOI, skipping length-delimited segments such as EXIF
// (which can contain a separate thumbnail with its own EOI). Scans escape literal
// 0xff bytes as 0xff00, and restart markers have no length field.
func completeJPEG(b []byte) bool {
	b = b[2:] // SOI was checked by Sniff
	for len(b) > 0 {
		off := bytes.IndexByte(b, 0xff)
		if off < 0 {
			return false
		}
		b = b[off+1:]
		for len(b) > 0 && b[0] == 0xff {
			b = b[1:]
		}
		if len(b) == 0 {
			return false
		}
		marker := b[0]
		b = b[1:]
		switch {
		case marker == 0xd9: // EOI
			return true
		case marker == 0 || marker >= 0xd0 && marker <= 0xd7:
			continue
		default:
			if len(b) < 2 {
				return false
			}
			n := int(binary.BigEndian.Uint16(b))
			if n < 2 || n > len(b) {
				return false
			}
			b = b[n:]
		}
	}
	return false
}
