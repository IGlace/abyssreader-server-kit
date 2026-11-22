// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyExample copies the hosted example into a folder of its own, changing its manifest.
func copyExample(t *testing.T, change func(m map[string]any)) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS(hostedExample)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	var m map[string]any
	json.Unmarshal(b, &m)
	change(m)
	b, _ = json.Marshal(m)
	os.WriteFile(filepath.Join(dir, "manifest.json"), b, 0o644)
	return dir
}

func TestRemovingHostedChaptersNeedsTheFlag(t *testing.T) {
	env := baseEnv(t)
	_, stop := serveForTest(t, env)
	stop()
	if code, out := admin_(t, env, "import", hostedExample); code != 0 {
		t.Fatalf("import = %d:\n%s", code, out)
	}
	shorter := copyExample(t, func(m map[string]any) {
		hosted := m["titles"].([]any)[0].(map[string]any)["hosted"].([]any)[0].(map[string]any)
		hosted["chapters"] = hosted["chapters"].([]any)[:2]
	})
	code, out := admin_(t, env, "import", shorter)
	if code != 1 || !strings.Contains(out, "Hosted chapters: 0 added, 0 updated, 2 unchanged, 0 removed, 0 failed") ||
		!strings.Contains(out, "  night-ferry/en/3\n") || !strings.Contains(out, "--remove-chapters") {
		t.Fatalf("without the flag = %d:\n%s", code, out)
	}
	code, out = admin_(t, env, "import", "--remove-chapters", shorter)
	if code != 0 || !strings.Contains(out, "2 unchanged, 1 removed, 0 failed") || !strings.Contains(out, "Removed hosted chapters:\n  night-ferry/en/3\n") {
		t.Fatalf("with the flag = %d:\n%s", code, out)
	}
	if code, out := admin_(t, env, "import", "--remove-chapters"); code != 2 {
		t.Fatalf("a flag without a folder = %d: %s", code, out)
	}
}

func TestTwoAdminCommandsCannotChangeTheCatalogAtOnce(t *testing.T) {
	env := baseEnv(t)
	_, stop := serveForTest(t, env)
	stop()
	unlock, err := lockData(env["ABYSS_DATA_DIR"])
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"import", hostedExample}, {"titles", "remove", "night-ferry"}, {"covers", "retry"}} {
		code, out := admin_(t, env, args...)
		if code != 1 || !strings.Contains(out, "Another admin command is changing the catalog") {
			t.Errorf("%v while locked = %d: %s", args, code, out)
		}
	}
	unlock()
	if code, out := admin_(t, env, "import", hostedExample); code != 0 {
		t.Fatalf("after the unlock = %d: %s", code, out)
	}
}
