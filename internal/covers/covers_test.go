// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package covers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pngOf(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, 0, color.RGBA{uint8(x), 40, 90, 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegOf(t testing.TB, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// A 1×1 lossless WebP.
var webp1x1, _ = base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA==")

// withChunk inserts a PNG chunk right after IHDR.
func withChunk(p []byte, typ string, data []byte) []byte {
	ihdrEnd := 8 + 12 + int(binary.BigEndian.Uint32(p[8:]))
	var c bytes.Buffer
	binary.Write(&c, binary.BigEndian, uint32(len(data)))
	c.WriteString(typ)
	c.Write(data)
	binary.Write(&c, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(typ), data...)))
	out := append([]byte{}, p[:ihdrEnd]...)
	out = append(out, c.Bytes()...)
	return append(out, p[ihdrEnd:]...)
}

func TestCheckAcceptsStillJPEGPNGAndWebP(t *testing.T) {
	for _, c := range []struct {
		name string
		b    []byte
		f    Format
		w, h int
	}{
		{"jpeg", jpegOf(t, 30, 45), JPEG, 30, 45},
		{"png", pngOf(t, 300, 450), PNG, 300, 450},
		{"webp", webp1x1, WebP, 1, 1},
		{"largest png", pngOf(t, MaxWidth, 1), PNG, MaxWidth, 1},
	} {
		img, err := Check(c.b)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if img.Format.Ext != c.f.Ext || img.Width != c.w || img.Height != c.h {
			t.Errorf("%s: got %s %d×%d", c.name, img.Format.Ext, img.Width, img.Height)
		}
	}
}

func TestCheckRefuses(t *testing.T) {
	var g bytes.Buffer
	gif.Encode(&g, image.NewPaletted(image.Rect(0, 0, 2, 2), []color.Color{color.Black, color.White}), nil)
	animatedWebP := []byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x02\x00\x00\x00\x00\x00\x00\x00\x00\x00")
	brokenPNG := pngOf(t, 10, 10)[:20] // the header is cut off
	big := make([]byte, MaxBytes+1)
	copy(big, pngOf(t, 2, 2))
	for _, c := range []struct {
		name string
		b    []byte
		want string
	}{
		{"html", []byte("<!doctype html><title>Not found</title>"), "not a JPEG, PNG or WebP"},
		{"gif", g.Bytes(), "not a JPEG, PNG or WebP"},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), "not a JPEG, PNG or WebP"},
		{"empty", nil, "not a JPEG, PNG or WebP"},
		{"animated webp", animatedWebP, "animated"},
		{"apng", withChunk(pngOf(t, 4, 4), "acTL", make([]byte, 8)), "animated"},
		{"broken png", brokenPNG, "broken image/png"},
		{"too wide", pngOf(t, MaxWidth+1, 1), "at most 4000×6000"},
		{"too tall", pngOf(t, 1, MaxHeight+1), "at most 4000×6000"},
		{"too many bytes", big, "larger than 5 MiB"},
	} {
		if _, err := Check(c.b); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error with %q", c.name, err, c.want)
		}
	}
}

func TestPublic(t *testing.T) {
	for _, a := range []string{"93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946", "1.1.1.1"} {
		if !Public(netip.MustParseAddr(a)) {
			t.Errorf("%s should be public", a)
		}
	}
	for _, a := range []string{
		"127.0.0.1", "127.8.8.8", "::1", "0.0.0.0", "::", "10.0.0.1", "172.16.3.4", "192.168.1.1",
		"169.254.169.254", "100.64.0.1", "fd00::1", "fe80::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1",
		"224.0.0.1", "ff02::1", "255.255.255.255", "198.18.0.1", "64:ff9b::a00:1", "2002:a00:1::1", "192.0.2.1",
	} {
		if Public(netip.MustParseAddr(a)) {
			t.Errorf("%s must not be public", a)
		}
	}
}

// coverServer is a local HTTPS server standing in for a cover host, and a fetcher allowed to
// reach it.
func coverServer(t *testing.T, h http.Handler) (*httptest.Server, *Fetcher) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	pool := srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	return srv, NewFetcher(FetchOptions{AllowPrivateAddresses: true, RootCAs: pool})
}

func TestFetch(t *testing.T) {
	cover := pngOf(t, 200, 300)
	mux := http.NewServeMux()
	mux.HandleFunc("/ok.png", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
			t.Error("a cover download must send no cookie and no credentials")
		}
		w.Header().Set("Content-Type", "image/png; charset=binary")
		w.Write(cover)
	})
	mux.HandleFunc("/html", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<html></html>"))
	})
	mux.HandleFunc("/no-type", func(w http.ResponseWriter, r *http.Request) {
		w.Header()["Content-Type"] = nil
		w.Write(cover)
	})
	mux.HandleFunc("/lies.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("<html>not an image</html>"))
	})
	mux.HandleFunc("/missing.png", http.NotFound)
	mux.HandleFunc("/declared-huge.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", "6000000")
		w.Write(cover)
	})
	mux.HandleFunc("/streamed-huge.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(cover)
		chunk := make([]byte, 64<<10)
		for i := 0; i < (MaxBytes/len(chunk))+2; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	})
	mux.HandleFunc("/to-http", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/ok.png", http.StatusFound)
	})
	mux.HandleFunc("/to-ok", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ok.png", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	srv, f := coverServer(t, mux)
	ctx := context.Background()

	img, err := f.Fetch(ctx, srv.URL+"/ok.png")
	if err != nil || img.Format.Ext != PNG.Ext || img.Width != 200 || !bytes.Equal(img.Bytes, cover) {
		t.Fatalf("ok.png: %v %+v", err, img.Format)
	}
	if _, err := f.Fetch(ctx, srv.URL+"/to-ok"); err != nil {
		t.Errorf("an https redirect is followed: %v", err)
	}
	for path, want := range map[string]string{
		"/html":              "the answer is text/html",
		"/no-type":           "no Content-Type",
		"/lies.png":          "not a JPEG, PNG or WebP",
		"/missing.png":       "404",
		"/declared-huge.png": "larger than 5 MiB",
		"/streamed-huge.png": "larger than 5 MiB",
		"/to-http":           "must be https",
		"/loop":              "more than 3 redirects",
	} {
		if _, err := f.Fetch(ctx, srv.URL+path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v, want %q", path, err, want)
		}
	}
	for _, u := range []string{"http://covers.example.org/a.png", "ftp://covers.example.org/a.png", "https://user:pw@covers.example.org/a.png", "file:///etc/passwd", "https:///a.png"} {
		if _, err := f.Fetch(ctx, u); err == nil {
			t.Errorf("%s must be refused", u)
		}
	}
}

func TestTheRealFetcherRefusesLocalAddresses(t *testing.T) {
	srv, _ := coverServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the real fetcher reached a loopback server")
	}))
	f := NewFetcher(FetchOptions{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs})
	port := srv.URL[strings.LastIndex(srv.URL, ":"):]
	for _, u := range []string{
		srv.URL + "/a.png", // https://127.0.0.1:port
		"https://localhost" + port + "/a.png",
		"https://cover.local/a.png",
		"https://[::1]" + port + "/a.png",
		"https://[::ffff:127.0.0.1]" + port + "/a.png",
		"https://169.254.169.254/latest/meta-data",
		"https://10.0.0.1/a.png",
	} {
		if _, err := f.Fetch(context.Background(), u); err == nil {
			t.Errorf("%s must be refused", u)
		}
	}
	// A public-looking name that resolves to a local address is refused where it is dialled.
	if err := f.checkDialled("tcp4", "127.0.0.1:443", nil); !errors.Is(err, errPrivate) {
		t.Errorf("dialling loopback: %v", err)
	}
	if err := f.checkDialled("tcp6", "[fd12::1]:443", nil); !errors.Is(err, errPrivate) {
		t.Errorf("dialling a ULA: %v", err)
	}
	if err := f.checkDialled("tcp4", "93.184.216.34:443", nil); err != nil {
		t.Errorf("dialling a public address: %v", err)
	}
}

func TestStore(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	img, err := Check(pngOf(t, 5, 5))
	if err != nil {
		t.Fatal(err)
	}
	name, err := s.Put(img)
	if err != nil || !ValidName(name) || !strings.HasSuffix(name, ".png") {
		t.Fatalf("put: %q %v", name, err)
	}
	if again, err := s.Put(img); err != nil || again != name {
		t.Fatalf("the same bytes get the same name: %q %v", again, err)
	}
	f, format, err := s.Open(name)
	if err != nil || format.Ext != PNG.Ext {
		t.Fatalf("open: %v", err)
	}
	f.Close()
	entries, _ := os.ReadDir(filepath.Join(dir, Dir))
	if len(entries) != 1 {
		t.Fatalf("one file, no temporary leftovers: %v", entries)
	}
	for _, bad := range []string{"../abyssreader.db", name + "/x", "x.png", strings.ToUpper(name)} {
		if _, _, err := s.Open(bad); err == nil {
			t.Errorf("open %q must fail", bad)
		}
	}
	if err := s.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(name); err != nil {
		t.Fatal("removing a missing cover is no error")
	}
}
