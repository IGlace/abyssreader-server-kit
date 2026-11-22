// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"net/http"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/config"
)

func TestUnavailableOptionalOperationsReturnFeatureDisabled(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Features = []string{"backup", "hosted-chapters"}
		c.AccessKey = "test-access-key"
	})
	for _, route := range []struct {
		path    string
		methods []string
	}{
		{"/titles/17/comments", []string{"GET", "POST"}},
		{"/comments/17", []string{"PUT", "DELETE"}},
		{"/comments/17/replies", []string{"GET", "POST"}},
		{"/comments/17/vote", []string{"PUT"}},
		{"/comments/17/reports", []string{"POST"}},
		{"/collections", []string{"GET", "POST"}},
		{"/me/collections", []string{"GET"}},
		{"/collections/17", []string{"GET", "PUT", "DELETE"}},
		{"/collections/by-slug/comments", []string{"GET"}},
		{"/collections/17/titles", []string{"GET"}},
		{"/collections/17/vote", []string{"PUT"}},
		{"/collections/17/reports", []string{"POST"}},
		{"/collections/17/import", []string{"POST"}},
		{"/collections/17/comments", []string{"GET", "POST"}},
		{"/collection-comments/17", []string{"PUT", "DELETE"}},
		{"/collection-comments/17/replies", []string{"GET", "POST"}},
		{"/collection-comments/17/vote", []string{"PUT"}},
		{"/collection-comments/17/reports", []string{"POST"}},
		{"/me/stats", []string{"GET"}},
		{"/rankings", []string{"GET"}},
		{"/auth/google/token", []string{"POST"}},
	} {
		for _, method := range route.methods {
			t.Run(method+" "+route.path, func(t *testing.T) {
				sub := *h
				sub.t = t
				sub.expect(sub.do(method, BasePath+route.path, req{accessKey: h.cfg.AccessKey}), http.StatusNotFound, "feature_disabled")
			})
		}
	}
	h.expect(h.do("GET", BasePath+"/auth/google/start", req{}), http.StatusNotFound, "feature_disabled")
	h.expect(h.do("POST", BasePath+"/auth/google/token", req{}), http.StatusForbidden, "access_key_required")
	h.expect(h.do("GET", BasePath+"/titles/by-slug/comments", req{accessKey: h.cfg.AccessKey}), http.StatusNotFound, "not_found")
	h.expect(h.do("GET", BasePath+"/collections/17/unknown", req{accessKey: h.cfg.AccessKey, noCheck: true}), http.StatusNotFound, "not_found")
}
