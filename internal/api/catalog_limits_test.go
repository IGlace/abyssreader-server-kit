// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"fmt"
	"strings"
	"testing"
)

// A repeated filter takes at most maxFilterValues values.
func TestRepeatedFiltersAreCapped(t *testing.T) {
	h := newHarness(t, nil)
	h.smallCatalog()
	repeat := func(name, value string, n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = name + "=" + value
		}
		return strings.Join(parts, "&")
	}
	h.expect(h.do("GET", "/api/v1/titles?"+repeat("lang", "en", maxFilterValues), req{}), 200, "")
	for name, value := range map[string]string{"genre": "1", "excludeGenre": "1", "status": "ongoing", "lang": "en", "source": "paper-moon"} {
		a := h.do("GET", "/api/v1/titles?"+repeat(name, value, maxFilterValues+1), req{})
		h.expect(a, 400, "validation_failed")
		if got := fieldCodes(a); got != name+":too_many" {
			t.Errorf("%s: fields %s", name, got)
		}
	}
}

func (h *harness) views(slug string) float64 {
	h.t.Helper()
	return h.do("GET", "/api/v1/titles/by-slug/"+slug+"?countView=false", req{}).body["views"].(float64)
}

// A view counts once per client and title in viewWindow, and only on GET.
func TestViewsAreCountedOncePerClient(t *testing.T) {
	h := newHarness(t, nil)
	h.smallCatalog()
	id := int64(h.do("GET", "/api/v1/titles/by-slug/tidewater?countView=false", req{}).body["id"].(float64))
	path := fmt.Sprintf("/api/v1/titles/%d", id)

	h.do("HEAD", path, req{noCheck: true})
	if v := h.views("tidewater"); v != 0 {
		t.Fatalf("HEAD counted a view: %v", v)
	}
	h.expect(h.do("GET", path, req{xff: "203.0.113.9"}), 200, "")
	h.expect(h.do("GET", path, req{xff: "203.0.113.9"}), 200, "")
	h.expect(h.do("GET", "/api/v1/titles/by-slug/tidewater", req{xff: "203.0.113.9"}), 200, "")
	if v := h.views("tidewater"); v != 1 {
		t.Fatalf("one client counts once: %v", v)
	}
	h.expect(h.do("GET", path, req{xff: "198.51.100.7"}), 200, "")
	if v := h.views("tidewater"); v != 2 {
		t.Fatalf("another client counts: %v", v)
	}
	// The same client's view of another title counts.
	h.expect(h.do("GET", "/api/v1/titles/by-slug/quiet-orbit", req{xff: "203.0.113.9"}), 200, "")
	if v := h.views("quiet-orbit"); v != 1 {
		t.Fatalf("another title: %v", v)
	}
}
