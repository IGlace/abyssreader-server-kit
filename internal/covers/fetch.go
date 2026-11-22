// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package covers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// The download limits. A cover URL comes from a manifest that may have been made from
// scraped pages, so it is treated as hostile.
const (
	// DialTimeout, TLSTimeout and HeaderTimeout bound each step; FetchTimeout bounds the whole
	// download, redirects and body included.
	DialTimeout   = 10 * time.Second
	TLSTimeout    = 10 * time.Second
	HeaderTimeout = 15 * time.Second
	FetchTimeout  = 30 * time.Second
	// MaxRedirects is how many redirects one download follows; every hop must be https and
	// reach a public address.
	MaxRedirects = 3
)

// FetchOptions configures a Fetcher.
type FetchOptions struct {
	UserAgent string
	// AllowPrivateAddresses and RootCAs exist for tests only, where a local HTTPS test server
	// stands in for a cover host. The admin commands never set them.
	AllowPrivateAddresses bool
	RootCAs               *x509.CertPool
}

// Fetcher downloads covers:
//   - https only, also on every redirect hop (at most MaxRedirects);
//   - never to a loopback, private, link-local, carrier-grade NAT, multicast, unspecified or
//     other non-public address, checked on the address actually dialled (so a DNS answer that
//     changes between checks cannot slip through), and never to localhost or .local names;
//   - no proxy from the environment (a proxy would hide the address being reached), no
//     cookies, no credentials;
//   - the timeouts above;
//   - a 200 answer whose Content-Type is image/jpeg, image/png or image/webp, at most MaxBytes
//     (checked on Content-Length and while reading), and bytes that pass Check.
type Fetcher struct {
	client       *http.Client
	userAgent    string
	allowPrivate bool
	timeout      time.Duration         // FetchTimeout; tests shorten it
	dialer       *net.Dialer           // checks every address it connects to (checkDialled)
	public       func(netip.Addr) bool // Public; tests stand a loopback address in for a public host
}

// NewFetcher returns a Fetcher.
func NewFetcher(o FetchOptions) *Fetcher {
	f := &Fetcher{userAgent: o.UserAgent, allowPrivate: o.AllowPrivateAddresses, timeout: FetchTimeout, public: Public}
	if f.userAgent == "" {
		f.userAgent = "abyssreader-server (cover import)"
	}
	f.dialer = &net.Dialer{Timeout: DialTimeout, Control: f.checkDialled}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            f.dialer.DialContext,
		TLSClientConfig:        &tls.Config{RootCAs: o.RootCAs, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    TLSTimeout,
		ResponseHeaderTimeout:  HeaderTimeout,
		MaxResponseHeaderBytes: 64 << 10,
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           8,
		IdleConnTimeout:        30 * time.Second,
	}
	f.client = &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > MaxRedirects {
				return fmt.Errorf("more than %d redirects", MaxRedirects)
			}
			return f.checkURL(req.URL)
		},
	}
	return f
}

// Fetch downloads one cover and checks it.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string) (Image, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return Image{}, errors.New("not a URL")
	}
	if err := f.checkURL(u); err != nil {
		return Image{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Image{}, errors.New("not a URL")
	}
	req.Header.Set("User-Agent", f.userAgent)
	req.Header.Set("Accept", "image/webp,image/png,image/jpeg")
	resp, err := f.client.Do(req)
	if err != nil {
		return Image{}, f.describeFetchError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Image{}, fmt.Errorf("the server answered %s", resp.Status)
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch mt {
	case "image/jpeg", "image/jpg", "image/png", "image/webp":
	case "":
		return Image{}, errors.New("the answer has no Content-Type; a cover must be image/jpeg, image/png or image/webp")
	default:
		return Image{}, fmt.Errorf("the answer is %s, not image/jpeg, image/png or image/webp", mt)
	}
	if resp.ContentLength > MaxBytes {
		return Image{}, ErrTooLarge
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxBytes+1))
	if err != nil {
		return Image{}, f.describeFetchError(err)
	}
	if len(b) > MaxBytes {
		return Image{}, ErrTooLarge
	}
	return Check(b)
}

// checkURL applies the rules a URL can show by itself: https, a host, no credentials, no
// local names, no non-public IP literal.
func (f *Fetcher) checkURL(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("%s:// is not allowed; a cover URL must be https", u.Scheme)
	}
	if u.User != nil {
		return errors.New("a cover URL must not carry a user name or password")
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return errors.New("the URL has no host")
	}
	if f.allowPrivate {
		return nil
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return fmt.Errorf("%s is a local name", host)
	}
	if ip, err := netip.ParseAddr(host); err == nil && !Public(ip) {
		return fmt.Errorf("%s is not a public address", host)
	}
	return nil
}

// checkDialled refuses a connection to a non-public address, whatever name led to it.
func (f *Fetcher) checkDialled(network, address string, _ syscall.RawConn) error {
	if f.allowPrivate {
		return nil
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("unexpected address %q", address)
	}
	if !f.public(ap.Addr()) {
		return errPrivate
	}
	return nil
}

var errPrivate = errors.New("the host resolves to an address that is not public")

// nonPublic lists ranges that are not reachable public unicast addresses, beyond what netip
// classifies itself.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),  // documentation
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),  // reserved, broadcast
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64: could reach a private IPv4 address
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001:db8::/32"), // documentation
	netip.MustParsePrefix("2002::/16"),     // 6to4: could reach a private IPv4 address
	netip.MustParsePrefix("::/96"),         // IPv4-compatible (deprecated)
	netip.MustParsePrefix("2001::/32"),     // Teredo: could reach a private IPv4 address
	netip.MustParsePrefix("fec0::/10"),     // site-local (deprecated)
	netip.MustParsePrefix("100::/64"),      // discard-only
}

// Public reports whether ip is a public unicast address a cover may be fetched from.
func Public(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	for _, p := range nonPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

func (f *Fetcher) describeFetchError(err error) error {
	switch {
	case errors.Is(err, errPrivate):
		return errPrivate
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("no complete answer within %s", f.timeout)
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		if ue.Timeout() {
			return errors.New("the server did not answer in time")
		}
		return ue.Err
	}
	return err
}
