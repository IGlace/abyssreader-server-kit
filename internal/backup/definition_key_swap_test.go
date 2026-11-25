// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/definitions"
)

func TestDefinitionKeyRestoreResumeBeforeAndAfterKeyRename(t *testing.T) {
	ctx := t.Context()
	source, db, _ := fixture(t)
	check := func(t *testing.T, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	readKey := func(t *testing.T, data string) []byte {
		t.Helper()
		key, err := os.ReadFile(definitions.KeyPath(data))
		check(t, err)
		return key
	}
	check(t, definitions.EnsureLocalKey(source))
	newKey := readKey(t, source)
	generation, err := Create(ctx, source, db, 1, time.Now())
	check(t, err)
	var archive bytes.Buffer
	check(t, ExportGeneration(ctx, generation, &archive))

	for _, originalKey := range []bool{false, true} {
		for _, keyInstalled := range []bool{false, true} {
			t.Run(fmt.Sprintf("original-%t/installed-%t", originalKey, keyInstalled), func(t *testing.T) {
				data := t.TempDir()
				check(t, definitions.EnsureSecretsFolder(data))
				var oldKey []byte
				if originalKey {
					check(t, definitions.EnsureLocalKey(data))
					oldKey = readKey(t, data)
				}
				check(t, Stage(ctx, data, bytes.NewReader(archive.Bytes())))
				tx := filepath.Join(data, transaction)
				if originalKey {
					check(t, mark(tx, definitionRestoreOverride))
				}
				if keyInstalled {
					check(t, ResumeApply(data))
					check(t, os.Remove(filepath.Join(tx, "applied")))
				} else {
					// Interrupt after recording the choice and saving old key state,
					// before installing any new payload.
					plan, err := planDefinitionRestore(ctx, data, filepath.Join(tx, "new"), originalKey)
					check(t, err)
					check(t, saveDefinitionRestorePlan(tx, plan))
					check(t, mark(tx, "applying"))
					if !originalKey {
						check(t, mark(filepath.Join(tx, "old"), "absent-"+definitions.KeyFile))
					}
				}
				check(t, ResumeApply(data))
				expected := newKey
				if originalKey {
					expected = oldKey
				}
				if !bytes.Equal(readKey(t, data), expected) {
					t.Fatal("resume did not preserve the existing key or recover into the keyless volume")
				}
				check(t, Undo(data))
				if originalKey {
					if !bytes.Equal(readKey(t, data), oldKey) {
						t.Fatal("undo did not restore the original key")
					}
				} else if _, err := os.Stat(definitions.KeyPath(data)); !os.IsNotExist(err) {
					t.Fatal("undo kept a key on a formerly keyless volume")
				}
			})
		}
	}
}
