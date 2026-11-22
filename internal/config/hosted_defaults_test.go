// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package config

import "testing"

func TestDefaultHostedBudgets(t *testing.T) {
	m := minimal()
	m["ABYSS_FEATURES"] = "hosted-chapters"
	c, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if c.HostedRequestsPerMinute != 600 || c.PageImagesPerMinute != 1800 {
		t.Fatalf("default hosted budgets: got %d requests and %d images per minute, want 600 and 1800",
			c.HostedRequestsPerMinute, c.PageImagesPerMinute)
	}
}
