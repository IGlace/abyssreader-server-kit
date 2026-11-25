// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package config

import (
	"slices"
	"strings"
	"testing"
)

func TestOptionalFeaturesAreOffByDefault(t *testing.T) {
	c, err := Load(env(minimal()))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Features) != 0 || c.FeatureOn(HostedChapters) {
		t.Fatalf("features: %v", c.Features)
	}
}

func TestSwitchingFeaturesOn(t *testing.T) {
	m := minimal()
	m["ABYSS_FEATURES"] = " hosted-chapters , hosted-chapters,"
	m["ABYSS_HOSTED_REQUESTS_PER_MINUTE"] = "30"
	m["ABYSS_PAGE_IMAGES_PER_MINUTE"] = "0"
	c, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Features, []string{"hosted-chapters"}) || !c.FeatureOn(HostedChapters) || c.FeatureOn("reports") {
		t.Fatalf("features: %v", c.Features)
	}
	if c.HostedRequestsPerMinute != 30 || c.PageImagesPerMinute != 0 {
		t.Fatalf("hosted limits: %d %d", c.HostedRequestsPerMinute, c.PageImagesPerMinute)
	}
}

func TestUnknownOrUnbuiltFeaturesAreRefused(t *testing.T) {
	for value, want := range map[string]string{
		"hosted-chapter":             `"hosted-chapter" is not an optional feature`,
		"Hosted-Chapters":            `"Hosted-Chapters" is not an optional feature`,
		"google-sign-in":             `"google-sign-in" is not in this kit version yet`,
		"hosted-chapters;collection": `is not an optional feature`,
	} {
		m := minimal()
		m["ABYSS_FEATURES"] = value
		_, err := Load(env(m))
		if err == nil || !strings.Contains(err.Error(), "ABYSS_FEATURES") || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v", value, err)
		}
	}
	m := minimal()
	m["ABYSS_PAGE_IMAGES_PER_MINUTE"] = "-1"
	if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "ABYSS_PAGE_IMAGES_PER_MINUTE") {
		t.Errorf("a negative limit: %v", err)
	}
}
