// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package setup

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// The DNS check of the Caddy mode: Let's Encrypt gives Caddy a certificate
// only once the public address's name resolves to this machine. The setup runs in a container,
// so install.sh passes the host's own addresses (hostname -I) in a file; the setup resolves the
// name and compares. Nothing else is contacted than the resolver the host already uses.

type dnsVerdict int

const (
	dnsPointsHere dnsVerdict = iota
	// dnsCannotTell: the host has no public interface address for a DNS record's family.
	// The address may be behind NAT and work all the same.
	dnsCannotTell
	dnsWrong
)

// LookupFunc resolves a host name to its addresses (net.DefaultResolver.LookupHost).
type LookupFunc func(ctx context.Context, host string) ([]string, error)

// parseHostAddresses reads the host's addresses: separated by spaces or lines, "/prefix"
// allowed (ip -o addr), anything else ignored.
func parseHostAddresses(s string) []netip.Addr {
	var out []netip.Addr
	for _, f := range strings.Fields(s) {
		f, _, _ = strings.Cut(f, "/")
		if a, err := netip.ParseAddr(f); err == nil {
			out = append(out, a.Unmap().WithZone(""))
		}
	}
	return out
}

func public(a netip.Addr) bool {
	return a.IsGlobalUnicast() && !a.IsPrivate() && !netip.MustParsePrefix("100.64.0.0/10").Contains(a)
}

// checkDNS verifies each resolved address against the host's public addresses of that family.
func checkDNS(ctx context.Context, lookup LookupFunc, host string, hostAddrs []netip.Addr) (dnsVerdict, string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	names, err := lookup(ctx, host)
	var resolved []netip.Addr
	for _, n := range names {
		if a, err := netip.ParseAddr(n); err == nil {
			resolved = append(resolved, a.Unmap().WithZone(""))
		}
	}
	if err != nil || len(resolved) == 0 {
		return dnsWrong, fmt.Sprintf("%s does not resolve to any address yet. Add an A record (and an AAAA record if this\n"+
			"machine has IPv6) for %s pointing at this machine, wait until it resolves, then try again.", host, host)
	}
	verdict := dnsPointsHere
	var notes []string
	for _, family := range []struct {
		name, record string
		ipv4         bool
	}{{"IPv4", "A", true}, {"IPv6", "AAAA", false}} {
		records := slices.DeleteFunc(slices.Clone(resolved), func(a netip.Addr) bool { return a.Is4() != family.ipv4 })
		if len(records) == 0 {
			continue
		}
		mine := slices.DeleteFunc(slices.Clone(hostAddrs), func(a netip.Addr) bool {
			return a.Is4() != family.ipv4 || !public(a)
		})
		if len(mine) == 0 {
			verdict = max(verdict, dnsCannotTell)
			notes = append(notes, fmt.Sprintf("%s has %s records for %s. This machine has no public %s address\n"+
				"on its interfaces, so the setup cannot tell whether those records point here.", host, family.record, join(records), family.name))
			continue
		}
		mismatches := slices.DeleteFunc(records, func(a netip.Addr) bool { return slices.Contains(mine, a) })
		if len(mismatches) > 0 {
			verdict = dnsWrong
			notes = append(notes, fmt.Sprintf("%s has %s records for %s that point elsewhere.\n"+
				"This machine's public %s addresses are %s.", host, family.record, join(mismatches), family.name, join(mine)))
		}
	}
	if verdict == dnsWrong {
		notes = append(notes, "Point the name's A/AAAA records at this machine (or use a Cloudflare record without its proxy), wait\n"+
			"until it resolves, then try again. Let's Encrypt cannot give Caddy a certificate until then.")
	}
	return verdict, strings.Join(notes, "\n")
}

func join(as []netip.Addr) string {
	s := make([]string, len(as))
	for i, a := range as {
		s[i] = a.String()
	}
	return strings.Join(s, ", ")
}
