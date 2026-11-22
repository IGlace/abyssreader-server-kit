// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/config"
)

func TestHostedIPv6BudgetUsesRealClientPrefix(t *testing.T) {
	type client struct{ remote, forwarded string }
	for _, scenario := range []struct {
		name    string
		clients []client
		want    []int
	}{
		{
			name: "direct",
			clients: []client{
				{remote: "[2001:db8:1:1::a]:1234"},
				{remote: "[2001:db8:1:1::b]:1235"},
				{remote: "[2001:db8:1:2::a]:1236"},
			},
			want: []int{400, 429, 400},
		},
		{
			name: "trusted proxy received IPv6",
			clients: []client{
				{remote: "172.19.0.2:1234", forwarded: "2001:db8:1:1::a"},
				{remote: "172.19.0.2:1235", forwarded: "2001:db8:1:1::b"},
				{remote: "172.19.0.2:1236", forwarded: "2001:db8:1:2::a"},
			},
			want: []int{400, 429, 400},
		},
		{
			name: "proxy only received the bridge gateway",
			clients: []client{
				{remote: "172.19.0.2:1234", forwarded: "172.19.0.1"},
				{remote: "172.19.0.2:1235", forwarded: "172.19.0.1"},
			},
			want: []int{400, 429},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			handler := New(Options{
				Config: config.Config{
					Features:                []string{config.HostedChapters},
					TrustedProxies:          []netip.Prefix{netip.MustParsePrefix("172.19.0.0/16")},
					HostedRequestsPerMinute: 1,
				},
				Logger: slog.New(slog.DiscardHandler),
			})
			for i, client := range scenario.clients {
				// An invalid ID exercises the limiter without catalog setup.
				r := httptest.NewRequest("GET", BasePath+"/source-titles/bad-id/chapters", nil)
				r.RemoteAddr = client.remote
				if client.forwarded != "" {
					r.Header.Set("X-Forwarded-For", client.forwarded)
				}
				w := httptest.NewRecorder()
				handler.ServeHTTP(w, r)
				if w.Code != scenario.want[i] || (w.Code == http.StatusTooManyRequests && w.Header().Get("Retry-After") == "") {
					t.Fatalf("request %d got HTTP %d, want %d, headers %v", i+1, w.Code, scenario.want[i], w.Header())
				}
			}
		})
	}
}
