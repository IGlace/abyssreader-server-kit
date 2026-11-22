// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"math"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// maxBuckets caps how many clients a limiter remembers (about 100 bytes each). When it is full
// of clients that are still limited, a new client is limited too: the cap protects memory and
// the password hashing behind the limits, at the price of refusing new clients during a flood.
const maxBuckets = 50_000

// limiter is a per-key token bucket in memory: up to n requests per window, refilled evenly.
// The simple setup is one process, so memory is enough. n = 0 switches it off.
type limiter struct {
	n      float64
	window time.Duration
	max    int
	now    func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newLimiter(n int, window time.Duration) *limiter {
	return &limiter{n: float64(n), window: window, max: maxBuckets, now: time.Now, buckets: map[string]*bucket{}}
}

func (l *limiter) rate() float64 { return l.n / l.window.Seconds() } // tokens per second

// allow takes one token for key. When none is left it returns the whole seconds to wait.
func (l *limiter) allow(key string) (int, bool) {
	if l.n <= 0 {
		return 0, true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.bucket(key)
	if b == nil || b.tokens < 1 {
		return l.waitFor(b), false
	}
	b.tokens--
	return 0, true
}

// check reports whether key has a token left, without taking it (for limits that count only
// failures, such as wrong passwords per account).
func (l *limiter) check(key string) (int, bool) {
	if l.n <= 0 {
		return 0, true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.bucket(key)
	if b == nil || b.tokens < 1 {
		return l.waitFor(b), false
	}
	return 0, true
}

// spend takes one token for key, if there is one.
func (l *limiter) spend(key string) {
	if l.n <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if b := l.bucket(key); b != nil && b.tokens >= 1 {
		b.tokens--
	}
}

func (l *limiter) waitFor(b *bucket) int {
	missing := 1.0
	if b != nil {
		missing = 1 - b.tokens
	}
	return int(math.Ceil(missing / l.rate()))
}

// bucket returns key's bucket refilled to now, or nil when the map is full. l.mu is held.
func (l *limiter) bucket(key string) *bucket {
	now := l.now()
	if b, ok := l.buckets[key]; ok {
		b.tokens = math.Min(l.n, b.tokens+now.Sub(b.at).Seconds()*l.rate())
		b.at = now
		return b
	}
	if now.Sub(l.lastSweep) > l.window || len(l.buckets) >= l.max {
		l.sweep(now)
	}
	if len(l.buckets) >= l.max {
		return nil
	}
	b := &bucket{tokens: l.n, at: now}
	l.buckets[key] = b
	return b
}

// sweep forgets keys whose bucket has refilled: they are back to a fresh start anyway.
func (l *limiter) sweep(now time.Time) {
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.at).Seconds()*l.rate() >= l.n {
			delete(l.buckets, k)
		}
	}
	l.lastSweep = now
}

// clientKey returns the function that names a request's client for rate limits: its IPv4
// address, or its IPv6 /64 (one customer's network, so a client cannot dodge a limit by
// hopping addresses inside it). Behind a trusted proxy (ABYSS_TRUSTED_PROXIES: Caddy, the
// Docker bridge gateway, the operator's nginx, a tunnel) the client is the right-most
// X-Forwarded-For address that is not itself a trusted proxy.
//
// warn is called once when nothing is trusted and a peer is a private address: then every
// client behind that proxy shares one budget.
func clientKey(trusted []netip.Prefix, warn func(peer string)) func(*http.Request) string {
	isTrusted := func(a netip.Addr) bool {
		for _, p := range trusted {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	var warnOnce sync.Once
	return func(r *http.Request) string {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		peer, err := netip.ParseAddr(host)
		if err != nil {
			return host
		}
		peer = peer.Unmap()
		if !isTrusted(peer) {
			if len(trusted) == 0 && (peer.IsPrivate() || peer.IsLoopback()) && warn != nil {
				warnOnce.Do(func() { warn(peer.String()) })
			}
			return keyOf(peer)
		}
		hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				break
			}
			if a = a.Unmap(); !isTrusted(a) {
				return keyOf(a)
			}
		}
		return keyOf(peer)
	}
}

func keyOf(a netip.Addr) string {
	if a.Is4() {
		return a.String()
	}
	p, _ := a.Prefix(64)
	return p.String()
}
