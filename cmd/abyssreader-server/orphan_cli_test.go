// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestOrphanCLIInspectsIdleVolumeAndRefusesEmptyLiveData(t *testing.T) {
	env := envOf(baseEnv(t))
	var out, stderr bytes.Buffer
	invoke := func(cmd string) int { return maintenance(context.Background(), []string{cmd}, nil, &out, &stderr, env) }
	if code := invoke("orphan-status"); code != 0 || !strings.Contains(out.String(), "No interrupted operation") {
		t.Fatal(code, out.String(), stderr.String())
	}
	if code := invoke("begin"); code != 0 {
		t.Fatal(code, stderr.String())
	}
	if code := invoke("abandon"); code != 1 || !strings.Contains(stderr.String(), "verify existing live") {
		t.Fatal(code, stderr.String())
	}
	if code := invoke("ready"); code != 1 {
		t.Fatal("unguarded after failed abandon", code)
	}
}
