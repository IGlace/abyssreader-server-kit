// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package pages checks, slices and stores the page images of hosted chapters.
// A page is a still JPEG, PNG or WebP of at most 2000 × 6000 pixels and 5 MiB; a
// taller long strip is cut into slices within those limits at import. Pages are stored like
// covers, named by the SHA-256 of their bytes, so a page URL never changes and a replaced page
// gets a new one.
//
// Every page file is decoded once at import (with its decoded size bounded before decoding),
// so a file damaged after a valid header is refused. What is stored carries no metadata: EXIF
// (GPS included), XMP, IPTC, comments and text chunks are removed without touching the image
// data, and a file with an EXIF rotation is refused, so the stored width and height are what
// every viewer shows.
//
// Page files come from the import folder only, through os.Root, and must be plain files (no
// link, FIFO or device). Nothing is downloaded.
package pages

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/image/webp"

	"github.com/IGlace/abyssreader-server-kit/internal/covers"
)

// The limits of one served page image.
const (
	MaxBytes  = 5 << 20
	MaxWidth  = 2000
	MaxHeight = 6000
)

// The limits of a page file in the import folder.
const (
	// MaxSlices caps how many images one page file becomes, so a page file is at most
	// MaxSourceHeight tall. A real long strip is a few slices; a chapter's whole strip is
	// usually several files.
	MaxSlices       = 10
	MaxSourceHeight = MaxSlices * MaxHeight
	// MaxAspect caps how much taller than wide a page may be: 1 × 6000 is no page, and an
	// 800 px wide strip may still be 60 000 px tall (75:1).
	MaxAspect      = 100
	MaxSourceBytes = 50 << 20
	// MaxDecodedBytes bounds the memory one decode takes, estimated per decoder from the
	// header before decoding (decodeCost).
	MaxDecodedBytes = 128 << 20
)

// MaxChapterImages caps the served images of one chapter (pages and slices together): the
// page list is one small, fixed answer. A long-strip chapter is rarely over 100 images.
const MaxChapterImages = 500

// ErrStore wraps a failure of the page store itself (a full disk, ...): it stops an import,
// where a bad page file fails only its chapter.
var ErrStore = errors.New("page store")

// sliceQuality is the JPEG quality of the slices of a JPEG or lossy WebP strip.
const sliceQuality = 90

// Dir is the page images folder inside the data folder: <data>/pages/<sha256>.<ext>.
const Dir = "pages"

// OpenStore opens (creating it if needed) the page images folder of a data folder.
func OpenStore(dataDir string) (*covers.Store, error) {
	return covers.OpenDir(filepath.Join(dataDir, Dir))
}

// Stored is one served image of a page: the file itself, or one slice of it.
type Stored struct {
	Name          string // the stored file's name (SHA-256 of its bytes + extension)
	Width, Height int
}

// Source is what one page file of the import folder became.
type Source struct {
	SHA256        string // of the file's bytes
	Width, Height int    // the file's own size
	Slices        []Stored
}

// HashFile returns the SHA-256 of the page file at path, read as a stream: an import that
// already stored this file uses what it stored without reading the image again.
func HashFile(root *os.Root, path string) (string, error) {
	f, err := open(root, path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, MaxSourceBytes+1)); err != nil {
		return "", fmt.Errorf("%s: %v", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// open opens a plain file of the import folder. Its type is checked first, without following
// a link, so a FIFO or a device never blocks the import.
func open(root *os.Root, path string) (*os.File, error) {
	strip := func(err error) error {
		var pe *fs.PathError
		if errors.As(err, &pe) {
			return pe.Err // the reason without the folder's own path
		}
		return err
	}
	st, err := root.Lstat(filepath.FromSlash(path))
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, strip(err))
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: not a plain file (a link, a folder, a FIFO or a device); a page file must be a plain file in the import folder", path)
	}
	f, err := root.Open(filepath.FromSlash(path))
	if err != nil {
		return nil, fmt.Errorf("%s: %v", path, strip(err))
	}
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s: not a plain file", path)
	}
	return f, nil
}

// Prepare reads the page file at path (relative to the import folder), checks it and stores
// what the app is served: the file itself without its metadata when it is within the limits,
// or its slices when it is taller than MaxHeight. Each image is stored as soon as it is made,
// never held with the others. declaredW and declaredH, each when not 0, must be the file's
// own. Every error starts with the path and says what is wrong in words an operator can act
// on.
func Prepare(root *os.Root, path string, declaredW, declaredH int, store *covers.Store) (Source, error) {
	fail := func(format string, args ...any) (Source, error) {
		return Source{}, fmt.Errorf("%s: %s", path, fmt.Sprintf(format, args...))
	}
	f, err := open(root, path)
	if err != nil {
		return Source{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxSourceBytes+1))
	if err != nil {
		return fail("%v", err)
	}
	if len(b) > MaxSourceBytes {
		return fail("larger than %d MiB; a page file is at most %d MiB", MaxSourceBytes>>20, MaxSourceBytes>>20)
	}
	format, cfg, err := covers.Sniff(b)
	if err != nil {
		return fail("%v", err)
	}
	w, h := cfg.Width, cfg.Height
	switch {
	case declaredW != 0 && declaredW != w:
		return fail("declared width %d, the file is %d px wide", declaredW, w)
	case declaredH != 0 && declaredH != h:
		return fail("declared height %d, the file is %d px tall", declaredH, h)
	case w < 1 || h < 1:
		return fail("%d×%d pixels: not a picture", w, h)
	case w > MaxWidth:
		return fail("%d×%d pixels: a page is at most %d px wide; scale it down before the import", w, h, MaxWidth)
	case h > MaxSourceHeight:
		return fail("%d×%d pixels: a page file is at most %d px tall (%d slices); cut it into several files before the import", w, h, MaxSourceHeight, MaxSlices)
	case h > MaxAspect*w:
		return fail("%d×%d pixels: a page is at most %d times as tall as it is wide", w, h, MaxAspect)
	}
	if o := orientation(b, format); o > 1 {
		return fail("the file carries an EXIF rotation (orientation %d); save it upright, with the rotation applied to its pixels, before the import", o)
	}
	cost, err := decodeCost(b, format, cfg)
	if err != nil {
		return fail("a broken %s image: %v", format.ContentType, err)
	}
	if cost > MaxDecodedBytes {
		return fail("%d×%d pixels is too large to decode here (about %d MiB decoded, at most %d MiB); save it as a baseline JPEG or a non-interlaced PNG, or cut it into smaller files",
			w, h, cost>>20, MaxDecodedBytes>>20)
	}
	img, err := safeDecode(b, format)
	if err != nil {
		return fail("a broken %s image: %v", format.ContentType, err)
	}
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		return fail("a broken %s image: its header says %d×%d, its data %d×%d", format.ContentType, w, h, b.Dx(), b.Dy())
	}
	sum := sha256.Sum256(b)
	src := Source{SHA256: hex.EncodeToString(sum[:]), Width: w, Height: h}
	if h <= MaxHeight {
		clean, err := stripMetadata(b, format)
		if err != nil {
			return fail("a broken %s image: %v", format.ContentType, err)
		}
		if len(clean) > MaxBytes {
			return fail("larger than %d MiB; a page is at most %d MiB: save it smaller (JPEG, or a lower quality)", MaxBytes>>20, MaxBytes>>20)
		}
		name, err := store.Put(covers.Image{Bytes: clean, Format: format, Width: w, Height: h})
		if err != nil {
			return Source{}, fmt.Errorf("%w: %v", ErrStore, err)
		}
		src.Slices = []Stored{{Name: name, Width: w, Height: h}}
		return src, nil
	}
	slices, err := cut(img, format, w, h, store)
	if errors.Is(err, ErrStore) {
		return Source{}, err
	}
	if err != nil {
		return fail("%v", err)
	}
	src.Slices = slices
	return src, nil
}

// decodeImage decodes a whole image; a variable so a test can make a decoder fail.
var decodeImage = func(b []byte, f covers.Format) (image.Image, error) {
	switch f.Ext {
	case covers.JPEG.Ext:
		return jpeg.Decode(bytes.NewReader(b))
	case covers.PNG.Ext:
		return png.Decode(bytes.NewReader(b))
	case covers.WebP.Ext:
		return webp.Decode(bytes.NewReader(b))
	}
	return nil, errors.New("no decoder")
}

// safeDecode decodes b; a decoder panic is a broken file, not a crashed import.
func safeDecode(b []byte, f covers.Format) (img image.Image, err error) {
	defer func() {
		if v := recover(); v != nil {
			img, err = nil, fmt.Errorf("the decoder failed (%v)", v)
		}
	}()
	return decodeImage(b, f)
}

// decodeCost estimates the most memory decoding b takes, per decoder:
//   - JPEG: the image (1 byte a pixel in grey, 3 in colour, 8 for CMYK/YCCK, which decodes to
//     planes and then a second image), plus, when progressive, 4 bytes a pixel for every
//     component's coefficients;
//   - PNG: the image's own bytes a pixel, half as much again when interlaced (the passes);
//   - WebP: 8 bytes a pixel when lossless (VP8L), 3 with an alpha channel, 2 otherwise.
func decodeCost(b []byte, f covers.Format, cfg image.Config) (int64, error) {
	pixels := int64(cfg.Width) * int64(cfg.Height)
	switch f.Ext {
	case covers.JPEG.Ext:
		n, progressive, err := jpegFrame(b)
		if err != nil {
			return 0, err
		}
		perPixel := int64(3)
		switch n {
		case 1:
			perPixel = 1
		case 4:
			perPixel = 8
		}
		if progressive {
			perPixel += 4 * int64(n)
		}
		return pixels * perPixel, nil
	case covers.PNG.Ext:
		cost := pixels * bytesPerPixel(cfg.ColorModel)
		if len(b) > 28 && b[28] != 0 { // IHDR's interlace method
			cost += cost / 2
		}
		return cost, nil
	case covers.WebP.Ext:
		lossless, alpha := webpKind(b)
		switch {
		case lossless:
			return pixels * 8, nil
		case alpha:
			return pixels * 3, nil
		}
		return pixels * 2, nil
	}
	return 0, errors.New("no decoder")
}

// bytesPerPixel is how much memory a decoded pixel of the colour model takes, at most.
func bytesPerPixel(m color.Model) int64 {
	switch m {
	case color.GrayModel, color.AlphaModel:
		return 1
	case color.Gray16Model, color.Alpha16Model:
		return 2
	case color.YCbCrModel:
		return 3
	case color.RGBAModel, color.NRGBAModel, color.CMYKModel, color.NYCbCrAModel:
		return 4
	}
	if _, ok := m.(color.Palette); ok {
		return 1
	}
	return 8
}

type subImager interface {
	image.Image
	SubImage(r image.Rectangle) image.Image
}

// cut stores a decoded tall image as slices of even height, each at most MaxHeight tall,
// from top to bottom. A PNG strip, a lossless WebP strip, or one with transparency gives PNG
// slices; a JPEG or lossy WebP strip gives JPEG slices. The same file always gives the same
// slices. When a slice fails, the slices this call stored are removed again.
func cut(img image.Image, format covers.Format, w, h int, store *covers.Store) (stored []Stored, err error) {
	sub, ok := img.(subImager)
	if !ok {
		return nil, errors.New("this image cannot be cut into slices; cut it before the import")
	}
	out := covers.JPEG
	switch img.(type) {
	case *image.YCbCr, *image.Gray:
		if format.Ext == covers.PNG.Ext {
			out = covers.PNG
		}
	default: // RGBA, NRGBA (lossless WebP), NYCbCrA (WebP with alpha), paletted, 16-bit ...
		out = covers.PNG
	}
	var added []string // slices this call stored that were not stored before
	defer func() {
		if err != nil {
			for _, name := range added {
				store.Remove(name)
			}
		}
	}()
	n := (h + MaxHeight - 1) / MaxHeight
	origin := img.Bounds().Min
	y := 0
	for i := range n {
		sh := h / n
		if i < h%n {
			sh++
		}
		part := sub.SubImage(image.Rect(origin.X, origin.Y+y, origin.X+w, origin.Y+y+sh))
		y += sh
		var buf bytes.Buffer
		if out.Ext == covers.PNG.Ext {
			err = png.Encode(&buf, part)
		} else {
			err = jpeg.Encode(&buf, part, &jpeg.Options{Quality: sliceQuality})
		}
		if err != nil {
			return nil, fmt.Errorf("slice %d of %d: %v", i+1, n, err)
		}
		if buf.Len() > MaxBytes {
			return nil, fmt.Errorf("slice %d of %d is %.1f MiB as %s; a page is at most %d MiB: save the strip as JPEG, or narrower, before the import",
				i+1, n, float64(buf.Len())/(1<<20), out.ContentType, MaxBytes>>20)
		}
		piece := covers.Image{Bytes: buf.Bytes(), Format: out, Width: w, Height: sh}
		existed := store.Has(covers.NameOf(piece))
		name, err := store.Put(piece)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrStore, err)
		}
		if !existed {
			added = append(added, name)
		}
		stored = append(stored, Stored{Name: name, Width: w, Height: sh})
	}
	return stored, nil
}
