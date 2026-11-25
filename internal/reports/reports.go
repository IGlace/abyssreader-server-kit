// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package reports stores the operator's moderation queue.
package reports

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/catalog"
)

const (
	maxOpenPerAccount   = 100
	maxOpenPerTitle     = 250
	maxStoredPerAccount = 500
	MaxList             = 100
)

var (
	ErrTitle     = errors.New("title is missing or hidden")
	ErrDuplicate = errors.New("an open report already exists")
	ErrFull      = errors.New("too many open reports")
)

// Create checks visibility, the unique open report and the quotas in one SQLite
// immediate transaction. Concurrent submissions and admin actions serialize here.
func Create(ctx context.Context, db *sql.DB, user, title int64, hosted bool, reason string, details *string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var visible bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM titles t WHERE t.id=? AND
 `+catalog.VisibleTitle+`)`, title, hosted).Scan(&visible)
	if err != nil {
		return err
	}
	if !visible {
		return ErrTitle
	}
	var duplicate bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM title_reports WHERE user_id=? AND title_id=? AND closed=0)", user, title).Scan(&duplicate); err != nil {
		return err
	}
	if duplicate {
		return ErrDuplicate
	}
	var accountOpen, titleOpen, stored int
	err = tx.QueryRowContext(ctx, `SELECT
 (SELECT count(*) FROM title_reports WHERE user_id=? AND closed=0),
 (SELECT count(*) FROM title_reports WHERE title_id=? AND closed=0),
 (SELECT count(*) FROM title_reports WHERE user_id=?)`, user, title, user).Scan(&accountOpen, &titleOpen, &stored)
	if err != nil {
		return err
	}
	if accountOpen >= maxOpenPerAccount || titleOpen >= maxOpenPerTitle {
		return ErrFull
	}
	if stored >= maxStoredPerAccount {
		// At most maxOpenPerAccount are open, so there is always closed history to retire.
		if _, err = tx.ExecContext(ctx, `DELETE FROM title_reports WHERE id=(SELECT id FROM title_reports WHERE user_id=? AND closed=1 ORDER BY id LIMIT 1)`, user); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO title_reports(user_id,title_id,reason,details,created_at) VALUES(?,?,?,?,?)", user, title, reason, details, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

type Report struct {
	ID, TitleID, UserID           int64
	Title, Email, Reason, Details string
	TargetType                    string
	TargetID                      int64
	Content                       bool
	CreatedAt                     int64
	Closed                        bool
}

type ListOptions struct {
	Page, Limit int
	Title       string // ID or manifest ref; ref: disambiguates a numeric ref.
}

var titleFilter = regexp.MustCompile(`^(ref:)?[a-z0-9][a-z0-9._-]{0,127}$`)

func (q ListOptions) Validate() error {
	if q.Limit < 1 || q.Limit > MaxList {
		return fmt.Errorf("limit must be 1 to %d", MaxList)
	}
	if q.Page < 1 || int64(q.Page) > math.MaxInt64/int64(q.Limit) {
		return errors.New("invalid page number")
	}
	if q.Title != "" && !titleFilter.MatchString(q.Title) {
		return errors.New("invalid title ref or ID")
	}
	return nil
}

type ReportPage struct {
	Items       []Report
	Open, Total int64
}

// The count and the page see the same SQLite read snapshot even if another
// account submits or the operator closes a report during the list command.
func List(ctx context.Context, db *sql.DB, q ListOptions) (ReportPage, error) {
	var page ReportPage
	if err := q.Validate(); err != nil {
		return page, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	where := ""
	var args []any
	if q.Title != "" {
		if id, err := strconv.ParseInt(q.Title, 10, 64); err == nil {
			where = " WHERE r.title_id=?"
			args = append(args, id)
		} else {
			where = " WHERE r.title_id=(SELECT id FROM titles WHERE ref=?)"
			args = append(args, strings.TrimPrefix(q.Title, "ref:"))
		}
	}
	if err := tx.QueryRowContext(ctx, reportQueue+`SELECT count(*),count(*) FILTER (WHERE r.closed=0) FROM queue r`+where, args...).Scan(&page.Total, &page.Open); err != nil {
		return page, err
	}
	offset := int64(q.Page-1) * int64(q.Limit)
	if offset >= page.Total {
		return page, nil
	}
	rows, err := tx.QueryContext(ctx, reportQueue+`SELECT r.id,coalesce(r.title_id,0),r.label,u.id,u.email,r.reason,coalesce(r.details,''),r.created_at,r.closed,r.target_type,r.target_id,r.content
 FROM queue r JOIN users u ON u.id=r.user_id`+where+
		" ORDER BY r.closed,r.created_at DESC,r.id DESC,r.content LIMIT ? OFFSET ?", append(args, q.Limit, offset)...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Report
		if err := rows.Scan(&r.ID, &r.TitleID, &r.Title, &r.UserID, &r.Email, &r.Reason, &r.Details, &r.CreatedAt, &r.Closed, &r.TargetType, &r.TargetID, &r.Content); err != nil {
			return page, err
		}
		page.Items = append(page.Items, r)
	}
	return page, rows.Err()
}

const reportQueue = `WITH queue AS (
	SELECT r.id,0 AS content,r.title_id,'title' AS target_type,t.id AS target_id,t.name AS label,r.user_id,r.reason,r.details,r.created_at,r.closed
	FROM title_reports r JOIN titles t ON t.id=r.title_id
	UNION ALL
	SELECT r.id,1,c.title_id,
	 CASE WHEN r.collection_id IS NOT NULL THEN 'collection'
	      WHEN c.collection_id IS NOT NULL THEN 'collection comment'
	      WHEN c.parent_id IS NOT NULL THEN 'reply' ELSE 'comment' END,
	 coalesce(r.collection_id,r.comment_id),coalesce(k.name,substr(c.text,1,160)),r.user_id,r.reason,r.details,r.created_at,r.closed
	FROM content_reports r LEFT JOIN comments c ON c.id=r.comment_id LEFT JOIN collections k ON k.id=r.collection_id
) `

type CloseTarget int

const (
	SingleReport CloseTarget = iota
	TitleReports
	AccountReports
	SingleContentReport
)

func Close(ctx context.Context, db *sql.DB, target CloseTarget, id int64) (int64, error) {
	var query, content string
	switch target {
	case SingleReport:
		query = "UPDATE title_reports SET closed=1 WHERE id=? AND closed=0"
	case SingleContentReport:
		query = "UPDATE content_reports SET closed=1 WHERE id=? AND closed=0"
	case TitleReports:
		query = "UPDATE title_reports SET closed=1 WHERE title_id=? AND closed=0"
		content = "UPDATE content_reports SET closed=1 WHERE comment_id IN (SELECT id FROM comments WHERE title_id=?) AND closed=0"
	case AccountReports:
		query = "UPDATE title_reports SET closed=1 WHERE user_id=? AND closed=0"
		content = "UPDATE content_reports SET closed=1 WHERE user_id=? AND closed=0"
	default:
		return 0, errors.New("unknown report close target")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	change := func(query string) (int64, error) {
		r, err := tx.ExecContext(ctx, query, id)
		if err != nil {
			return 0, err
		}
		return r.RowsAffected()
	}
	n, err := change(query)
	if err != nil {
		return 0, err
	}
	if content != "" {
		m, err := change(content)
		if err != nil {
			return 0, err
		}
		n += m
	}
	return n, tx.Commit()
}
