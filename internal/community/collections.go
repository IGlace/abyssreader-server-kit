// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package community

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/catalog"
)

const (
	MaxCollectionTitles      = 200
	maxCollectionsPerAccount = 100
)

type Collection struct {
	ID           int64    `json:"id"`
	Slug         string   `json:"slug"`
	Name         string   `json:"name"`
	Description  *string  `json:"description"`
	Owner        User     `json:"owner"`
	TitleCount   int64    `json:"titleCount"`
	CommentCount int64    `json:"commentCount"`
	Upvotes      int64    `json:"upvotes"`
	Downvotes    int64    `json:"downvotes"`
	Views        int64    `json:"views"`
	CoverURLs    []string `json:"coverUrls"`
	CreatedAt    string   `json:"createdAt"`
	UpdatedAt    string   `json:"updatedAt"`
	Me           *MyVote  `json:"me"`
}

type CollectionInput struct {
	Name        string
	Description *string
	TitleIDs    []int64
}

const collectionColumns = `c.id,c.slug,c.name,c.description,u.id,u.display_name,u.avatar_url,
 (SELECT count(*) FROM collection_titles ct JOIN titles t ON t.id=ct.title_id WHERE ct.collection_id=c.id AND ` + catalog.VisibleTitle + `),
 (SELECT count(*) FROM comments cm WHERE cm.collection_id=c.id),
 (SELECT count(*) FROM collection_votes v WHERE v.collection_id=c.id AND v.vote='up') AS upvotes,
 (SELECT count(*) FROM collection_votes v WHERE v.collection_id=c.id AND v.vote='down'),
 c.views,c.created_at,c.updated_at,(SELECT vote FROM collection_votes v WHERE v.collection_id=c.id AND v.user_id=?)`
const collectionTables = " FROM collections c JOIN users u ON u.id=c.user_id"

func scanCollection(row interface{ Scan(...any) error }, user int64) (Collection, error) {
	var c Collection
	var created, updated int64
	var choice *string
	err := row.Scan(&c.ID, &c.Slug, &c.Name, &c.Description, &c.Owner.ID, &c.Owner.DisplayName, &c.Owner.AvatarURL,
		&c.TitleCount, &c.CommentCount, &c.Upvotes, &c.Downvotes, &c.Views, &created, &updated, &choice)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	c.CreatedAt, c.UpdatedAt, c.Me = timestamp(created), timestamp(updated), myVote(user, choice)
	return c, err
}

func (s *Service) covers(ctx context.Context, tx *sql.Tx, c *Collection, publicURL string) error {
	rows, err := tx.QueryContext(ctx, `SELECT t.cover_image FROM collection_titles ct JOIN titles t ON t.id=ct.title_id
	 WHERE ct.collection_id=? AND t.cover_image IS NOT NULL AND `+catalog.VisibleTitle+
		" ORDER BY ct.position LIMIT 10", c.ID, s.hosted)
	if err != nil {
		return err
	}
	defer rows.Close()
	c.CoverURLs = []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		c.CoverURLs = append(c.CoverURLs, publicURL+"/covers/"+name)
	}
	return rows.Err()
}

func (s *Service) getCollection(ctx context.Context, tx *sql.Tx, condition string, key any, user int64, publicURL string) (Collection, error) {
	c, err := scanCollection(tx.QueryRowContext(ctx, "SELECT "+collectionColumns+collectionTables+" WHERE "+condition, s.hosted, user, key), user)
	if err != nil {
		return c, err
	}
	err = s.covers(ctx, tx, &c, publicURL)
	return c, err
}

func (s *Service) ReadCollection(ctx context.Context, id int64, slug string, user int64, publicURL string, countView func(int64) bool) (Collection, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: countView == nil})
	if err != nil {
		return Collection{}, err
	}
	defer tx.Rollback()
	condition, key := "c.id=?", any(id)
	if slug != "" {
		condition, key = "c.slug=?", slug
	}
	c, err := s.getCollection(ctx, tx, condition, key, user, publicURL)
	if err != nil || countView == nil || !countView(c.ID) {
		return c, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE collections SET views=views+1 WHERE id=?", c.ID); err != nil {
		return c, err
	}
	c.Views++
	return c, tx.Commit()
}

func (s *Service) ListCollections(ctx context.Context, user int64, onlyMine bool, q PageQuery, publicURL string) (Page[Collection], error) {
	p := newPage[Collection](q)
	tx, err := readTx(ctx, s.db)
	if err != nil {
		return p, err
	}
	defer tx.Rollback()
	var filters []string
	var args []any
	if onlyMine {
		filters = append(filters, "c.user_id=?")
		args = append(args, user)
	}
	if q.Text != "" {
		filters = append(filters, "instr(c.name_key,?)>0")
		args = append(args, strings.ToLower(q.Text))
	}
	var where string
	if len(filters) > 0 {
		where = " WHERE " + strings.Join(filters, " AND ")
	}
	var total int64
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM collections c"+where, args...).Scan(&total); err != nil {
		return p, err
	}
	p.addTotal(q, total)
	order := "c.updated_at"
	switch q.Sort {
	case "popular":
		order = "upvotes"
	case "trending":
		order = "c.views"
	case "alphabetical":
		order = "c.name_key"
	}
	queryArgs := append([]any{s.hosted, user}, args...)
	queryArgs = append(queryArgs, q.Size, q.offset())
	rows, err := tx.QueryContext(ctx, "SELECT "+collectionColumns+collectionTables+where+" ORDER BY "+order+" "+q.direction()+",c.id ASC LIMIT ? OFFSET ?", queryArgs...)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanCollection(rows, user)
		if err != nil {
			return p, err
		}
		p.Items = append(p.Items, c)
	}
	if err = rows.Err(); err != nil {
		return p, err
	}
	for i := range p.Items {
		if err = s.covers(ctx, tx, &p.Items[i], publicURL); err != nil {
			return p, err
		}
	}
	return p, nil
}

func (s *Service) SaveCollection(ctx context.Context, id, user int64, in CollectionInput, publicURL string) (Collection, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Collection{}, err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	if id == 0 {
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM collections WHERE user_id=?", user).Scan(&count); err != nil {
			return Collection{}, err
		}
		if count >= maxCollectionsPerAccount {
			return Collection{}, ErrLimit
		}
		// The permanent slug has a short name prefix and a random suffix so
		// equal names can coexist. Updating the name preserves shared links.
		prefix := []rune(catalog.Slugify(in.Name))
		prefix = prefix[:min(len(prefix), 100)]
		err = tx.QueryRowContext(ctx, `INSERT INTO collections (user_id,slug,name,name_key,description,created_at,updated_at)
		 VALUES (?,?||lower(hex(randomblob(12))),?,?,?,?,?) RETURNING id`, user, strings.Trim(string(prefix), "-")+"-", in.Name, strings.ToLower(in.Name), in.Description, now, now).Scan(&id)
		if err != nil {
			return Collection{}, err
		}
	} else {
		if err = owner(ctx, tx, user, "SELECT user_id FROM collections WHERE id=?", id); err != nil {
			return Collection{}, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE collections SET name=?,name_key=?,description=?,updated_at=? WHERE id=?", in.Name, strings.ToLower(in.Name), in.Description, now, id); err != nil {
			return Collection{}, err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM collection_titles WHERE collection_id=?", id); err != nil {
			return Collection{}, err
		}
	}
	for position, title := range in.TitleIDs {
		res, err := tx.ExecContext(ctx, `INSERT INTO collection_titles (collection_id,title_id,position)
		 SELECT ?,t.id,? FROM titles t WHERE t.id=? AND `+catalog.VisibleTitle, id, position, title, s.hosted)
		if err != nil {
			return Collection{}, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return Collection{}, err
		}
		if n == 0 {
			return Collection{}, ErrTitles
		}
	}
	c, err := s.getCollection(ctx, tx, "c.id=?", id, user, publicURL)
	if err != nil {
		return c, err
	}
	return c, tx.Commit()
}

func (s *Service) VoteCollection(ctx context.Context, id, user int64, choice *string, publicURL string) (Collection, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Collection{}, err
	}
	defer tx.Rollback()
	if err = exists(ctx, tx, "SELECT 1 FROM collections WHERE id=?", id); err != nil {
		return Collection{}, err
	}
	if err = vote(ctx, tx, "collection_votes", "collection_id", id, user, choice); err != nil {
		return Collection{}, err
	}
	c, err := s.getCollection(ctx, tx, "c.id=?", id, user, publicURL)
	if err != nil {
		return c, err
	}
	return c, tx.Commit()
}

func (s *Service) DeleteCollection(ctx context.Context, id, user int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = owner(ctx, tx, user, "SELECT user_id FROM collections WHERE id=?", id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM collections WHERE id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}
