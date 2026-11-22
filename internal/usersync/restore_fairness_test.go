// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package usersync

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"
)

// Make SQLite reject competing transactions immediately, so a push can only
// succeed if it waits its turn before trying BEGIN IMMEDIATE.
func TestPushWaitsForSyncWriter(t *testing.T) {
	s, account, db := restoreFixture(t)
	other := addUser(t, db, "other@example.com")
	ctx := t.Context()
	db.SetMaxOpenConns(2)

	// Hold the transaction that a restore batch takes. Cancel a push while
	// it is waiting. The canceled waiter must leave room for the next push.
	item := []json.RawMessage{fav(t, 1, early, false)}
	err := s.write(ctx, account, func(_ *sql.Tx, _ *int64) error {
		// This configures the pool's one remaining connection.
		if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=0"); err != nil {
			return err
		}
		wait, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		_, err := s.Push(wait, other, item)
		if err != context.DeadlineExceeded {
			return fmt.Errorf("waiting push returned %v, want deadline exceeded", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if status := push1(t, s, other, item[0]); status != "applied" {
		t.Fatal(status)
	}
}

func TestPushDuring100kMarkRestore(t *testing.T) {
	s, account, db := restoreFixture(t)
	other := addUser(t, db, "other@example.com")
	writer := s.NewRestoreWriter(account, false)
	ctx, stop := context.WithCancel(t.Context())
	defer stop()
	const total = 100000
	data := json.RawMessage(`{"read":true,"bookmarked":false}`)
	batch := func(start int) error {
		changes := make([]Change, MaxBatch)
		for i := range changes {
			changes[i] = Change{Kind: ChapterMark, Key: Key{MainMangaID: 1, SourceSlug: "paper-moon", ChapterURL: "/c/" + strconv.Itoa(start+i)}, Data: data}
		}
		out, err := writer.RestoreBatch(ctx, changes)
		if err != nil {
			return err
		}
		for _, result := range out {
			if result.Status != "applied" {
				return fmt.Errorf("restore item at batch offset %d: %+v", start, result)
			}
		}
		return nil
	}
	// Finish the initial alias scan before observing writer activity.
	if err := batch(0); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var restoreErr error
	go func() {
		defer close(done)
		for start := MaxBatch; start < total; start += MaxBatch {
			if err := batch(start); err != nil {
				restoreErr = err
				return
			}
		}
	}()
	defer func() { stop(); <-done }()

	item := []json.RawMessage{fav(t, 1, early, false)}
	// Only the restore uses a connection here; begin the push during a batch.
	wait := time.NewTicker(time.Millisecond)
	defer wait.Stop()
	for db.Stats().InUse == 0 {
		select {
		case <-wait.C:
		case <-done:
			t.Fatalf("restore ended before the push started: %v", restoreErr)
		}
	}
	const bound = 2 * time.Second // SQLite's busy timeout is five seconds.
	pushCtx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	start := time.Now()
	out, err := s.Push(pushCtx, other, item)
	elapsed := time.Since(start)
	if err != nil || len(out.Results) != 1 || out.Results[0].Status != "applied" || elapsed >= bound {
		t.Fatalf("push during restore took %s: %+v, %v", elapsed, out.Results, err)
	}
	var restored int
	if err := db.QueryRowContext(ctx, "SELECT seq FROM sync_accounts WHERE user_id=?", account).Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if restored < MaxBatch || restored >= total {
		t.Fatalf("push completed after %d of %d marks", restored, total)
	}
	t.Logf("push completed in %s with %d of %d marks committed", elapsed, restored, total)
	<-done
	if restoreErr != nil {
		t.Fatal(restoreErr)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sync_records WHERE user_id=? AND kind='chapterMark'", account).Scan(&restored); err != nil || restored != total {
		t.Fatalf("restored %d marks, want %d: %v", restored, total, err)
	}
}
