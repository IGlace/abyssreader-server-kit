// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestRawUninstallVolumeArchiveIncludesOlderHistoryAndForeignData(t *testing.T) {
	data := t.TempDir()
	for _, p := range []string{"backups/old/abyssreader.db", ".kit-pins/old/source", "operator-notes"} {
		path := filepath.Join(data, p)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(p), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var archive bytes.Buffer
	if err := ExportStoppedVolume(t.Context(), data, &archive); err != nil {
		t.Fatal(err)
	}
	if err := CheckStoppedVolumeArchive(t.Context(), data, bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "backups/old/abyssreader.db"), []byte("other recoverable data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckStoppedVolumeArchive(t.Context(), data, bytes.NewReader(archive.Bytes())); err == nil {
		t.Fatal("accepted archive missing surviving historical data")
	}
	if err := os.Symlink("/unknown", filepath.Join(data, "foreign-link")); err != nil {
		t.Fatal(err)
	}
	if err := ExportStoppedVolume(t.Context(), data, &bytes.Buffer{}); err == nil {
		t.Fatal("followed unexpected volume link")
	}
}
