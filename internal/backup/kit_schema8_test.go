// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"path/filepath"
	"testing"
	"time"
)

func TestKitGenerationRequiresAccountBackupMigrationObjects(t *testing.T) {
	for _, query := range []string{"DROP INDEX sync_records_user_title", "DROP TABLE backup_hosted_identity", "DROP TABLE title_reports"} {
		t.Run(query, func(t *testing.T) {
			d, db, _ := fixture(t)
			if _, err := db.Exec(query); err != nil {
				t.Fatal(err)
			}
			dest, err := Create(t.Context(), d, db, 2, time.Now())
			if err == nil {
				t.Fatalf("published %s despite missing schema object", filepath.Base(dest))
			}
		})
	}
}
