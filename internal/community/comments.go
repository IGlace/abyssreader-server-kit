// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package community

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/catalog"
)

type CommentGroup int

const (
	TitleComments CommentGroup = iota
	CollectionComments
	maxCommentsPerAccount = 10000
)

type Comment struct {
	ID           int64   `json:"id"`
	TitleID      *int64  `json:"titleId,omitempty"`
	CollectionID *int64  `json:"collectionId,omitempty"`
	ParentID     *int64  `json:"parentId"`
	Text         string  `json:"text"`
	ImageURL     *string `json:"imageUrl"`
	Spoiler      bool    `json:"spoiler"`
	Author       User    `json:"author"`
	Upvotes      int64   `json:"upvotes"`
	Downvotes    int64   `json:"downvotes"`
	ReplyCount   int64   `json:"replyCount"`
	CreatedAt    string  `json:"createdAt"`
	UpdatedAt    string  `json:"updatedAt"`
	Me           *MyVote `json:"me"`
}

type CommentInput struct {
	Text     string
	ImageURL *string
	Spoiler  bool
}

// commentScope returns a predicate in the comment row's address space. An
// inactive hosted-only title must be hidden even when accessed through a comment ID.
func (s *Service) commentScope(group CommentGroup) (string, []any) {
	if group == CollectionComments {
		return "c.collection_id IS NOT NULL", nil
	}
	return "EXISTS (SELECT 1 FROM titles t WHERE t.id=c.title_id AND " + catalog.VisibleTitle + ")", []any{s.hosted}
}

func (s *Service) commentTarget(ctx context.Context, tx *sql.Tx, group CommentGroup, anchor int64, replies bool) (titleID, collectionID *int64, err error) {
	if replies {
		scope, args := s.commentScope(group)
		var parentID *int64
		err = tx.QueryRowContext(ctx, "SELECT c.title_id,c.collection_id,c.parent_id FROM comments c WHERE c.id=? AND "+scope, append([]any{anchor}, args...)...).Scan(&titleID, &collectionID, &parentID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			err = ErrNotFound
		case err == nil && parentID != nil:
			err = ErrNested
		}
		return
	}
	if group == CollectionComments {
		return nil, &anchor, exists(ctx, tx, "SELECT 1 FROM collections WHERE id=?", anchor)
	}
	return &anchor, nil, exists(ctx, tx, "SELECT 1 FROM titles t WHERE t.id=? AND "+catalog.VisibleTitle, anchor, s.hosted)
}

const commentColumns = `c.id,c.title_id,c.collection_id,c.parent_id,c.text,c.image_url,c.spoiler,
 u.id,u.display_name,u.avatar_url,
 (SELECT count(*) FROM comment_votes v WHERE v.comment_id=c.id AND v.vote='up') AS upvotes,
 (SELECT count(*) FROM comment_votes v WHERE v.comment_id=c.id AND v.vote='down') AS downvotes,
 (SELECT count(*) FROM comments child WHERE child.parent_id=c.id),
 c.created_at,c.updated_at,(SELECT vote FROM comment_votes v WHERE v.comment_id=c.id AND v.user_id=?)`
const commentTables = " FROM comments c JOIN users u ON u.id=c.user_id"

func scanComment(row interface{ Scan(...any) error }, user int64) (Comment, error) {
	var c Comment
	var created, updated int64
	var choice *string
	err := row.Scan(&c.ID, &c.TitleID, &c.CollectionID, &c.ParentID, &c.Text, &c.ImageURL, &c.Spoiler,
		&c.Author.ID, &c.Author.DisplayName, &c.Author.AvatarURL, &c.Upvotes, &c.Downvotes, &c.ReplyCount,
		&created, &updated, &choice)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	c.CreatedAt, c.UpdatedAt, c.Me = timestamp(created), timestamp(updated), myVote(user, choice)
	return c, err
}

func getComment(ctx context.Context, tx *sql.Tx, id, user int64) (Comment, error) {
	return scanComment(tx.QueryRowContext(ctx, "SELECT "+commentColumns+commentTables+" WHERE c.id=?", user, id), user)
}

func (s *Service) ListComments(ctx context.Context, group CommentGroup, anchor, user int64, replies bool, q PageQuery) (Page[Comment], error) {
	p := newPage[Comment](q)
	tx, err := readTx(ctx, s.db)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	_, _, err = s.commentTarget(ctx, tx, group, anchor, replies)
	if errors.Is(err, ErrNested) {
		return p, nil // replies never have children
	}
	if err != nil {
		return p, err
	}
	where := " WHERE c.title_id=? AND c.parent_id IS NULL"
	switch {
	case replies:
		where = " WHERE c.parent_id=?"
	case group == CollectionComments:
		where = " WHERE c.collection_id=? AND c.parent_id IS NULL"
	}
	var total int64
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM comments c"+where, anchor).Scan(&total); err != nil {
		return p, err
	}
	p.addTotal(q, total)
	order := "c.created_at"
	if q.Sort == "popular" {
		order = `((SELECT count(*) FROM comment_votes v WHERE v.comment_id=c.id AND v.vote='up')-
				(SELECT count(*) FROM comment_votes v WHERE v.comment_id=c.id AND v.vote='down'))`
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+commentColumns+commentTables+where+" ORDER BY "+order+" "+q.direction()+",c.id ASC LIMIT ? OFFSET ?", user, anchor, q.Size, q.offset())
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanComment(rows, user)
		if err != nil {
			return p, err
		}
		p.Items = append(p.Items, c)
	}
	return p, rows.Err()
}

func (s *Service) CreateComment(ctx context.Context, group CommentGroup, anchor, user int64, replies bool, in CommentInput) (Comment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Comment{}, err
	}
	defer tx.Rollback()
	title, collection, err := s.commentTarget(ctx, tx, group, anchor, replies)
	if err != nil {
		return Comment{}, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM comments WHERE user_id=?", user).Scan(&count); err != nil {
		return Comment{}, err
	}
	if count >= maxCommentsPerAccount {
		return Comment{}, ErrLimit
	}
	var parent *int64
	if replies {
		parent = &anchor
	}
	now := time.Now().UnixMilli()
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO comments (user_id,title_id,collection_id,parent_id,text,image_url,spoiler,created_at,updated_at)
	 VALUES (?,?,?,?,?,?,?,?,?) RETURNING id`, user, title, collection, parent, in.Text, in.ImageURL, in.Spoiler, now, now).Scan(&id)
	if err != nil {
		return Comment{}, err
	}
	c, err := getComment(ctx, tx, id, user)
	if err != nil {
		return c, err
	}
	return c, tx.Commit()
}

func (s *Service) UpdateComment(ctx context.Context, group CommentGroup, id, user int64, in CommentInput) (Comment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Comment{}, err
	}
	defer tx.Rollback()
	scope, args := s.commentScope(group)
	if err := owner(ctx, tx, user, "SELECT c.user_id FROM comments c WHERE c.id=? AND "+scope, append([]any{id}, args...)...); err != nil {
		return Comment{}, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE comments SET text=?,image_url=?,spoiler=?,updated_at=? WHERE id=?", in.Text, in.ImageURL, in.Spoiler, time.Now().UnixMilli(), id); err != nil {
		return Comment{}, err
	}
	c, err := getComment(ctx, tx, id, user)
	if err != nil {
		return c, err
	}
	return c, tx.Commit()
}

func (s *Service) DeleteComment(ctx context.Context, group CommentGroup, id, user int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	scope, args := s.commentScope(group)
	if err := owner(ctx, tx, user, "SELECT c.user_id FROM comments c WHERE c.id=? AND "+scope, append([]any{id}, args...)...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM comments WHERE id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Service) VoteComment(ctx context.Context, group CommentGroup, id, user int64, choice *string) (Comment, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Comment{}, err
	}
	defer tx.Rollback()
	scope, args := s.commentScope(group)
	if err := exists(ctx, tx, "SELECT 1 FROM comments c WHERE c.id=? AND "+scope, append([]any{id}, args...)...); err != nil {
		return Comment{}, err
	}
	if err := vote(ctx, tx, "comment_votes", "comment_id", id, user, choice); err != nil {
		return Comment{}, err
	}
	c, err := getComment(ctx, tx, id, user)
	if err != nil {
		return c, err
	}
	return c, tx.Commit()
}
