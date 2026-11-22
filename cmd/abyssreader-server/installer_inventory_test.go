// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestImageCleanupHonoursOtherContainers(t *testing.T) {
	file, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	script, _, ok := strings.Cut(string(file), "\nCOMMAND=install\n")
	if !ok {
		t.Fatal("missing installer entry point")
	}
	script += `
docker() {
 case "$*" in
 "image inspect shared:1"|"image inspect unused:1") return 0;;
 "ps -aq --filter ancestor=shared:1") echo another-project-stopped-container;;
 "ps -aq --filter ancestor=unused:1") return 0;;
 "image rm unused:1") echo removed;;
 *) echo "unexpected removal or invocation: $*" >&2; exit 9;;
 esac
}
remove_image_if_unused shared:1
remove_image_if_unused unused:1
`
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "another container") || !strings.Contains(string(out), "removed") {
		t.Fatal(string(out), err)
	}
}

func TestUninstallIdentifiesCertificateDataAndTagMismatchRemedy(t *testing.T) {
	data, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"TLS certificates", "ACME account", "set the server image in compose.yaml back to $CURRENT_IMAGE", "#   sudo sh install.sh upgrade --check"} {
		if !strings.Contains(string(data), required) {
			t.Errorf("missing operator guidance %q", required)
		}
	}
}
