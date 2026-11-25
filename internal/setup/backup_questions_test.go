// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package setup

import (
	"strings"
	"testing"
)

func TestBackupScheduleQuestions(t *testing.T) {
	r := runSetup(t, nil, interactive("https://reader.example.org", "2", "", "Lantern", "Team", "mailto:t@example.org", "", "", "", "", "", "", "", "", "", "", "", "", "weekly", "12", ""))
	if r.err != nil || !strings.Contains(r.out.String(), "Backups:          weekly, keep 12") {
		t.Fatalf("missing weekly backup summary (%v)", r.err)
	}
}
