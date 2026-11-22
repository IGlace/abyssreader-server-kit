// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package config

import (
	"fmt"
	"slices"
	"strings"

	kitfeatures "github.com/IGlace/abyssreader-server-kit/internal/features"
)

// The optional features, by their /api/status names. ABYSS_FEATURES switches them on;
// all are off by default. A feature that is off answers 404
// feature_disabled on every one of its routes and keeps its data.
const HostedChapters = kitfeatures.HostedChapters

// Built exposes the names the installer can offer.
var Built = func() []string {
	var names []string
	for _, f := range kitfeatures.Built {
		names = append(names, f.Name)
	}
	return names
}()

// contractFeatures are every optional feature of contract v1 (its Feature enum).
var contractFeatures = []string{"comments", "collections", "stats", "reports", "backup", "google-sign-in", "hosted-chapters"}

// FeatureOn reports whether the operator switched the optional feature name on.
func (c Config) FeatureOn(name string) bool { return slices.Contains(c.Features, name) }

// features reads ABYSS_FEATURES: a comma-separated list of optional feature names. It returns
// them sorted, each once, and one problem per name it cannot switch on.
func features(v string) ([]string, []string) {
	var on, problems []string
	for _, part := range strings.Split(v, ",") {
		name := strings.TrimSpace(part)
		switch {
		case name == "":
		case slices.Contains(Built, name):
			if !slices.Contains(on, name) {
				on = append(on, name)
			}
		case slices.Contains(contractFeatures, name):
			problems = append(problems, fmt.Sprintf("%q is not in this kit version yet", name))
		default:
			problems = append(problems, fmt.Sprintf("%q is not an optional feature (known: %s)", name, strings.Join(Built, ", ")))
		}
	}
	slices.Sort(on)
	return on, problems
}
