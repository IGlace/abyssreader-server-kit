// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package setup

import (
	"strings"
	"testing"
)

func TestBackupDiskAndDisabledSchedule(t *testing.T) {
	r := runSetup(t, nil, interactive("https://reader.example.org", "2", "", "Lantern", "Team", "mailto:t@example.org", "", "", "", "", "", "", "", "", "", "", "", "", "off", ""))
	if r.err != nil {
		t.Fatal(r.err)
	}
	output := r.out.String()
	if strings.Count(output, "same disk") < 2 || strings.Count(output, "docs/install.md") < 2 || strings.Contains(output, "off, keep") {
		t.Fatal("question and summary must explain off-machine copies and the disabled schedule")
	}
}
