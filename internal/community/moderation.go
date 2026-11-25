// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package community

import (
	"context"
	"database/sql"
	"time"
)

const (
	maxContentOpenPerAccount   = 100
	maxContentOpenPerTarget    = 250
	maxContentStoredPerAccount = 500
)

func (s *Service) ReportComment(ctx context.Context, group CommentGroup, id, user int64, reason string, details *string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	scope, args := s.commentScope(group)
	if err = exists(ctx, tx, "SELECT 1 FROM comments c WHERE c.id=? AND "+scope, append([]any{id}, args...)...); err != nil {
		return err
	}
	if err = submitContentReport(ctx, tx, "comment_id", id, user, reason, details); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) ReportCollection(ctx context.Context, id, user int64, reason string, details *string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = exists(ctx, tx, "SELECT 1 FROM collections WHERE id=?", id); err != nil {
		return err
	}
	if err = submitContentReport(ctx, tx, "collection_id", id, user, reason, details); err != nil {
		return err
	}
	return tx.Commit()
}

// targetColumn is one of two constant column names. The check and write share
// the same SQLite write transaction as target lookup.
func submitContentReport(ctx context.Context, tx *sql.Tx, targetColumn string, id, user int64, reason string, details *string) error {
	var openByUser, openOnItem, stored int
	var duplicate bool
	err := tx.QueryRowContext(ctx, `SELECT
	 (SELECT count(*) FROM content_reports WHERE user_id=? AND closed=0),
	 (SELECT count(*) FROM content_reports WHERE `+targetColumn+`=? AND closed=0),
	 (SELECT count(*) FROM content_reports WHERE user_id=?),
	 EXISTS(SELECT 1 FROM content_reports WHERE user_id=? AND `+targetColumn+`=? AND closed=0)`,
		user, id, user, user, id).Scan(&openByUser, &openOnItem, &stored, &duplicate)
	if err != nil {
		return err
	}
	switch {
	case duplicate:
		return ErrDuplicate
	case openByUser >= maxContentOpenPerAccount || openOnItem >= maxContentOpenPerTarget:
		return ErrLimit
	}
	if stored >= maxContentStoredPerAccount {
		if _, err = tx.ExecContext(ctx, "DELETE FROM content_reports WHERE id=(SELECT id FROM content_reports WHERE user_id=? AND closed=1 ORDER BY id LIMIT 1)", user); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO content_reports ("+targetColumn+",user_id,reason,details,created_at) VALUES (?,?,?,?,?)", id, user, reason, details, time.Now().UnixMilli())
	return err
}

func Remove(ctx context.Context, db *sql.DB, kind string, id int64) error {
	var query string
	var args []any
	switch kind {
	case "comment":
		query, args = "DELETE FROM comments WHERE id=? OR parent_id=?", []any{id, id}
	case "collection":
		query, args = "DELETE FROM collections WHERE id=?", []any{id}
	default:
		return ErrNotFound
	}
	res, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}
