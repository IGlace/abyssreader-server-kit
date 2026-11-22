// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package usersync

import (
	"encoding/json"
	"testing"
	"time"
)

func replayFavorite(t *testing.T, id, at string, deleted bool) json.RawMessage {
	t.Helper()
	return raw(t, map[string]any{"opId": id, "kind": "favorite", "key": map[string]any{"mainMangaId": 1},
		"data": nil, "clientUpdatedAt": at, "deleted": deleted})
}

func receiptPush(t *testing.T, s *Service, user int64, item json.RawMessage) PushOutcome {
	t.Helper()
	out, err := s.Push(t.Context(), user, []json.RawMessage{item})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestReplayReceiptKeepsTheRemoval(t *testing.T) {
	db := testDB(t)
	user := addUser(t, db, "lost-ack@example.com")
	addTitle(t, db, 1)
	now := mustTime(t, "2026-11-22T10:00:00.000Z")
	s := New(db).WithClock(func() time.Time { return now })
	add := replayFavorite(t, "00000000-0000-4000-8000-000000000101", "2026-11-22T11:00:00.000Z", false)
	first := receiptPush(t, s, user, add)
	if first.Results[0].Status != "applied" {
		t.Fatalf("add: %+v", first)
	}
	now = now.Add(time.Minute)
	remove := receiptPush(t, s, user, replayFavorite(t, "00000000-0000-4000-8000-000000000102",
		"2026-11-22T10:01:00.000Z", true))
	now = now.Add(time.Minute)
	replay := receiptPush(t, s, user, add)
	if replay.Results[0] != first.Results[0] || replay.Cursor != remove.Cursor {
		t.Fatalf("replay %+v; want the original result %+v at the removal cursor %q", replay, first.Results[0], remove.Cursor)
	}
	zero := "d.0"
	changes, _ := pullAll(t, s, user, &zero, 10)
	if len(changes) != 1 || !changes[0].Deleted {
		t.Fatalf("replay resurrected the favorite: %+v", changes)
	}
}

func TestReplayReceiptIsScopedToAccount(t *testing.T) {
	db := testDB(t)
	a, b := addUser(t, db, "replay-a@example.com"), addUser(t, db, "replay-b@example.com")
	addTitle(t, db, 1)
	s := New(db)
	item := replayFavorite(t, "00000000-0000-4000-8000-000000000103", "2026-11-22T10:00:00.000Z", false)
	for _, user := range []int64{a, b} {
		receiptPush(t, s, user, item)
		if changes, _ := pullAll(t, s, user, nil, 10); len(changes) != 1 || changes[0].Deleted {
			t.Fatalf("account %d did not apply its own operation: %+v", user, changes)
		}
	}
}

func TestReplayReceiptPruning(t *testing.T) {
	db := testDB(t)
	user := addUser(t, db, "replay-age@example.com")
	addTitle(t, db, 1)
	now := mustTime(t, "2026-11-22T10:00:00.000Z")
	s := New(db).WithClock(func() time.Time { return now })
	oldID, newID := "00000000-0000-4000-8000-000000000104", "00000000-0000-4000-8000-000000000105"
	receiptPush(t, s, user, replayFavorite(t, oldID, "2026-11-22T10:00:00.000Z", false))
	now = now.Add(time.Minute)
	receiptPush(t, s, user, replayFavorite(t, newID, "2026-11-22T10:01:00.000Z", true))
	now = now.Add(TombstoneLifetime - time.Minute + time.Millisecond)
	if _, err := s.PurgeTombstones(t.Context(), now.Add(-TombstoneLifetime)); err != nil {
		t.Fatal(err)
	}
	var retained int
	if err := db.QueryRow("SELECT count(*) FROM sync_applied_operations WHERE user_id=?", user).Scan(&retained); err != nil || retained != 2 {
		t.Fatalf("receipts must outlive tombstone retention: %d, %v", retained, err)
	}
	now = now.Add(receiptLifetime - TombstoneLifetime)
	if _, err := s.PurgeTombstones(t.Context(), now.Add(-TombstoneLifetime)); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRow("SELECT op_id FROM sync_applied_operations WHERE user_id=?", user).Scan(&id); err != nil || id != newID {
		t.Fatalf("only recent receipt should remain, got %q: %v", id, err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sync_applied_operations WHERE user_id=?", user).Scan(&n); err != nil || n != 1 {
		t.Fatalf("remaining receipts: %d, %v", n, err)
	}
}

func TestReplayReceiptFailureRollsBackTheWrite(t *testing.T) {
	db := testDB(t)
	user := addUser(t, db, "receipt-rollback@example.com")
	addTitle(t, db, 1)
	s := New(db)
	if _, err := db.Exec(`CREATE TRIGGER block_receipt BEFORE INSERT ON sync_applied_operations
		BEGIN SELECT RAISE(ABORT, 'receipt write failed'); END`); err != nil {
		t.Fatal(err)
	}
	item := replayFavorite(t, "00000000-0000-4000-8000-000000000106", "2026-11-22T10:00:00.000Z", false)
	if _, err := s.Push(t.Context(), user, []json.RawMessage{item}); err == nil {
		t.Fatal("push succeeded without a receipt")
	}
	if changes, cursor := pullAll(t, s, user, nil, 10); len(changes) != 0 || cursor != "d.0" {
		t.Fatalf("failed receipt left a committed write: %+v, %q", changes, cursor)
	}
	if _, err := db.Exec("DROP TRIGGER block_receipt"); err != nil {
		t.Fatal(err)
	}
	if replay := receiptPush(t, s, user, item); replay.Results[0].Status != "applied" || replay.Cursor != "d.1" {
		t.Fatalf("retry did not perform the rolled-back write: %+v", replay)
	}
}

func TestReceiptCapAndMixedBatchKeepsFirstResult(t *testing.T) {
	db := testDB(t)
	user := addUser(t, db, "receipt-cap@example.com")
	addTitle(t, db, 1)
	now := mustTime(t, "2026-11-22T10:00:00.000Z")
	s := New(db).WithClock(func() time.Time { return now })
	s.receiptLimit = 2
	a, b, c := "00000000-0000-4000-8000-000000000107", "00000000-0000-4000-8000-000000000108", "00000000-0000-4000-8000-000000000109"
	receiptPush(t, s, user, replayFavorite(t, a, "2026-11-22T09:00:00.000Z", false))
	first := receiptPush(t, s, user, replayFavorite(t, b, "2026-11-22T09:01:00.000Z", true))
	newItem := replayFavorite(t, c, "2026-11-22T09:02:00.000Z", true)
	batch, err := s.Push(t.Context(), user, []json.RawMessage{
		replayFavorite(t, b, "2026-11-22T09:05:00.000Z", false),
		newItem,
		replayFavorite(t, c, "2026-11-22T09:06:00.000Z", false),
	})
	if err != nil || len(batch.Results) != 3 || batch.Cursor != "d.3" ||
		batch.Results[0] != first.Results[0] || batch.Results[1].Status != "applied" || batch.Results[2] != batch.Results[1] {
		t.Fatalf("mixed batch did not keep the first results: %+v, %v", batch, err)
	}
	replay := receiptPush(t, s, user, replayFavorite(t, b, "2026-11-22T09:07:00.000Z", false))
	if replay.Results[0] != first.Results[0] || replay.Cursor != "d.3" {
		t.Fatalf("replay inside the cap was reapplied: %+v", replay)
	}
	var total, oldest int
	if err := db.QueryRow("SELECT count(*),coalesce(sum(op_id=?),0) FROM sync_applied_operations WHERE user_id=?", a, user).
		Scan(&total, &oldest); err != nil || total != 2 || oldest != 0 {
		t.Fatalf("cap did not discard just the oldest receipt: %d, %d, %v", total, oldest, err)
	}
	zero := "d.0"
	if changes, _ := pullAll(t, s, user, &zero, 10); len(changes) != 1 || !changes[0].Deleted {
		t.Fatalf("a replay with a different payload changed the record: %+v", changes)
	}
}
