// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package setup

import (
	"strings"
	"testing"
)

func TestCaddyDNSChecksEveryRecord(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resolved []string
		stale    string
	}{
		{"A", []string{"8.8.8.8", "1.1.1.1", "2001:4860:4860::8888"}, "1.1.1.1"},
		{"AAAA", []string{"8.8.8.8", "2001:4860:4860::8888", "2606:4700:4700::1111"}, "2606:4700:4700::1111"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runSetup(t, map[string]string{
				AnswersFile:   caddyAnswers,
				HostAddrsFile: "8.8.8.8 2001:4860:4860::8888",
			}, Options{Lookup: lookupTo(tc.resolved...)})
			if r.err == nil || !strings.Contains(r.err.Error(), tc.stale) {
				t.Fatalf("want setup to refuse the stale %s record %s, got %v", tc.name, tc.stale, r.err)
			}
			r.noOutput(t)
		})
	}
}

func TestCaddyDNSCannotVerifyNATByFamily(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resolved []string
	}{
		{"A only", []string{"8.8.8.8"}},
		{"A and matching AAAA", []string{"8.8.8.8", "2001:4860:4860::8888"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := runSetup(t, map[string]string{
				AnswersFile:   caddyAnswers,
				HostAddrsFile: "192.168.1.10 2001:4860:4860::8888",
			}, Options{Lookup: lookupTo(tc.resolved...)})
			if r.err != nil {
				t.Fatal(r.err)
			}
			if msg := r.out.String(); !strings.Contains(msg, "cannot tell") || !strings.Contains(msg, "IPv4") {
				t.Fatalf("want a warning for the unverifiable IPv4 record, got %q", msg)
			}
		})
	}
}
