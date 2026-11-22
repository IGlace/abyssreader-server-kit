// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package covers

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"
)

// The address check where the connection is made, through the real Fetch: a name that passes
// the URL check but resolves to a refused address is stopped, also on a redirect hop.
//
// The test server listens on 127.0.0.1. The fetcher's policy treats 127.0.0.1 as public (it
// stands in for a public host) and every other address as the real Public does. Names are
// resolved by a hook: example.com (the test certificate's name) → 127.0.0.1, and
// internal.example.com → 127.0.0.2, which the policy refuses.
func TestTheDialledAddressIsChecked(t *testing.T) {
	cover := pngOf(t, 10, 15)
	srv, _ := coverServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(cover)
		case "/to-internal":
			http.Redirect(w, r, "https://internal.example.com:"+r.URL.Query().Get("port")+"/ok.png", http.StatusFound)
		}
	}))
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
	f := NewFetcher(FetchOptions{RootCAs: srv.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs})
	stand := netip.MustParseAddr("127.0.0.1")
	f.public = func(a netip.Addr) bool { return a == stand || Public(a) }
	resolved := map[string]string{"example.com": "127.0.0.1", "internal.example.com": "127.0.0.2"}
	f.client.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, p, _ := net.SplitHostPort(addr)
		if ip, ok := resolved[host]; ok {
			addr = net.JoinHostPort(ip, p)
		}
		return f.dialer.DialContext(ctx, network, addr)
	}
	ctx := context.Background()

	if _, err := f.Fetch(ctx, "https://example.com:"+port+"/ok.png"); err != nil {
		t.Fatalf("the stand-in public host: %v", err)
	}
	if _, err := f.Fetch(ctx, "https://internal.example.com:"+port+"/ok.png"); !errors.Is(err, errPrivate) {
		t.Fatalf("a name resolving to a refused address: %v", err)
	}
	if _, err := f.Fetch(ctx, "https://example.com:"+port+"/to-internal?port="+port); !errors.Is(err, errPrivate) {
		t.Fatalf("a redirect to a name resolving to a refused address: %v", err)
	}
}

func TestPublicRefusesTunnelsAndOldRanges(t *testing.T) {
	for _, a := range []string{
		"::a00:1",        // IPv4-compatible ::/96
		"::7f00:1",       // IPv4-compatible loopback
		"2001:0:4136::1", // Teredo 2001::/32
		"fec0::1",        // site-local fec0::/10
		"100::1",         // discard 100::/64
	} {
		if Public(netip.MustParseAddr(a)) {
			t.Errorf("%s must not be public", a)
		}
	}
	if !Public(netip.MustParseAddr("2001:4860:4860::8888")) {
		t.Error("2001:4860::/32 is public")
	}
}
