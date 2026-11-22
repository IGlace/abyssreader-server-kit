// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootRefusesPendingOperationBeforeOpeningData(t *testing.T) {
	for _, marker := range []string{".kit-operation", ".kit-restore"} {
		t.Run(marker, func(t *testing.T) {
			data := t.TempDir()
			env := map[string]string{"ABYSS_DATA_DIR": data}
			if err := os.Mkdir(filepath.Join(data, marker), 0700); err != nil {
				t.Fatal(err)
			}
			open := func() error {
				db, err := openDatabase(context.Background(), envOf(env))
				if err == nil {
					db.Close()
				}
				return err
			}
			if err := open(); err == nil {
				t.Fatal("unattended boot opened pending volume")
			}
			if _, err := os.Stat(filepath.Join(data, databaseFile)); !os.IsNotExist(err) {
				t.Fatalf("boot touched database: %v", err)
			}
			token := strings.Repeat("ab", 32)
			if err := os.WriteFile(filepath.Join(data, ".kit-boot-permit"), []byte(token), 0600); err != nil {
				t.Fatal(err)
			}
			env["ABYSS_RECOVERY_TOKEN"] = "stale"
			if err := open(); err == nil {
				t.Fatal("accepted stale token")
			}
			env["ABYSS_RECOVERY_TOKEN"] = token
			if err := open(); err != nil {
				t.Fatal(err)
			}
			if err := open(); err == nil {
				t.Fatal("reused permit on restart")
			}
		})
	}
}

func TestAdminRefusesPendingVolume(t *testing.T) {
	data := t.TempDir()
	if err := os.Mkdir(filepath.Join(data, ".kit-restore"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"import", "/import/example"}, {"backup", "now"}, {"users", "disable", "user@example.invalid"}} {
		var diagnostic bytes.Buffer
		code := run(context.Background(), append([]string{"admin"}, args...), io.Discard, &diagnostic, envOf(map[string]string{"ABYSS_DATA_DIR": data}))
		if code == 0 || !strings.Contains(diagnostic.String(), "install.sh recover") {
			t.Fatalf("%s: %d %s", args, code, &diagnostic)
		}
	}
}
