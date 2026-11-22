// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Run the installer with its host directory actually absent. The Docker stub exposes
// two independently labelled candidate volumes in the ambiguous case.
func TestInstallerDiscoversOrphanWithoutHostFolder(t *testing.T) {
	src, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		volumes, args, input, want string
		success                    bool
	}{
		{"chosen_data", "recover", "", "chosen_data", false},
		{"chosen_data\nsecond", "recover --abandon", "ABANDON\n", "multiple", false},
		{"chosen_data", "recover --abandon", "CANCEL\n", "cancelled", false},
		{"chosen_data", "recover --abandon", "ABANDON\n", "preserved files", true},
		{"unlocked", "recover --abandon", "CANCEL\n", "cancelled", false},
		{"unlocked", "recover", "", "incomplete", false},
	} {
		dir := t.TempDir()
		script := filepath.Join(dir, "install.sh")
		fixture := strings.NewReplacer(
			"KIT_VERSION=unreleased", "KIT_VERSION=1.2.3",
			"KIT_IMAGE_DIGEST=unreleased", "KIT_IMAGE_DIGEST=local-test",
		).Replace(string(src))
		if err = os.WriteFile(script, []byte(fixture), 0700); err != nil {
			t.Fatal(err)
		}
		stub := `#!/bin/sh
case "$*" in
"compose version --short") echo 2.25.0 ;;
"volume ls "*)
    case "$*" in *com.docker.compose.project=chosen*com.docker.compose.volume=data*) printf '%s\n' "$VOLS";; *) exit 10;; esac ;;
"run "*"maintenance orphan-status") if [ "$VOLS" = unlocked ]; then echo "No interrupted operation is pending."; exit 0; fi; echo 'Present: /data/.kit-operation' ;;
"run "*"maintenance abandon") echo 'preserved files' ;;
"ps "*|"run "*"maintenance ready"|"run "*"maintenance describe-unlocked") ;;
"volume inspect "*|"image inspect "*|"info"|"rm -f "*) ;;
*) echo "unexpected Docker command: $*" >&2; exit 23 ;;
esac`
		if err = os.WriteFile(filepath.Join(dir, "docker"), []byte(stub), 0700); err != nil {
			t.Fatal(err)
		}
		if tc.volumes == "unlocked" {
			state := filepath.Join(dir, "lost-host", ".installer")
			if err = os.MkdirAll(state, 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(state, "pending"), []byte("incomplete"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("sh", append([]string{script}, strings.Fields(tc.args)...)...)
		cmd.Env = append(os.Environ(), "ABYSS_INSTALL_TEST=1", "ABYSS_INSTALL_DIR="+filepath.Join(dir, "lost-host"), "COMPOSE_PROJECT_NAME=chosen", "VOLS="+tc.volumes, "PATH="+dir+":"+os.Getenv("PATH"))
		cmd.Stdin = strings.NewReader(tc.input)
		out, err := cmd.CombinedOutput()
		if tc.volumes == "unlocked" {
			if _, err := os.Stat(filepath.Join(dir, "lost-host", ".installer", "pending")); err != nil {
				t.Error("moved incomplete journal without confirmation", err)
			}
		}
		if (err == nil) != tc.success || !strings.Contains(string(out), tc.want) {
			t.Errorf("%s vols=%q: %v\n%s", tc.args, tc.volumes, err, out)
		}
	}
}

func TestExistingPinSchemaIndependentOfInstalledImage(t *testing.T) {
	body, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	before, _, _ := strings.Cut(string(body), "COMMAND=install\n")
	for _, tc := range []struct {
		receipt string
		cached  bool
		want    string
	}{{"8", false, "8"}, {"0", true, "7"}, {"0", false, "0"}} {
		dir := t.TempDir()
		point := "operation.abcdefgh"
		if err = os.Mkdir(filepath.Join(dir, point), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, point, "image"), []byte("kit:1.0.0"), 0600); err != nil {
			t.Fatal(err)
		}
		script := before + `
STATE_DIR=$STATE_TEST
KIT_IMAGE_REPO=kit
max_schema=99999
helper(){ [ "$1" = pin-schema ] || exit 8; echo "$RECEIPT"; }
docker(){ case "$*" in "image inspect kit:1.0.0") [ "$CACHED" = true ];; "run --rm --pull never "*"kit:1.0.0 maintenance schema-version") echo 7;; *) exit 99;; esac; }
previous_pin_schema operation.abcdefgh
`
		cmd := exec.Command("sh", "-c", script)
		cmd.Env = append(os.Environ(), "STATE_TEST="+dir, "RECEIPT="+tc.receipt, fmt.Sprintf("CACHED=%v", tc.cached))
		out, err := cmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != tc.want {
			t.Fatalf("receipt=%s cached=%v: %s %v", tc.receipt, tc.cached, out, err)
		}
	}
}
