// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestFinalArchiveDeletionGuardAndExtraConfirmation(t *testing.T) {
	source, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	prefix, _, _ := strings.Cut(string(source), "\nCOMMAND=install\n")
	for _, c := range []struct {
		guard, reply string
		ok           bool
	}{{"denied", "DELETE INCOMPLETE", false}, {"missing", "", false}, {"missing", "unrelated", false}, {"missing", "DELETE INCOMPLETE", true}, {"complete", "", true}} {
		script := prefix + `
helper(){ case "$1" in deletion-guard) case "$GUARD" in denied) echo "local copy available" >&2;return 1;; missing) echo 'covers/lost.png';; esac;; *) return 1;; esac; }
confirm_missing_images test-generation
`
		cmd := exec.Command("dash", "-c", script)
		cmd.Env = append(os.Environ(), "GUARD="+c.guard)
		cmd.Stdin = strings.NewReader(c.reply + "\n")
		out, err := cmd.CombinedOutput()
		if (err == nil) != c.ok || c.guard == "missing" && !strings.Contains(string(out), "covers/lost.png") {
			t.Fatalf("%s %q: %v %s", c.guard, c.reply, err, out)
		}
	}
}

func TestUninstallRecoveryRechecksTheOnlyExternalArchive(t *testing.T) {
	source, err := os.ReadFile("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	prefix, _, _ := strings.Cut(string(source), "\nCOMMAND=install\n")
	script := prefix + `
WORK_DIR="$1/state"
archive_dir="$1/saved"
mkdir "$WORK_DIR" "$archive_dir"
sync_state(){ :; }
printf '%s\n' "$archive_dir" >"$WORK_DIR/final-archive-dir"
printf verified >"$archive_dir/backup.tar"
remember_final_archive backup.tar
check_final_archives
printf raw >"$archive_dir/full-volume.tar"
remember_final_archive full-volume.tar
touch "$WORK_DIR/raw-final"
check_final_archives
printf changed >"$archive_dir/full-volume.tar"
if check_final_archives; then exit 1; fi
rm "$archive_dir/backup.tar"
if check_final_archives; then exit 1; fi
`
	out, err := exec.Command("dash", "-c", script, "sh", t.TempDir()).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestDeletionChecksTheExportedGenerationIdentity(t *testing.T) {
	env := envOf(baseEnv(t))
	db, err := openDatabase(t.Context(), env)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var out, diag bytes.Buffer
	call := func(in io.Reader, args ...string) int {
		out.Reset()
		diag.Reset()
		return maintenance(t.Context(), args, in, &out, &diag, env)
	}
	if call(nil, "snapshot-preserve", "14", "1.0.0") != 0 {
		t.Fatal(diag.String())
	}
	before := strings.TrimSpace(out.String())
	if call(nil, "export-preserved", before) != 0 {
		t.Fatal(diag.String())
	}
	ar := bytes.Clone(out.Bytes())
	if call(nil, "snapshot-preserve", "14", "1.0.0") != 0 {
		t.Fatal(diag.String())
	}
	after := strings.TrimSpace(out.String())
	if call(bytes.NewReader(ar), "verify-preserved", before) != 0 {
		t.Fatalf("correct exported snapshot refused: %s", &diag)
	}
	if call(bytes.NewReader(ar), "verify-preserved", after) == 0 || !strings.Contains(diag.String(), "different generation") {
		t.Fatalf("uninstall accepted the wrong valid final archive: %s", &diag)
	}
}
