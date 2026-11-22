// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package config

import "testing"

func TestReportsFeatureConfiguration(t *testing.T) {
	m := minimal()
	m["ABYSS_FEATURES"] = "reports,hosted-chapters"
	c, err := Load(env(m))
	if err != nil || !c.FeatureOn("reports") {
		t.Fatalf("expected reports enabled: %v", err)
	}
}
