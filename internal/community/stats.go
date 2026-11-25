// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package community

import (
	"context"
	"database/sql"
	"time"
)

type StatsFeatures struct{ Comments, Collections, TitleReports bool }

type Genres struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

type Stats struct {
	TitlesRead                         int64    `json:"titlesRead"`
	LastReadAt                         *string  `json:"lastReadAt"`
	CurrentStreakDays                  int64    `json:"currentStreakDays"`
	LongestStreakDays                  int64    `json:"longestStreakDays"`
	FavoriteCount                      int64    `json:"favoriteCount"`
	PlanningCount                      int64    `json:"planningCount"`
	BlacklistCount                     int64    `json:"blacklistCount"`
	TitleVotesGiven                    int64    `json:"titleVotesGiven"`
	TitleUpvotesGiven                  int64    `json:"titleUpvotesGiven"`
	TitleDownvotesGiven                int64    `json:"titleDownvotesGiven"`
	CommentsPosted                     int64    `json:"commentsPosted"`
	CommentUpvotesGiven                int64    `json:"commentUpvotesGiven"`
	CommentDownvotesGiven              int64    `json:"commentDownvotesGiven"`
	CommentUpvotesReceived             int64    `json:"commentUpvotesReceived"`
	CommentDownvotesReceived           int64    `json:"commentDownvotesReceived"`
	CollectionsCreated                 int64    `json:"collectionsCreated"`
	CollectionTitles                   int64    `json:"collectionTitles"`
	CollectionCommentsPosted           int64    `json:"collectionCommentsPosted"`
	CollectionCommentUpvotesGiven      int64    `json:"collectionCommentUpvotesGiven"`
	CollectionCommentDownvotesGiven    int64    `json:"collectionCommentDownvotesGiven"`
	CollectionCommentUpvotesReceived   int64    `json:"collectionCommentUpvotesReceived"`
	CollectionCommentDownvotesReceived int64    `json:"collectionCommentDownvotesReceived"`
	CollectionVotesGiven               int64    `json:"collectionVotesGiven"`
	CollectionVotesReceived            int64    `json:"collectionVotesReceived"`
	ReportsSubmitted                   int64    `json:"reportsSubmitted"`
	GenresInteracted                   int64    `json:"genresInteracted"`
	LastAggregatedAt                   string   `json:"lastAggregatedAt"`
	TopGenres                          []Genres `json:"topGenres"`
}

const readActivity = `(kind='position' OR (kind='chapterMark' AND json_extract(data,'$.read')=1))`

func (s *Service) Stats(ctx context.Context, user int64, features StatsFeatures, now time.Time) (Stats, error) {
	out := Stats{TopGenres: []Genres{}, LastAggregatedAt: timestamp(now.UnixMilli())}
	tx, err := readTx(ctx, s.db)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	var last *int64
	err = tx.QueryRowContext(ctx, `SELECT
	 count(*) FILTER (WHERE kind='favorite'),
	 count(*) FILTER (WHERE kind='planning'),
	 count(*) FILTER (WHERE kind='blacklist'),
	 count(*) FILTER (WHERE kind='rating'),
	 count(*) FILTER (WHERE kind='rating' AND json_extract(data,'$.opinion')='up'),
	 count(*) FILTER (WHERE kind='rating' AND json_extract(data,'$.opinion')='down'),
	 count(DISTINCT title_id) FILTER (WHERE `+readActivity+`),
	 max(client_updated_at) FILTER (WHERE `+readActivity+`)
	 FROM sync_records WHERE user_id=? AND deleted=0`, user).Scan(
		&out.FavoriteCount, &out.PlanningCount, &out.BlacklistCount, &out.TitleVotesGiven, &out.TitleUpvotesGiven, &out.TitleDownvotesGiven, &out.TitlesRead, &last)
	if err != nil {
		return out, err
	}
	if last != nil {
		v := timestamp(*last)
		out.LastReadAt = &v
	}
	if err = readStreaks(ctx, tx, user, now, &out); err != nil {
		return out, err
	}
	if err = readGenres(ctx, tx, user, &out); err != nil {
		return out, err
	}
	if features.Comments {
		err = commentStats(ctx, tx, user, "title_id", &out.CommentsPosted, &out.CommentUpvotesGiven, &out.CommentDownvotesGiven, &out.CommentUpvotesReceived, &out.CommentDownvotesReceived)
		if err != nil {
			return out, err
		}
	}
	if features.Collections {
		err = commentStats(ctx, tx, user, "collection_id", &out.CollectionCommentsPosted, &out.CollectionCommentUpvotesGiven, &out.CollectionCommentDownvotesGiven, &out.CollectionCommentUpvotesReceived, &out.CollectionCommentDownvotesReceived)
		if err != nil {
			return out, err
		}
		err = tx.QueryRowContext(ctx, `SELECT
		 (SELECT count(*) FROM collections WHERE user_id=?1),
		 (SELECT count(*) FROM collection_titles ct JOIN collections c ON c.id=ct.collection_id WHERE c.user_id=?1),
		 (SELECT count(*) FROM collection_votes WHERE user_id=?1),
		 (SELECT count(*) FROM collection_votes v JOIN collections c ON c.id=v.collection_id WHERE c.user_id=?1)`, user).
			Scan(&out.CollectionsCreated, &out.CollectionTitles, &out.CollectionVotesGiven, &out.CollectionVotesReceived)
		if err != nil {
			return out, err
		}
	}
	err = tx.QueryRowContext(ctx, `SELECT
	 (SELECT count(*) FROM title_reports WHERE user_id=?1 AND ?2)
	 + (SELECT count(*) FROM content_reports r LEFT JOIN comments c ON c.id=r.comment_id
	  WHERE r.user_id=?1 AND ((?3 AND c.title_id IS NOT NULL) OR (?4 AND (c.collection_id IS NOT NULL OR r.collection_id IS NOT NULL))))`,
		user, features.TitleReports, features.Comments, features.Collections).Scan(&out.ReportsSubmitted)
	return out, err
}

func commentStats(ctx context.Context, tx *sql.Tx, user int64, group string, counts ...*int64) error {
	where := "c." + group + " IS NOT NULL"
	row := tx.QueryRowContext(ctx, `SELECT
	 (SELECT count(*) FROM comments c WHERE c.user_id=?1 AND `+where+`),
	 (SELECT count(*) FROM comment_votes v JOIN comments c ON c.id=v.comment_id WHERE v.user_id=?1 AND v.vote='up' AND `+where+`),
	 (SELECT count(*) FROM comment_votes v JOIN comments c ON c.id=v.comment_id WHERE v.user_id=?1 AND v.vote='down' AND `+where+`),
	 (SELECT count(*) FROM comments c JOIN comment_votes v ON v.comment_id=c.id WHERE c.user_id=?1 AND v.vote='up' AND `+where+`),
	 (SELECT count(*) FROM comments c JOIN comment_votes v ON v.comment_id=c.id WHERE c.user_id=?1 AND v.vote='down' AND `+where+`)`, user)
	dest := make([]any, len(counts))
	for i, count := range counts {
		dest[i] = count
	}
	return row.Scan(dest...)
}

func readStreaks(ctx context.Context, tx *sql.Tx, user int64, now time.Time, out *Stats) error {
	const dayMS = int64(24 * time.Hour / time.Millisecond)
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT client_updated_at / ? AS day FROM sync_records
	 WHERE user_id=? AND deleted=0 AND client_updated_at >= 0 AND `+readActivity+" ORDER BY day", dayMS, user)
	if err != nil {
		return err
	}
	defer rows.Close()
	var latest, run int64
	today := now.UnixMilli() / dayMS
	for rows.Next() {
		var day int64
		if err := rows.Scan(&day); err != nil {
			return err
		}
		if day > today {
			break
		}
		if run > 0 && day == latest+1 {
			run++
		} else {
			run = 1
		}
		latest = day
		out.LongestStreakDays = max(out.LongestStreakDays, run)
	}
	if latest == today {
		out.CurrentStreakDays = run
	}
	return rows.Err()
}

func readGenres(ctx context.Context, tx *sql.Tx, user int64, out *Stats) error {
	rows, err := tx.QueryContext(ctx, `SELECT g.id,g.name,count(DISTINCT tg.title_id) AS count, count(*) OVER ()
	 FROM sync_records r JOIN title_genres tg ON tg.title_id=r.title_id JOIN genres g ON g.id=tg.genre_id
	 WHERE r.user_id=? AND r.deleted=0 AND `+readActivity+" GROUP BY g.id ORDER BY count DESC,g.id LIMIT 10", user)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var g Genres
		if err := rows.Scan(&g.ID, &g.Name, &g.Count, &out.GenresInteracted); err != nil {
			return err
		}
		out.TopGenres = append(out.TopGenres, g)
	}
	return rows.Err()
}
