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

func markedFile(marks int) *File {
	f := &File{}
	for i := range marks {
		if i%MaxChaptersPerEntry == 0 {
			f.Entries = append(f.Entries, Entry{Source: 77, URL: "/story?a=1"})
		}
		e := &f.Entries[len(f.Entries)-1]
		e.Chapters = append(e.Chapters, Chapter{URL: fmt.Sprintf("/c/%d", i), Read: true})
	}
	return f
}

func delayedBatch(delay time.Duration, write writeBatch) writeBatch {
	return func(ctx context.Context, batch []usersync.Change) ([]usersync.Result, error) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
			return write(ctx, batch)
		}
	}
}

func appliedBatch(_ context.Context, batch []usersync.Change) ([]usersync.Result, error) {
	result := make([]usersync.Result, len(batch))
	for i := range result {
		result[i].Status = "applied"
	}
	return result, nil
}

func TestTimeLimitedRestoreCanFinishOnRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db := matchCatalog(t)
		if _, err := db.Exec("INSERT INTO users(id,email,email_key,display_name,created_at,updated_at) VALUES(1,'a@x.test','a@x.test','reader',0,0)"); err != nil {
			t.Fatal(err)
		}
		svc := usersync.New(db)
		attempt := func(ctx context.Context) (Report, error) {
			writer := svc.NewRestoreWriter(1, false)
			return restore(ctx, db, markedFile(2000), false, "", time.Now(), 100_000, delayedBatch(9*time.Second, writer.RestoreBatch))
		}

		// Model a previous upload cut off at the historical processing deadline.
		limited, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		r, err := attempt(limited)
		if !errors.Is(err, ErrRestoreIncomplete) || r.ChapterMarks.Restored != 1500 {
			t.Fatalf("first upload committed %d marks, error %v", r.ChapterMarks.Restored, err)
		}
		r, err = attempt(t.Context())
		if err != nil || r.ChapterMarks.Restored != 500 || r.ChapterMarks.Skipped != 1500 {
			t.Fatalf("replayed upload committed %d, skipped %d, error %v", r.ChapterMarks.Restored, r.ChapterMarks.Skipped, err)
		}
		var count, seq int
		if err := db.QueryRow("SELECT COUNT(*),MAX(seq) FROM sync_records WHERE user_id=1").Scan(&count, &seq); err != nil || count != 2000 || seq != 2000 {
			t.Fatalf("after replay: %d rows, sequence %d, error %v", count, seq, err)
		}
	})
}

func TestSlowStoreFinishes100000MarksInOneUpload(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db := matchCatalog(t)
		r, err := restore(t.Context(), db, markedFile(100_000), false, "", time.Now(), 100_000, delayedBatch(9*time.Second, appliedBatch))
		if err != nil || r.ChapterMarks.Restored != 100_000 || r.ChapterMarks.Skipped != 0 {
			t.Fatalf("one upload committed %d marks, skipped %d, error %v", r.ChapterMarks.Restored, r.ChapterMarks.Skipped, err)
		}
	})
}

func TestStalledRestoreBatchReturnsCommittedPrefix(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db := matchCatalog(t)
		calls := 0
		write := func(ctx context.Context, batch []usersync.Change) ([]usersync.Result, error) {
			calls++
			if calls == 1 {
				return appliedBatch(ctx, batch)
			}
			return delayedBatch(20*time.Second, appliedBatch)(ctx, batch)
		}
		r, err := restore(t.Context(), db, markedFile(1000), false, "", time.Now(), 100_000, write)
		if !errors.Is(err, ErrRestoreIncomplete) || r.ChapterMarks.Restored != 500 || r.ChapterMarks.Skipped != 500 {
			t.Fatalf("stalled upload committed %d marks, skipped %d, error %v", r.ChapterMarks.Restored, r.ChapterMarks.Skipped, err)
		}
	})
}

func TestRestoreRechecksStaleTailInBoundedBatches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db := matchCatalog(t)
		// With one write remaining after the first batch, the next 499
		// candidates are already stored. Only the last candidate needs a write.
		stored := make(map[string]bool)
		for i := 500; i < 999; i++ {
			stored[fmt.Sprintf("/c/%d", i)] = true
		}
		write := func(_ context.Context, batch []usersync.Change) ([]usersync.Result, error) {
			results := make([]usersync.Result, len(batch))
			for i, c := range batch {
				if stored[c.Key.ChapterURL] {
					results[i].Status = "stale"
				} else {
					results[i].Status = "applied"
					stored[c.Key.ChapterURL] = true
				}
			}
			return results, nil
		}
		for attempt := 1; attempt <= 2; attempt++ {
			start := time.Now()
			r, err := restore(t.Context(), db, markedFile(1000), false, "", start, 501, delayedBatch(9*time.Second, write))
			if time.Since(start) > 18*time.Second {
				t.Fatalf("upload %d rechecking a stale tail took %v for 1000 candidates", attempt, time.Since(start))
			}
			if err == nil {
				if len(stored) != 1000 {
					t.Fatalf("reported complete with %d stored marks", len(stored))
				}
				return
			}
			if !errors.Is(err, ErrRestoreIncomplete) || r.ChapterMarks.Restored == 0 {
				t.Fatalf("upload %d committed %d marks, error %v", attempt, r.ChapterMarks.Restored, err)
			}
		}
		t.Fatal("two healthy uploads failed to store the final mark")
	})
}

func TestRestoreKeepsRequestCancellationDistinctFromBatchTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db := matchCatalog(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		time.AfterFunc(time.Second, cancel)
		r, err := restore(ctx, db, markedFile(1), false, "", time.Now(), 100_000, delayedBatch(9*time.Second, appliedBatch))
		if !errors.Is(err, context.Canceled) || r.ChapterMarks.Restored != 0 {
			t.Fatalf("canceled upload committed %d marks, error %v", r.ChapterMarks.Restored, err)
		}
	})
}
