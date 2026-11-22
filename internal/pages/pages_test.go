// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package pages

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/covers"
)

func gray(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetGray(x, y, color.Gray{uint8((x/16 + y/16) % 2 * 200)})
		}
	}
	return img
}

func pngOf(t testing.TB, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegOf(t testing.TB, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// pngHeader is a PNG whose header claims w×h pixels of the given colour type and depth; only
// the header is real, which is all a limit check before decoding may read.
func pngHeader(w, h int, depth, colorType byte) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8], ihdr[9] = depth, colorType
	chunk := append([]byte("IHDR"), ihdr...)
	binary.Write(&b, binary.BigEndian, uint32(13))
	b.Write(chunk)
	binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

type fixture struct {
	t     *testing.T
	dir   string
	root  *os.Root
	store *covers.Store
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, dir: dir, root: root, store: store}
}

func (f *fixture) file(name string, b []byte) {
	f.t.Helper()
	p := filepath.Join(f.dir, filepath.FromSlash(name))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) prepare(name string, w, h int) (Source, error) {
	f.t.Helper()
	return Prepare(f.root, name, w, h, f.store)
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func TestAPageWithinTheLimitsIsStoredAsItIs(t *testing.T) {
	f := newFixture(t)
	b := pngOf(t, gray(800, 1200))
	f.file("ch1/01.png", b)
	src, err := f.prepare("ch1/01.png", 800, 1200)
	if err != nil {
		t.Fatal(err)
	}
	if src.SHA256 != sum(b) || src.Width != 800 || src.Height != 1200 || len(src.Slices) != 1 {
		t.Fatalf("%+v", src)
	}
	s := src.Slices[0]
	if s.Name != sum(b)+".png" || s.Width != 800 || s.Height != 1200 || !f.store.Has(s.Name) {
		t.Fatalf("%+v", s)
	}
	hash, err := HashFile(f.root, "ch1/01.png")
	if err != nil || hash != src.SHA256 {
		t.Fatalf("HashFile = %s, %v", hash, err)
	}
	// Undeclared sizes are read from the file.
	f.file("ch1/02.jpg", jpegOf(t, gray(640, 900)))
	if src, err := f.prepare("ch1/02.jpg", 0, 0); err != nil || src.Slices[0].Width != 640 || !strings.HasSuffix(src.Slices[0].Name, ".jpg") {
		t.Fatalf("%+v %v", src, err)
	}
}

func TestPagesOutsideTheLimitsAreRefusedWithAClearReason(t *testing.T) {
	f := newFixture(t)
	noisy := image.NewGray(image.Rect(0, 0, 2000, 3000))
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range noisy.Pix {
		noisy.Pix[i] = uint8(rng.Uint32())
	}
	var anim bytes.Buffer
	gif.Encode(&anim, image.NewPaletted(image.Rect(0, 0, 10, 10), color.Palette{color.Black, color.White}), nil)
	f.file("wide.png", pngOf(t, gray(2001, 100)))
	f.file("heavy.png", pngOf(t, noisy))
	f.file("text.png", []byte("not an image at all"))
	f.file("anim.gif", anim.Bytes())
	f.file("broken.png", []byte("\x89PNG\r\n\x1a\nbroken"))
	f.file("ok.png", pngOf(t, gray(100, 150)))
	// A strip too large to decode safely: only its header is real.
	f.file("huge.png", pngHeader(2000, 30000, 8, 6))
	big, _ := os.Create(filepath.Join(f.dir, "big.png"))
	big.Truncate(MaxSourceBytes + 1)
	big.Close()

	for name, want := range map[string]string{
		"wide.png":       "2001×100 pixels: a page is at most 2000 px wide",
		"heavy.png":      "larger than 5 MiB",
		"text.png":       "not a JPEG, PNG or WebP image",
		"anim.gif":       "not a JPEG, PNG or WebP image",
		"broken.png":     "a broken image/png image",
		"huge.png":       "too large to decode",
		"big.png":        "larger than 50 MiB",
		"missing.png":    "no such file",
		"../outside.png": "path escapes",
	} {
		_, err := f.prepare(name, 0, 0)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.HasPrefix(err.Error(), name+": ") {
			t.Errorf("%s: %v, want %q", name, err, want)
		}
	}
	if _, err := f.prepare("ok.png", 100, 151); err == nil || !strings.Contains(err.Error(), "declared height 151, the file is 150 px tall") {
		t.Errorf("declared size: %v", err)
	}
	if _, err := HashFile(f.root, "../outside.png"); err == nil {
		t.Error("HashFile left the folder")
	}
}

func TestASymlinkOutOfTheFolderIsRefused(t *testing.T) {
	f := newFixture(t)
	outside := filepath.Join(t.TempDir(), "secret.png")
	os.WriteFile(outside, pngOf(t, gray(10, 10)), 0o644)
	if err := os.Symlink(outside, filepath.Join(f.dir, "link.png")); err != nil {
		t.Skip(err)
	}
	if _, err := f.prepare("link.png", 0, 0); err == nil {
		t.Fatal("a symlink led out of the import folder")
	}
}

func TestALongStripIsCutIntoSlicesWithinTheLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		data   []byte
		ext    string
		height []int
	}{
		{"strip.png", pngOf(t, gray(300, 13000)), ".png", []int{4334, 4333, 4333}},
		{"strip.jpg", jpegOf(t, gray(720, 6001)), ".jpg", []int{3001, 3000}},
	} {
		f := newFixture(t)
		f.file(tc.name, tc.data)
		src, err := f.prepare(tc.name, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(src.Slices) != len(tc.height) || src.SHA256 != sum(tc.data) {
			t.Fatalf("%s: %+v", tc.name, src)
		}
		for i, s := range src.Slices {
			if s.Height != tc.height[i] || s.Height > MaxHeight || !strings.HasSuffix(s.Name, tc.ext) {
				t.Errorf("%s slice %d: %+v", tc.name, i, s)
			}
			// The declared size is the stored file's own.
			file, format, err := f.store.Open(s.Name)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(file)
			file.Close()
			got, cfg, err := covers.Sniff(b)
			if err != nil || got.Ext != format.Ext || cfg.Width != s.Width || cfg.Height != s.Height || len(b) > MaxBytes {
				t.Errorf("%s slice %d: stored %v %dx%d (%d bytes), declared %dx%d", tc.name, i, err, cfg.Width, cfg.Height, len(b), s.Width, s.Height)
			}
		}
		// The same file gives the same slices, so the same URLs.
		again, err := f.prepare(tc.name, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		for i := range again.Slices {
			if again.Slices[i] != src.Slices[i] {
				t.Errorf("%s: slice %d changed: %+v vs %+v", tc.name, i, again.Slices[i], src.Slices[i])
			}
		}
	}
}

func TestDecodedSizeBound(t *testing.T) {
	for _, tc := range []struct {
		w, h        int
		depth, kind byte
		ok          bool
	}{
		{2000, 20000, 8, 0, true},   // gray: 40 MB decoded
		{2000, 20000, 8, 6, false},  // RGBA: 160 MB
		{1000, 20000, 8, 6, true},   // RGBA: 80 MB
		{1000, 20000, 16, 6, false}, // 16-bit RGBA: 160 MB
	} {
		f := newFixture(t)
		f.file("x.png", pngHeader(tc.w, tc.h, tc.depth, tc.kind))
		_, err := f.prepare("x.png", 0, 0)
		tooLarge := err != nil && strings.Contains(err.Error(), "too large to decode")
		if tooLarge == tc.ok {
			t.Errorf("%dx%d depth %d type %d: %v", tc.w, tc.h, tc.depth, tc.kind, err)
		}
	}
}
