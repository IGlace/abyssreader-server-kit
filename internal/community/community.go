// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package community stores comments, public collections and their moderation records.
package community

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var (
	ErrNotFound  = errors.New("community item was not found")
	ErrForbidden = errors.New("community item belongs to another account")
	ErrNested    = errors.New("replies are one level deep")
	ErrTitles    = errors.New("a title is missing or hidden")
	ErrLimit     = errors.New("community storage limit reached")
	ErrDuplicate = errors.New("an open report already exists")
)

type Service struct {
	db     *sql.DB
	hosted bool

	rankingNow      func() time.Time
	overallRankings rankingSnapshot
	monthlyRankings rankingSnapshot
}

func New(db *sql.DB, hosted bool) *Service {
	return &Service{db: db, hosted: hosted, rankingNow: time.Now}
}

type User struct {
	ID          int64   `json:"id"`
	DisplayName string  `json:"displayName"`
	AvatarURL   *string `json:"avatarUrl"`
}

type MyVote struct {
	Vote *string `json:"vote"`
}

func myVote(userID int64, vote *string) *MyVote {
	if userID == 0 {
		return nil
	}
	return &MyVote{Vote: vote}
}

type PageQuery struct {
	Page, Size int
	Sort, Text string
	Asc        bool
}

func (p PageQuery) offset() int64 { return int64(p.Page-1) * int64(p.Size) }

func (p PageQuery) direction() string {
	if p.Asc {
		return "ASC"
	}
	return "DESC"
}

type Page[T any] struct {
	Items   []T   `json:"items"`
	Page    int   `json:"page"`
	Size    int   `json:"size"`
	Total   int64 `json:"totalItems"`
	HasMore bool  `json:"hasMore"`
}

func newPage[T any](q PageQuery) Page[T] {
	return Page[T]{Items: []T{}, Page: q.Page, Size: q.Size}
}

func (p *Page[T]) addTotal(q PageQuery, total int64) {
	p.Total = total
	p.HasMore = q.offset()+int64(q.Size) < total
}

func timestamp(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z")
}

func exists(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	var ok bool
	err := tx.QueryRowContext(ctx, "SELECT EXISTS("+query+")", args...).Scan(&ok)
	if err == nil && !ok {
		return ErrNotFound
	}
	return err
}

func owner(ctx context.Context, tx *sql.Tx, userID int64, query string, args ...any) error {
	var actual int64
	err := tx.QueryRowContext(ctx, query, args...).Scan(&actual)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err == nil && actual != userID {
		return ErrForbidden
	}
	return err
}

// vote runs after the target has been found. table and column are constant call-site names.
func vote(ctx context.Context, tx *sql.Tx, table, column string, id, userID int64, choice *string) error {
	if choice == nil {
		_, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE "+column+"=? AND user_id=?", id, userID)
		return err
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO "+table+" ("+column+",user_id,vote) VALUES(?,?,?) ON CONFLICT DO UPDATE SET vote=excluded.vote", id, userID, *choice)
	return err
}

func readTx(ctx context.Context, db *sql.DB) (*sql.Tx, error) {
	return db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
}
