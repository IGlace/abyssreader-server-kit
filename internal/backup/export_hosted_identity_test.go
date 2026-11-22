// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

func TestForeignKitHostedChapterMarksStayWithOrigin(t *testing.T) {
	origin := exportDatabase(t)
	destination := exportDatabase(t)
	exportRecord(t, origin, 1, 2, 1000, "chapterMark", "hosted", "/chapters/10", `{"bookmarked":true}`)
	exportRecord(t, destination, 1, 2, 1000, "chapterMark", "hosted", "/chapters/10", `{"read":true}`)
	// Each independent catalog owns its local row 4 and chapter 10.
	if _, err := destination.Exec("UPDATE titles SET name='Other series',name_key='other series' WHERE id=2"); err != nil {
		t.Fatal(err)
	}
	local := runExport(t, destination, true)
	foreignID := runExport(t, origin, true).Entries[0].Source
	localID := local.Entries[0].Source
	if foreignID >= 0 || localID >= 0 || foreignID == localID {
		t.Errorf("independent kit identities: origin %d, destination %d", foreignID, localID)
	}
	// Neither a foreign website nor a Mihon re-export without field 9100
	// establishes ownership of the destination’s local chapter IDs.
	for _, name := range []string{"foreign website", "no field 9100"} {
		t.Run(name, func(t *testing.T) {
			file := runExport(t, origin, true)
			if name == "no field 9100" {
				file.Entries[0].Extra = nil
			}
			report, err := Restore(t.Context(), destination, usersync.New(destination), 2, file, true, "https://other.example")
			if err != nil || report.Titles.Unmatched != 1 || report.ChapterMarks.Skipped != 1 || report.ChapterMarks.Restored != 0 {
				t.Errorf("foreign hosted backup: %+v, %v", report, err)
			}
			var marks int
			if err := destination.QueryRow("SELECT count(*) FROM sync_records WHERE user_id=2 AND kind='chapterMark'").Scan(&marks); err != nil || marks != 0 {
				t.Fatalf("destination has %d marks, %v", marks, err)
			}
		})
	}
	// An explicit matching website and path is still the first contract match.
	file := runExport(t, origin, true)
	report, err := Restore(t.Context(), destination, usersync.New(destination), 2, file, true, "https://kit.example")
	if err != nil || report.Titles.Matched != 1 || report.ChapterMarks.Restored != 1 {
		t.Errorf("explicit website hit: %+v, %v", report, err)
	}
}
