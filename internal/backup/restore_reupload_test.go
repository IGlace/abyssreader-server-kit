// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

func TestReuploadAfterCancellationInEachRestoreBatch(t *testing.T) {
	// Include a partial last batch. Run each cut against a fresh account, and
	// retry as an independent request with a fresh decode of the same marks.
	const marks = 1601
	const batches = 4
	for completed := 0; completed <= batches; completed++ {
		t.Run(fmt.Sprintf("after_%d_batches", completed), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				db := matchCatalog(t)
				if _, err := db.Exec(`INSERT INTO users(id,email,email_key,display_name,created_at,updated_at) VALUES(1,'a@x.test','a@x.test','reader',0,0)`); err != nil {
					t.Fatal(err)
				}
				svc := usersync.New(db)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if completed < batches {
					// Cut during the next batch while it is waiting for storage.
					time.AfterFunc(time.Duration(completed)*8*time.Second+time.Second, cancel)
				}
				writer := svc.NewRestoreWriter(1, false)
				first, err := restore(ctx, db, markedFile(marks), false, "", time.Now(), 100_000, delayedBatch(8*time.Second, writer.RestoreBatch))
				cancel()
				wantCommitted := min(completed*usersync.MaxBatch, marks)
				if first.ChapterMarks.Restored != wantCommitted || (completed < batches && !errors.Is(err, context.Canceled)) || (completed == batches && err != nil) {
					t.Fatalf("cut committed %d marks, error %v; want %d marks", first.ChapterMarks.Restored, err, wantCommitted)
				}

				writer = svc.NewRestoreWriter(1, false)
				retry, err := restore(t.Context(), db, markedFile(marks), false, "", time.Now(), 100_000, writer.RestoreBatch)
				if err != nil || retry.ChapterMarks.Restored != marks-wantCommitted || retry.ChapterMarks.Skipped != wantCommitted {
					t.Fatalf("retry committed %d marks, skipped %d, error %v", retry.ChapterMarks.Restored, retry.ChapterMarks.Skipped, err)
				}
				var count, seq int
				if err := db.QueryRow("SELECT COUNT(*), MAX(seq) FROM sync_records WHERE user_id=1").Scan(&count, &seq); err != nil || count != marks || seq != marks {
					t.Fatalf("retry left %d rows, highest sequence %d, error %v; want %d rows and sequence", count, seq, err, marks)
				}
			})
		})
	}
}
