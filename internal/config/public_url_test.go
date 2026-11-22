// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package config

import (
	"strings"
	"testing"
)

func TestPublicURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://lantern.example.org":      "https://lantern.example.org",
		"https://Lantern.Example.org/":     "https://lantern.example.org",
		"https://lantern.example.org:8443": "https://lantern.example.org:8443",
		"http://127.0.0.1:8080":            "http://127.0.0.1:8080", // local testing only
		"http://localhost:8080":            "http://localhost:8080",
		"http://[::1]:8080":                "http://[::1]:8080",
	} {
		m := minimal()
		m["ABYSS_PUBLIC_URL"] = in
		c, err := Load(env(m))
		if err != nil || c.PublicURL != want {
			t.Errorf("%q: got %q %v, want %q", in, c.PublicURL, err, want)
		}
	}
	for _, bad := range []string{
		"", "lantern.example.org", "http://lantern.example.org", "http://10.0.0.5:8080", "https://lantern.example.org/kit",
		"https://lantern.example.org?x=1", "https://user@lantern.example.org", "ftp://lantern.example.org", "https://",
		"https://lantern.example.org:99999",
	} {
		m := minimal()
		m["ABYSS_PUBLIC_URL"] = bad
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "ABYSS_PUBLIC_URL") {
			t.Errorf("%q must be refused, naming ABYSS_PUBLIC_URL: %v", bad, err)
		}
	}
}
