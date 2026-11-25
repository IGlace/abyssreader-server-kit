// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package community

import (
	"context"
	"strings"
	"sync"
	"time"
)

type Ranking struct {
	Rank   int64 `json:"rank"`
	User   User  `json:"user"`
	Points int64 `json:"points"`
}

const (
	rankingRefresh      = time.Minute
	rankingQueryTimeout = 10 * time.Second
)

// Cache scores and account IDs per period. Current account details and exclusions
// are applied by SQLite on every read, so no deleted, disabled or suspended
// account is kept visible through the cache.
type rankingSnapshot struct {
	mu       sync.Mutex
	since    time.Time
	retryAt  time.Time
	features StatsFeatures
	points   string
	err      error
}

func (s *Service) rankingPoints(ctx context.Context, features StatsFeatures, monthly bool) (string, error) {
	cache := &s.overallRankings
	if monthly {
		cache = &s.monthlyRankings
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	now := s.rankingNow().UTC()
	var since time.Time
	if monthly {
		since = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	if cache.since.Equal(since) && cache.features == features && now.Before(cache.retryAt) {
		return cache.points, cache.err
	}

	// A caller who disconnects must not poison the shared cache with its
	// canceled context. The refresh has its own deadline and backs off on error.
	refresh, cancel := context.WithTimeout(context.WithoutCancel(ctx), rankingQueryTimeout)
	defer cancel()
	var points string
	err := s.db.QueryRowContext(refresh, `WITH activity AS (
	 SELECT user_id,count(DISTINCT title_id) FILTER (WHERE `+readActivity+`)+count(*) FILTER (WHERE kind='rating') AS points
	  FROM sync_records WHERE deleted=0 AND client_updated_at BETWEEN ?1 AND ?2
	   AND kind IN ('position','chapterMark','rating') GROUP BY user_id
	 UNION ALL SELECT user_id,count(*) FROM comments
	  WHERE created_at BETWEEN ?1 AND ?2 AND ((?3 AND title_id IS NOT NULL) OR (?4 AND collection_id IS NOT NULL)) GROUP BY user_id
	 UNION ALL SELECT user_id,count(*) FROM collections WHERE ?4 AND created_at BETWEEN ?1 AND ?2 GROUP BY user_id
	 ), totals AS (SELECT user_id,sum(points) AS points FROM activity GROUP BY user_id HAVING sum(points)>0)
	 SELECT json_group_array(json_array(user_id,points)) FROM totals`,
		since.UnixMilli(), now.UnixMilli(), features.Comments, features.Collections).Scan(&points)
	cache.since, cache.features, cache.points, cache.err = since, features, points, err
	cache.retryAt = now.Add(rankingRefresh)
	if err != nil {
		cache.retryAt = now.Add(5 * time.Second)
	}
	return points, err
}

const scoredAccounts = ` FROM json_each(?1) p JOIN users u ON u.id=json_extract(p.value,'$[0]')
 WHERE u.disabled_at IS NULL AND NOT EXISTS(SELECT 1 FROM account_suspensions a WHERE a.user_id=u.id)`

// Rankings awards points in the requested period. Rank is computed before the
// display-name filter. Equal scores are ordered by account ID.
func (s *Service) Rankings(ctx context.Context, q PageQuery, features StatsFeatures, monthly bool) (Page[Ranking], error) {
	p := newPage[Ranking](q)
	scores, err := s.rankingPoints(ctx, features, monthly)
	if err != nil {
		return p, err
	}
	tx, err := readTx(ctx, s.db)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	needle := strings.ToLower(q.Text)
	if err = tx.QueryRowContext(ctx, "SELECT count(*)"+scoredAccounts+
		" AND (?2='' OR instr(lower(u.display_name),?2)>0)", scores, needle).Scan(&p.Total); err != nil {
		return p, err
	}
	p.addTotal(q, p.Total)
	if q.offset() >= p.Total {
		return p, nil
	}
	order := "points"
	if q.Sort == "alphabetical" {
		order = "lower(display_name)"
	}
	rows, err := tx.QueryContext(ctx, `WITH scored AS (SELECT u.id,u.display_name,u.avatar_url,json_extract(p.value,'$[1]') AS points`+scoredAccounts+`),
	 ranks AS (SELECT row_number() OVER (ORDER BY points DESC,id) AS rank,id,display_name,avatar_url,points FROM scored)
	 SELECT rank,id,display_name,avatar_url,points FROM ranks WHERE (?2='' OR instr(lower(display_name),?2)>0)
	 ORDER BY `+order+" "+q.direction()+",rank ASC LIMIT ?3 OFFSET ?4",
		scores, needle, q.Size, q.offset())
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Ranking
		if err := rows.Scan(&r.Rank, &r.User.ID, &r.User.DisplayName, &r.User.AvatarURL, &r.Points); err != nil {
			return p, err
		}
		p.Items = append(p.Items, r)
	}
	return p, rows.Err()
}
