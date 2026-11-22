// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package manifest

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestTachiyomiAliasesMatchNumericIdentity(t *testing.T) {
	const source = `{"slug":%q,"name":"A","website":"https://a.example.org","lang":"en","tachiyomiIds":%s}`
	parse := func(srcs ...string) (*Manifest, error) {
		return Parse([]byte(`{"manifestVersion":1,"sources":[` + strings.Join(srcs, ",") + `],"titles":[]}`))
	}
	t.Run("canonical positive aliases", func(t *testing.T) {
		m, err := parse(fmt.Sprintf(source, "a", `["00077","9223372036854775807"]`))
		if err != nil {
			t.Fatal(err)
		}
		if got := m.Sources[0].TachiyomiIDs; !slices.Equal(got, []string{"77", "9223372036854775807"}) {
			t.Fatalf("aliases = %v", got)
		}
	})
	for _, tc := range []struct {
		name, aliases, otherAliases, want string
	}{
		{"zero", `["000"]`, "", "000"},
		{"signed 64-bit overflow", `["9223372036854775808"]`, "", "9223372036854775808"},
		{"duplicate within one source", `["77","00077"]`, "", "77"},
		{"duplicate across sources", `["77"]`, `["00077"]`, "77"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srcs := []string{fmt.Sprintf(source, "a", tc.aliases)}
			if tc.otherAliases != "" {
				srcs = append(srcs, fmt.Sprintf(source, "b", tc.otherAliases))
			}
			if _, err := parse(srcs...); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected a refusal naming alias %s, got %v", tc.want, err)
			}
		})
	}
}
