// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestInstallerOrdersReleaseTags(t *testing.T) {
	data, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	functions, _, found := strings.Cut(string(data), "\nCOMMAND=install\n")
	if !found {
		t.Fatal("missing installer entry point")
	}
	for _, item := range []struct{ a, b, want string }{
		{"1.0.0-rc.10", "1.0.0-rc.2", "1"}, {"1.0.0", "1.0.0-rc.99", "1"}, {"1.0.0-rc.9", "1.0.0", "-1"},
		{"2.0.0", "10.0.0", "-1"}, {"1.11.2", "1.11.2", "0"}, {"0.0.0-ci", "0.0.1-rc.1", "-1"},
		{"9999999999999999999.0.0", "9999999999999999998.0.0", "1"}} {
		cmd := exec.Command("sh", "-c", functions+"\nrelease_order \"$1\" \"$2\"\n", "installer", item.a, item.b)
		out, err := cmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != item.want {
			t.Fatalf("%s vs %s: %s (%v)", item.a, item.b, out, err)
		}
	}
}

func TestMaintenanceReportsSupportedSchemaWithoutOpeningData(t *testing.T) {
	var out, diagnostic bytes.Buffer
	code := maintenance(context.Background(), []string{"schema-version"}, nil, &out, &diagnostic, envOf(nil))
	if code != 0 || strings.TrimSpace(out.String()) == "" {
		t.Fatalf("schema version: %d %s", code, &diagnostic)
	}
}

// A docker run can outlive its attached CLI when the host shell is killed.
func TestRecoverWaitsForDetachedVolumeHelper(t *testing.T) {
	data, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script, _, ok := strings.Cut(string(data), "\nCOMMAND=install\n")
	if !ok {
		t.Fatal("installer entry point")
	}
	script += `
load_operation() { op=operation.abcd1234; saved_phase=prepare; ACTION=restore; }
docker() { if [ "$1" = wait ] && [ "$2" = "abyssreader-operation.abcd1234" ]; then touch "$1.done"; fi; }
phase() { test -f wait.done; }
finish_operation() { :; }
recover_operation
`
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = t.TempDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("recovery raced the detached helper: %s %v", out, err)
	}
}

func TestInstalledImageReadsActualContainerTag(t *testing.T) {
	data, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script, _, ok := strings.Cut(string(data), "\nCOMMAND=install\n")
	if !ok {
		t.Fatal("installer entry point")
	}
	script += `
compose() {
 case "$1" in ps) echo container;; config) echo "kit:1.0.0";; *) return 1;; esac
}
docker() {
 [ "$1" = inspect ] && echo "kit:2.0.0"
}
[ "$(installed_image)" = kit:2.0.0 ]
`
	if out, err := exec.Command("sh", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("configured tag hid running release: %s %v", out, err)
	}
}

func TestImageInventoryRetainsPreviouslyUsedTags(t *testing.T) {
	data, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script, _, _ := strings.Cut(string(data), "\nCOMMAND=install\n")
	script += `
STATE_DIR=$PWD
remember_image caddy:2.11.9
remember_image kit:1.2.1
remember_image caddy:2.11.9
[ "$(cat "$STATE_DIR/used-images")" = "caddy:2.11.9
kit:1.2.1" ]
`
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = t.TempDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("installer lost earlier image tags: %s %v", out, err)
	}
}
