// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

func TestTitleCountsDescribeCatalogMatches(t *testing.T) {
	db := matchCatalog(t)
	entry := Entry{Title: "Forest", Favorite: false} // unique normalized name, nothing to add
	f := File{Entries: []Entry{entry, {Title: "Missing", Favorite: true}}}
	r, err := restore(t.Context(), db, &f, false, "", time.Now(), 100, func(context.Context, []usersync.Change) ([]usersync.Result, error) {
		t.Fatal("unexpected write")
		return nil, nil
	})
	if err != nil || r.Titles.MatchedByName != 1 || r.Titles.Unmatched != 1 || len(r.Unmatched) != 1 || r.Unmatched[0].Title != "Missing" {
		t.Fatalf("report %+v; err %v", r, err)
	}
}
func TestPartialRestoreDistinguishesCapacityFromCatalogMiss(t *testing.T) {
	db := matchCatalog(t)
	f := File{Entries: []Entry{{Title: "Forest", Favorite: true}, {Title: "Absent", Favorite: true}}}
	r, err := restore(t.Context(), db, &f, false, "", time.Now(), 100, func(context.Context, []usersync.Change) ([]usersync.Result, error) {
		return []usersync.Result{{Status: "rejected", Reason: "cap"}}, nil
	})
	if err == nil || r.Titles.MatchedByName != 1 || r.Titles.Unmatched != 1 || len(r.Unmatched) != 1 || r.Unmatched[0].Title != "Absent" {
		t.Fatalf("capacity treated as catalog miss: %+v err %v", r, err)
	}
}
func TestFailedMatchQueryHasNoUnmatchedEntries(t *testing.T) {
	db := matchCatalog(t)
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Minute))
	defer cancel()
	f := File{Entries: []Entry{{Title: "Forest", Favorite: true}}}
	r, err := restore(ctx, db, &f, false, "", time.Now(), 100, nil)
	if err == nil || errors.Is(err, context.Canceled) || r.Titles.Unmatched != 0 || len(r.Unmatched) != 0 {
		t.Fatalf("undecided entry reported: %+v %v", r, err)
	}
}

func TestRetainedChapterLimitRequiresSmallerFiles(t *testing.T) {
	db := matchCatalog(t)
	f := File{Entries: []Entry{{Title: "Forest", Favorite: false}}, Dropped: 1}
	r, err := restore(t.Context(), db, &f, false, "", time.Now(), 1, nil)
	if !errors.Is(err, ErrRestoreRetention) || r.Titles.MatchedByName != 1 || r.Titles.Unmatched != 0 || r.ChapterMarks.Skipped != 1 {
		t.Fatalf("lost chapter should signal an incomplete restore: %+v %v", r, err)
	}
}

func TestRetryOfPartialRestoreContinuesFromCommittedMarks(t *testing.T) {
	db := matchCatalog(t)
	if _, err := db.Exec(`INSERT INTO users(id,email,email_key,display_name,created_at,updated_at) VALUES(1,'a@x.test','a@x.test','reader',0,0)`); err != nil {
		t.Fatal(err)
	}
	svc := usersync.New(db)
	now := time.Now()
	for run, want := range []struct {
		written, stored int
		stop            error
	}{{1, 1, ErrRestoreIncomplete}, {1, 2, nil}, {0, 2, nil}} {
		file := File{Entries: []Entry{{Source: 77, URL: "/story?a=1", Chapters: []Chapter{{URL: "/one", Read: true}, {URL: "/two", Bookmark: true}}}}}
		r, err := restore(t.Context(), db, &file, false, "", now.Add(time.Duration(run)*time.Minute), 1, svc.NewRestoreWriter(1, false).RestoreBatch)
		if !errors.Is(err, want.stop) || r.ChapterMarks.Restored != want.written || r.Titles.Matched != 1 || len(r.Unmatched) != 0 {
			t.Fatalf("attempt %d: %+v; %v", run+1, r, err)
		}
		var count, seq int
		if err := db.QueryRow("SELECT COUNT(*),MAX(seq) FROM sync_records WHERE user_id=1").Scan(&count, &seq); err != nil || count != want.stored || seq != want.stored {
			t.Fatalf("attempt %d: %d marks, sequence %d; %v", run+1, count, seq, err)
		}
	}
}
