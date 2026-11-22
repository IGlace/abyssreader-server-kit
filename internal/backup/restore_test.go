// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"context"
	"errors"
	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
	"testing"
	"time"
)

func TestRestoreBudgetReportsCommittedWork(t *testing.T) {
	entry := Entry{Title: "Forest", Favorite: true, Source: 77, URL: "/story?a=1", Chapters: []Chapter{{URL: "/c1", Read: true}, {URL: "/c2", Bookmark: true}}}
	db := matchCatalog(t)
	now := time.Now()
	calls := 0
	write := func(ctx context.Context, items []usersync.Change) ([]usersync.Result, error) {
		calls++
		if len(items) > 2 {
			t.Fatalf("large batch %d", len(items))
		}
		result := make([]usersync.Result, len(items))
		for i := range items {
			result[i].Status = "applied"
		}
		return result, nil
	}
	f := File{Entries: []Entry{entry, entry}}
	r, err := restore(t.Context(), db, &f, false, "", now, 2, write)
	if !errors.Is(err, ErrRestoreIncomplete) {
		t.Fatal(err)
	}
	if calls != 1 || r.Titles.Matched != 2 || r.Titles.Unmatched != 0 || r.ChapterMarks.Restored != 1 || r.ChapterMarks.Skipped != 3 {
		t.Fatalf("budget: %+v (calls %d)", r, calls)
	}
	expired, cancel := context.WithDeadline(t.Context(), now.Add(-time.Second))
	defer cancel()
	f = File{Entries: []Entry{entry, entry}}
	r, err = restore(expired, db, &f, false, "", now, 100, write)
	if !errors.Is(err, ErrRestoreIncomplete) || calls != 1 || r.Titles.Unmatched != 0 || r.ChapterMarks.Skipped != 4 {
		t.Fatalf("deadline: %+v, %v", r, err)
	}
}
func TestRestoreAtStorageCeilingReportsUnwrittenListAndMark(t *testing.T) {
	db := matchCatalog(t)
	f := File{Entries: []Entry{{URL: "/story?a=1", Source: 77, Favorite: true, Chapters: []Chapter{{URL: "/one", Read: true}}}}}
	r, err := restore(t.Context(), db, &f, false, "", time.Now(), 10, func(_ context.Context, items []usersync.Change) ([]usersync.Result, error) {
		out := make([]usersync.Result, len(items))
		for i := range out {
			out[i] = usersync.Result{Status: "rejected", Reason: "cap"}
		}
		return out, nil
	})
	if !errors.Is(err, ErrRestoreCapacity) || r.Titles.Matched != 1 || r.Titles.Unmatched != 0 || r.ChapterMarks.Skipped != 1 || len(r.Unmatched) != 0 {
		t.Fatalf("cap: %+v %v", r, err)
	}
}

func TestBackupRecordTimesUseFileOrRestoreTime(t *testing.T) {
	now := time.Date(2026, 11, 21, 10, 0, 0, 50_000_000, time.UTC)
	for _, tc := range []struct{ given, want int64 }{
		{0, now.UnixMilli()}, {-1, now.UnixMilli()}, {1700000000, 1700000000000}, {1700000000001, 1700000000001},
		{now.Add(3 * time.Minute).UnixMilli(), now.Add(3 * time.Minute).UnixMilli()}, {now.Add(6 * time.Minute).UnixMilli(), now.UnixMilli()},
	} {
		if got := backupRecordMillis(tc.given, now); got != tc.want {
			t.Errorf("time %d: got %d want %d", tc.given, got, tc.want)
		}
	}
}
