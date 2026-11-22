// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mod(t *testing.T, files map[string]string) module {
	dir := t.TempDir()
	for name, body := range files {
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
	}
	return module{Path: "example.org/" + filepath.Base(dir), Version: "v1.0.0", Dir: dir}
}

func TestPermissiveLicencesAreWritten(t *testing.T) {
	var b bytes.Buffer
	m := mod(t, map[string]string{"LICENSE": "MIT License\nPermission is hereby granted", "LICENSE-GO": "BSD", "main.go": "x"})
	if err := write(&b, []module{m}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Go go", "Permission is hereby granted", "(LICENSE-GO)", m.Path + " v1.0.0"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("notices lack %q", want)
		}
	}
	if strings.Contains(b.String(), "(main.go)") {
		t.Error("a source file was taken for a licence")
	}
}

// MPL-2.0 names the GNU licences as "Secondary Licenses"; a mention is not a GPL licence.
func TestAMentionIsNotALicence(t *testing.T) {
	var b bytes.Buffer
	mpl := "1.12. \"Secondary License\"\n      means either the GNU General Public License, Version 2.0, the GNU Lesser\n      General Public License, Version 2.1"
	if err := write(&b, []module{mod(t, map[string]string{"LICENSE": mpl})}); err != nil {
		t.Fatalf("a mention was refused: %v", err)
	}
}

func TestForbiddenOrMissingLicencesFail(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"GPL":                        {"COPYING": "                    GNU GENERAL PUBLIC LICENSE\n                       Version 3, 29 June 2007"},
		"AGPL":                       {"LICENSE": "GNU Affero General Public License\nVersion 3"},
		"LGPL":                       {"LICENSE.txt": "\t\t   GNU LESSER GENERAL PUBLIC LICENSE\n"},
		"SSPL":                       {"LICENSE": "Server Side Public License\nVERSION 1, OCTOBER 16, 2018"},
		"GPL in a bundle of notices": {"LICENSE-3RD-PARTY.md": "## libfoo\n\n### GNU General Public License\n"},
		"missing":                    {"README.md": "no licence here"},
	} {
		var b bytes.Buffer
		if err := write(&b, []module{mod(t, files)}); err == nil {
			t.Errorf("%s: not refused", name)
		}
	}
}

// The real program's dependencies pass.
func TestTheServerLinksOnlyAllowedLicences(t *testing.T) {
	mods, err := linkedModules("../../cmd/abyssreader-server")
	if err != nil {
		t.Fatal(err)
	}
	if len(mods) < 5 {
		t.Fatalf("only %d modules found", len(mods))
	}
	var b bytes.Buffer
	if err := write(&b, mods); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "modernc.org/sqlite") {
		t.Fatal("modernc.org/sqlite missing from the notices")
	}
}
