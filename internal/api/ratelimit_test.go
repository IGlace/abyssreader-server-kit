// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"fmt"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestLimiterRefillsEvenly(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	l := newLimiter(2, time.Minute)
	l.now = func() time.Time { return now }
	for i := 0; i < 2; i++ {
		if _, ok := l.allow("a"); !ok {
			t.Fatalf("request %d refused", i+1)
		}
	}
	wait, ok := l.allow("a")
	if ok || wait != 30 {
		t.Fatalf("third request: wait %d, ok %v; want 30s and refused", wait, ok)
	}
	if _, ok := l.allow("b"); !ok {
		t.Fatal("another client shares the bucket")
	}
	now = now.Add(30 * time.Second)
	if _, ok := l.allow("a"); !ok {
		t.Fatal("one token is back after 30s")
	}
	if _, ok := l.allow("a"); ok {
		t.Fatal("only one token came back")
	}
	now = now.Add(time.Hour)
	l.allow("c") // sweeps the refilled buckets
	if len(l.buckets) != 1 {
		t.Fatalf("%d buckets kept after a sweep, want 1", len(l.buckets))
	}
}

func TestLimiterCheckAndSpendCountOnlyWhatIsSpent(t *testing.T) {
	l := newLimiter(2, time.Minute)
	for i := 0; i < 5; i++ {
		if _, ok := l.check("a"); !ok {
			t.Fatal("check must not take a token")
		}
	}
	l.spend("a")
	l.spend("a")
	if wait, ok := l.check("a"); ok || wait < 1 {
		t.Fatalf("after two failures: wait %d, ok %v", wait, ok)
	}
}

func TestLimiterMapIsCapped(t *testing.T) {
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	l := newLimiter(1, time.Hour)
	l.now = func() time.Time { return now }
	l.max = 100
	for i := 0; i < 100; i++ {
		if _, ok := l.allow(fmt.Sprint("client-", i)); !ok {
			t.Fatalf("client %d refused before the cap", i)
		}
	}
	// Full of clients that are still limited: a new one is limited too, and the map stays capped.
	if wait, ok := l.allow("newcomer"); ok || wait < 1 {
		t.Fatalf("newcomer at the cap: wait %d, ok %v", wait, ok)
	}
	if len(l.buckets) != 100 {
		t.Fatalf("%d buckets, cap 100", len(l.buckets))
	}
	// Once old buckets refill, there is room again.
	now = now.Add(2 * time.Hour)
	if _, ok := l.allow("newcomer"); !ok {
		t.Fatal("newcomer refused after the others refilled")
	}
}

func TestLimiterOff(t *testing.T) {
	l := newLimiter(0, time.Minute)
	for i := 0; i < 100; i++ {
		if _, ok := l.allow("a"); !ok {
			t.Fatal("a limit of 0 must be off")
		}
	}
}

func TestClientKey(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("127.0.0.1/32")}
	key := clientKey(trusted, nil)
	cases := []struct{ remote, xff, want string }{
		{"203.0.113.9:5000", "", "203.0.113.9"},
		{"203.0.113.9:5000", "198.51.100.1", "203.0.113.9"},                     // an untrusted peer cannot claim an address
		{"172.18.0.1:5000", "198.51.100.1", "198.51.100.1"},                     // through the trusted proxy
		{"172.18.0.1:5000", "6.6.6.6, 198.51.100.1, 127.0.0.1", "198.51.100.1"}, // right-most untrusted hop
		{"172.18.0.1:5000", "", "172.18.0.1"},
		{"[::ffff:172.18.0.1]:5000", "198.51.100.1", "198.51.100.1"},
		{"172.18.0.1:5000", "garbage", "172.18.0.1"},
		{"[2001:db8:1:2::a]:5000", "", "2001:db8:1:2::/64"}, // IPv6 by /64
		{"[2001:db8:1:2:ffff:1:2:3]:5000", "", "2001:db8:1:2::/64"},
		{"[2001:db8:1:3::a]:5000", "", "2001:db8:1:3::/64"},
		{"172.18.0.1:5000", "2001:db8:9:9::1", "2001:db8:9:9::/64"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := key(r); got != c.want {
			t.Errorf("remote %s, X-Forwarded-For %q: got %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
	if got := clientKey(nil, nil)(httptest.NewRequest("GET", "/", nil)); got != "192.0.2.1" {
		t.Errorf("no trusted proxies: %s", got)
	}
}

// Nothing trusted and a private peer: every client behind it shares one budget. The server says
// so once (S1).
func TestClientKeyWarnsOnceAboutAnUntrustedPrivatePeer(t *testing.T) {
	var warned []string
	key := clientKey(nil, func(peer string) { warned = append(warned, peer) })
	for _, remote := range []string{"203.0.113.9:1", "172.18.0.1:1", "172.18.0.1:2", "10.0.0.1:1"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		key(r)
	}
	if len(warned) != 1 || warned[0] != "172.18.0.1" {
		t.Fatalf("warnings: %v; want one, for 172.18.0.1", warned)
	}
}
