// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestLiveSupportScheduleUsesStdinRatherThanEmbeddedCopy(t *testing.T) {
	var out, stderr bytes.Buffer
	live := strings.NewReader(`{"formatVersion":1,"supportSchedule":[{"contractMajor":1,"endDate":"2099-08-01"}]}`)
	if code := supportScheduleCommand([]string{"--stdin"}, live, &out, &stderr); code != 0 || !strings.Contains(out.String(), "2099-08-01") {
		t.Fatalf("live-only date was not reported: code %d, out %q, stderr %q", code, &out, &stderr)
	}
}
