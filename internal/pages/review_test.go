// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package pages

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/covers"
)

// ── Hand-made files: only the parts a check reads are real ──

// jpegHeader is a JPEG with only a start-of-frame: progressive or baseline, n components.
func jpegHeader(w, h, n int, progressive bool) []byte {
	sof := byte(0xC0)
	if progressive {
		sof = 0xC2
	}
	b := []byte{0xFF, 0xD8}
	b = append(b, jpegSegment(0xE0, []byte("JFIF\x00\x01\x01\x00\x00\x01\x00\x01\x00\x00"))...)
	b = append(b, 0xFF, sof)
	b = binary.BigEndian.AppendUint16(b, uint16(8+3*n))
	b = append(b, 8)
	b = binary.BigEndian.AppendUint16(b, uint16(h))
	b = binary.BigEndian.AppendUint16(b, uint16(w))
	b = append(b, byte(n))
	for i := range n {
		b = append(b, byte(i+1), 0x11, 0)
	}
	return append(b, 0xFF, 0xD9)
}

// interlacedPNGHeader is pngHeader with the interlace byte set.
func interlacedPNGHeader(w, h int, depth, colorType byte) []byte {
	b := pngHeader(w, h, depth, colorType)
	b[28] = 1
	binary.BigEndian.PutUint32(b[29:], crc32.ChecksumIEEE(b[12:29]))
	return b
}

// bitWriter writes VP8L's least-significant-bit-first stream.
type bitWriter struct {
	b     []byte
	acc   uint64
	nbits uint
}

func (w *bitWriter) put(v uint64, n uint) {
	w.acc |= v << w.nbits
	w.nbits += n
	for w.nbits >= 8 {
		w.b = append(w.b, byte(w.acc))
		w.acc >>= 8
		w.nbits -= 8
	}
}

func (w *bitWriter) bytes() []byte {
	if w.nbits > 0 {
		w.b = append(w.b, byte(w.acc))
	}
	return append(w.b, 0, 0, 0, 0)
}

// vp8l is a lossless VP8L bitstream of one colour: five one-symbol prefix codes, so every pixel
// takes no bits.
func vp8l(w, h int, c color.NRGBA) []byte {
	var bw bitWriter
	bw.put(0x2f, 8)
	bw.put(uint64(w-1), 14)
	bw.put(uint64(h-1), 14)
	bw.put(1, 1) // alpha is used
	bw.put(0, 3) // version
	bw.put(0, 1) // no transform
	bw.put(0, 1) // no colour cache
	bw.put(0, 1) // no meta prefix codes
	for _, sym := range []uint8{c.G, c.R, c.B, c.A, 0} {
		bw.put(1, 1) // simple code
		bw.put(0, 1) // one symbol
		bw.put(1, 1) // an 8-bit symbol
		bw.put(uint64(sym), 8)
	}
	return bw.bytes()
}

func chunk(fourcc string, data []byte) []byte {
	b := append([]byte(fourcc), binary.LittleEndian.AppendUint32(nil, uint32(len(data)))...)
	b = append(b, data...)
	if len(data)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

func riffWebP(chunks ...[]byte) []byte {
	body := []byte("WEBP")
	for _, c := range chunks {
		body = append(body, c...)
	}
	return append(append([]byte("RIFF"), binary.LittleEndian.AppendUint32(nil, uint32(len(body)))...), body...)
}

func vp8x(w, h int, flags byte) []byte {
	d := []byte{flags, 0, 0, 0}
	d = append(d, byte(w-1), byte((w-1)>>8), byte((w-1)>>16), byte(h-1), byte((h-1)>>8), byte((h-1)>>16))
	return chunk("VP8X", d)
}

// tiffOrientation is an EXIF (TIFF) block with one entry: the orientation.
func exifBlock(orientation uint16) []byte {
	b := []byte("II*\x00\x08\x00\x00\x00") // little-endian, IFD0 at 8
	b = binary.LittleEndian.AppendUint16(b, 2)
	b = binary.LittleEndian.AppendUint16(b, 0x0112) // Orientation, SHORT, 1
	b = binary.LittleEndian.AppendUint16(b, 3)
	b = binary.LittleEndian.AppendUint32(b, 1)
	b = binary.LittleEndian.AppendUint16(b, orientation)
	b = append(b, 0, 0)
	b = binary.LittleEndian.AppendUint16(b, 0x8825) // GPS IFD pointer: a GPS block is there
	b = binary.LittleEndian.AppendUint16(b, 4)
	b = binary.LittleEndian.AppendUint32(b, 1)
	b = binary.LittleEndian.AppendUint32(b, 0)
	return append(b, 0, 0, 0, 0)
}

// withJPEGSegments puts segments right after a real JPEG's start-of-image.
func withJPEGSegments(jpg []byte, segments ...[]byte) []byte {
	out := append([]byte{}, jpg[:2]...)
	for _, s := range segments {
		out = append(out, s...)
	}
	return append(out, jpg[2:]...)
}

func jpegSegment(marker byte, data []byte) []byte {
	b := []byte{0xFF, marker}
	b = binary.BigEndian.AppendUint16(b, uint16(len(data)+2))
	return append(b, data...)
}

// withPNGChunks puts chunks right after a real PNG's header chunk.
func withPNGChunks(p []byte, chunks ...[]byte) []byte {
	out := append([]byte{}, p[:33]...)
	for _, c := range chunks {
		out = append(out, c...)
	}
	return append(out, p[33:]...)
}

func pngChunk(kind string, data []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	body := append([]byte(kind), data...)
	b = append(b, body...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(body))
}

func (f *fixture) stored(name string) []byte {
	f.t.Helper()
	file, _, err := f.store.Open(name)
	if err != nil {
		f.t.Fatal(err)
	}
	defer file.Close()
	b, _ := io.ReadAll(file)
	return b
}

// ── 1. Caps: slices per file, shape ──

func TestAPageFileGivesAtMostTenSlicesAndNoNeedleShapes(t *testing.T) {
	f := newFixture(t)
	f.file("tall.png", pngHeader(300, MaxSourceHeight+1, 8, 0))
	f.file("needle.png", pngHeader(1, 6000, 8, 0))
	f.file("thin-strip.png", pngHeader(1, 134_000_000, 8, 0))
	for name, want := range map[string]string{
		"tall.png":       "300×60001 pixels: a page file is at most 60000 px tall (10 slices)",
		"needle.png":     "1×6000 pixels: a page is at most 100 times as tall as it is wide",
		"thin-strip.png": "a page file is at most 60000 px tall",
	} {
		if _, err := f.prepare(name, 0, 0); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", name, err, want)
		}
	}
	// The largest strip allowed is cut into exactly MaxSlices slices.
	f.file("ten.png", pngOf(t, gray(600, MaxSourceHeight)))
	src, err := f.prepare("ten.png", 0, 0)
	if err != nil || len(src.Slices) != MaxSlices {
		t.Fatalf("%d slices, %v", len(src.Slices), err)
	}
}

// ── 2. The decoded size, per decoder ──

func TestTheDecodedSizeIsEstimatedPerDecoder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		data  []byte
		large bool
	}{
		{"baseline.jpg", jpegHeader(2000, 7000, 3, false), false},  // 42 MB
		{"progressive.jpg", jpegHeader(2000, 7000, 3, true), true}, // + 4 bytes per pixel per component
		{"gray-progressive.jpg", jpegHeader(2000, 7000, 1, true), false},
		{"cmyk.jpg", jpegHeader(2000, 10000, 4, false), true},   // two 4-byte images
		{"ycbcr.jpg", jpegHeader(2000, 10000, 3, false), false}, // 60 MB
		{"rgba.png", pngHeader(1500, 20000, 8, 6), false},       // 120 MB
		{"interlaced.png", interlacedPNGHeader(1500, 20000, 8, 6), true},
		{"lossless.webp", riffWebP(chunk("VP8L", vp8l(2000, 9000, color.NRGBA{A: 255})[:5])), true},
		{"lossless-in-vp8x.webp", riffWebP(vp8x(2000, 9000, 0), chunk("VP8L", vp8l(2000, 9000, color.NRGBA{A: 255})[:5])), true},
		{"small-lossless.webp", riffWebP(chunk("VP8L", vp8l(1000, 9000, color.NRGBA{A: 255})[:5])), false},
	} {
		f := newFixture(t)
		f.file(tc.name, tc.data)
		_, err := f.prepare(tc.name, 0, 0)
		if err == nil {
			t.Errorf("%s: a header alone was accepted", tc.name)
			continue
		}
		if large := strings.Contains(err.Error(), "too large to decode"); large != tc.large {
			t.Errorf("%s: %v (want too large: %v)", tc.name, err, tc.large)
		}
	}
}

func TestADecoderPanicFailsThePage(t *testing.T) {
	f := newFixture(t)
	f.file("p.png", pngOf(t, gray(100, 150)))
	defer func(d func([]byte, covers.Format) (image.Image, error)) { decodeImage = d }(decodeImage)
	decodeImage = func([]byte, covers.Format) (image.Image, error) { panic("index out of range") }
	if _, err := f.prepare("p.png", 0, 0); err == nil || !strings.Contains(err.Error(), "p.png: a broken image/png image: the decoder failed") {
		t.Fatalf("%v", err)
	}
}

// ── 6. Transparency and lossless strips stay PNG ──

func TestTransparentOrLosslessWebPStripsGivePNGSlices(t *testing.T) {
	f := newFixture(t)
	f.file("strip.webp", riffWebP(chunk("VP8L", vp8l(300, 7000, color.NRGBA{R: 200, G: 100, B: 50, A: 128}))))
	src, err := f.prepare("strip.webp", 0, 0)
	if err != nil || len(src.Slices) != 2 {
		t.Fatalf("%+v %v", src, err)
	}
	for _, s := range src.Slices {
		if !strings.HasSuffix(s.Name, ".png") {
			t.Fatalf("a lossless strip gave %s", s.Name)
		}
		b := f.stored(s.Name)
		img, err := decodeImage(b, covers.PNG)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, a := img.At(10, 10).RGBA(); a>>8 != 128 {
			t.Fatalf("the transparency was lost: alpha %d", a>>8)
		}
	}
}

// ── 7. Every page is decoded; metadata is stripped; rotations are refused ──

func TestADamagedPageIsRefusedEvenWithAGoodHeader(t *testing.T) {
	f := newFixture(t)
	good := pngOf(t, gray(200, 300))
	f.file("cut.png", good[:len(good)-30])
	if _, err := f.prepare("cut.png", 0, 0); err == nil || !strings.Contains(err.Error(), "a broken image/png image") {
		t.Fatalf("%v", err)
	}
}

func TestMetadataIsStrippedFromStoredPages(t *testing.T) {
	f := newFixture(t)
	var jb bytes.Buffer
	jpeg.Encode(&jb, gray(200, 300), nil)
	exif := append([]byte("Exif\x00\x00"), exifBlock(1)...)
	f.file("a.jpg", withJPEGSegments(jb.Bytes(),
		jpegSegment(0xE1, exif), jpegSegment(0xE1, []byte("http://ns.adobe.com/xap/1.0/\x00<x:xmpmeta/>")),
		jpegSegment(0xED, []byte("Photoshop 3.0\x00")), jpegSegment(0xFE, []byte("made at 48.85N 2.35E"))))
	f.file("a.png", withPNGChunks(pngOf(t, gray(200, 300)),
		pngChunk("tEXt", []byte("Comment\x00made at 48.85N")), pngChunk("eXIf", exifBlock(1)), pngChunk("tIME", make([]byte, 7))))
	f.file("a.webp", riffWebP(vp8x(20, 30, 0x08|0x04), chunk("VP8L", vp8l(20, 30, color.NRGBA{G: 9, A: 255})),
		chunk("EXIF", exifBlock(1)), chunk("XMP ", []byte("<x:xmpmeta/>"))))
	for _, name := range []string{"a.jpg", "a.png", "a.webp"} {
		src, err := f.prepare(name, 0, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		b := f.stored(src.Slices[0].Name)
		for _, leak := range []string{"Exif", "xmpmeta", "Photoshop", "48.85N", "tIME", "eXIf", "EXIF"} {
			if bytes.Contains(b, []byte(leak)) {
				t.Errorf("%s: the stored page still has %q", name, leak)
			}
		}
		format, cfg, err := covers.Sniff(b)
		if err != nil || cfg.Width != src.Width || cfg.Height != src.Height {
			t.Fatalf("%s: stored %v %+v", name, err, cfg)
		}
		if _, err := decodeImage(b, format); err != nil {
			t.Fatalf("%s: the stored page does not decode: %v", name, err)
		}
		if name == "a.webp" && b[20]&(0x08|0x04) != 0 {
			t.Errorf("a.webp: VP8X still announces metadata: %08b", b[20])
		}
	}
}

func TestAnEXIFRotationIsRefused(t *testing.T) {
	f := newFixture(t)
	var jb bytes.Buffer
	jpeg.Encode(&jb, gray(200, 300), nil)
	f.file("r.jpg", withJPEGSegments(jb.Bytes(), jpegSegment(0xE1, append([]byte("Exif\x00\x00"), exifBlock(6)...))))
	f.file("r.png", withPNGChunks(pngOf(t, gray(200, 300)), pngChunk("eXIf", exifBlock(3))))
	f.file("r.webp", riffWebP(vp8x(20, 30, 0x08), chunk("VP8L", vp8l(20, 30, color.NRGBA{A: 255})),
		chunk("EXIF", append([]byte("Exif\x00\x00"), exifBlock(8)...))))
	for name, o := range map[string]string{"r.jpg": "6", "r.png": "3", "r.webp": "8"} {
		_, err := f.prepare(name, 0, 0)
		if err == nil || !strings.Contains(err.Error(), "an EXIF rotation (orientation "+o+")") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// ── 8. One declared side ──

func TestOneDeclaredSideIsChecked(t *testing.T) {
	f := newFixture(t)
	f.file("p.png", pngOf(t, gray(800, 1200)))
	if _, err := f.prepare("p.png", 800, 0); err != nil {
		t.Errorf("width alone: %v", err)
	}
	if _, err := f.prepare("p.png", 0, 1200); err != nil {
		t.Errorf("height alone: %v", err)
	}
	if _, err := f.prepare("p.png", 801, 0); err == nil || !strings.Contains(err.Error(), "declared width 801, the file is 800 px wide") {
		t.Errorf("wrong width: %v", err)
	}
	if _, err := f.prepare("p.png", 0, 1201); err == nil || !strings.Contains(err.Error(), "declared height 1201, the file is 1200 px tall") {
		t.Errorf("wrong height: %v", err)
	}
}

// ── 9. Only plain files ──

func TestAFIFOOrALinkIsRefusedWithoutHanging(t *testing.T) {
	f := newFixture(t)
	if err := syscall.Mkfifo(filepath.Join(f.dir, "pipe.png"), 0o644); err != nil {
		t.Skip(err)
	}
	f.file("real.png", pngOf(t, gray(10, 10)))
	os.Symlink("real.png", filepath.Join(f.dir, "link.png"))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, name := range []string{"pipe.png", "link.png"} {
			if _, err := f.prepare(name, 0, 0); err == nil || !strings.Contains(err.Error(), "not a plain file") {
				t.Errorf("Prepare %s: %v", name, err)
			}
			if _, err := HashFile(f.root, name); err == nil || !strings.Contains(err.Error(), "not a plain file") {
				t.Errorf("HashFile %s: %v", name, err)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a FIFO hung the import")
	}
}

// ── 3. A cut that fails leaves nothing behind ──

func TestAFailedCutRemovesTheSlicesItStored(t *testing.T) {
	f := newFixture(t)
	img := image.NewGray(image.Rect(0, 0, 2000, 7000))
	seed := uint32(7)
	for i := len(img.Pix) / 2; i < len(img.Pix); i++ { // the lower half is noise: too large as PNG
		seed = seed*1664525 + 1013904223
		img.Pix[i] = uint8(seed >> 24)
	}
	f.file("strip.png", pngOf(t, img))
	_, err := f.prepare("strip.png", 0, 0)
	if err == nil || !strings.Contains(err.Error(), "slice 2 of 2 is") {
		t.Fatalf("%v", err)
	}
	names, err := f.store.Names()
	if err != nil || len(names) != 0 {
		t.Fatalf("left in the store: %v %v", names, err)
	}
}
