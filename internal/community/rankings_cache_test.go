// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package community

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

func rankingFixture(t *testing.T) (*Service, *sql.DB) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "kit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, name := range []string{"Past", "Present", "Future", "Idle"} {
		_, err := db.Exec("INSERT INTO users (display_name,email,email_key,created_at,updated_at) VALUES (?,?,?,0,0)", name, name+"@example.org", name+"@example.org")
		if err != nil {
			t.Fatal(err)
		}
	}
	return New(db, false), db
}

func rankingVote(t *testing.T, db *sql.DB, user int64, at time.Time) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO sync_records
	 (user_id,kind,key_title,source_slug,chapter_url,url_hash,title_id,data,client_updated_at,deleted,seq)
	 VALUES (?,'rating',1,'','',X'',1,'{"opinion":"up"}',?,0,1)`, user, at.UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
}

func leaderboard(t *testing.T, s *Service, monthly bool, text string) Page[Ranking] {
	t.Helper()
	page, err := s.Rankings(context.Background(), PageQuery{Page: 1, Size: 20, Sort: "popular", Text: text}, StatsFeatures{}, monthly)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func TestRankingsOnlyShowAccountsWithPointsInThePeriod(t *testing.T) {
	s, db := rankingFixture(t)
	now := time.Date(2026, time.November, 25, 18, 0, 0, 0, time.UTC)
	s.rankingNow = func() time.Time { return now }
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	rankingVote(t, db, 1, month.Add(-time.Hour))
	rankingVote(t, db, 2, month)
	rankingVote(t, db, 3, now.Add(time.Hour))

	monthly := leaderboard(t, s, true, "")
	if monthly.Total != 1 || len(monthly.Items) != 1 || monthly.Items[0].User.ID != 2 || monthly.Items[0].Rank != 1 || monthly.Items[0].Points != 1 {
		t.Fatalf("wanted only current month's scorer: %+v", monthly)
	}
	overall := leaderboard(t, s, false, "")
	if overall.Total != 2 || len(overall.Items) != 2 || overall.Items[0].User.ID != 1 || overall.Items[1].User.ID != 2 {
		t.Fatalf("wanted only historical and current scorers: %+v", overall)
	}
	search := leaderboard(t, s, false, "present")
	if search.Total != 1 || len(search.Items) != 1 || search.Items[0].Rank != 2 {
		t.Fatalf("search should preserve rank: %+v", search)
	}
}

func TestRankingsReusePointsAndReadCurrentAccountVisibility(t *testing.T) {
	s, db := rankingFixture(t)
	now := time.Date(2026, time.November, 25, 18, 0, 0, 0, time.UTC)
	s.rankingNow = func() time.Time { return now }
	rankingVote(t, db, 1, now.Add(-time.Minute))
	first := leaderboard(t, s, false, "")
	if first.Total != 1 || len(first.Items) != 1 || first.Items[0].User.DisplayName != "Past" {
		t.Fatalf("wanted one scorer: %+v", first)
	}
	rankingVote(t, db, 2, now.Add(-time.Minute))
	if _, err := db.Exec("UPDATE users SET display_name='Renamed' WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	second := leaderboard(t, s, false, "")
	if second.Total != 1 || len(second.Items) != 1 || second.Items[0].User.DisplayName != "Renamed" {
		t.Fatalf("wanted cached scores and live account name: %+v", second)
	}
	if _, err := db.Exec("INSERT INTO account_suspensions (user_id,reason) VALUES (1,'reason')"); err != nil {
		t.Fatal(err)
	}
	hidden := leaderboard(t, s, false, "")
	if hidden.Total != 0 || len(hidden.Items) != 0 {
		t.Fatalf("suspended account remained visible: %+v", hidden)
	}
	if _, err := db.Exec("DELETE FROM account_suspensions WHERE user_id=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM users WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	deleted := leaderboard(t, s, false, "")
	if deleted.Total != 0 || len(deleted.Items) != 0 {
		t.Fatalf("deleted account remained visible: %+v", deleted)
	}

	now = now.Add(time.Minute)
	refreshed := leaderboard(t, s, false, "")
	if refreshed.Total != 1 || len(refreshed.Items) != 1 || refreshed.Items[0].User.DisplayName != "Present" {
		t.Fatalf("new score was not picked up on refresh: %+v", refreshed)
	}
}
