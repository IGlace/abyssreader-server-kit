// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"net/http"

	"github.com/IGlace/abyssreader-server-kit/internal/community"
)

func (s *server) listComments(group community.CommentGroup, replies bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		q := queryReader{v: r.URL.Query()}
		p := communityPage(&q)
		if replies {
			p.Sort = q.option("sort", "latest", "latest")
			p.Asc = q.option("order", "asc", "asc", "desc") == "asc"
		} else {
			p.Sort = q.option("sort", "latest", "latest", "popular")
			p.Asc = q.option("order", "desc", "asc", "desc") == "asc"
		}
		if !q.valid(w) {
			return
		}
		data, err := s.community.ListComments(r.Context(), group, id, communityUser(r), replies, p)
		s.replyCommunity(w, r, 200, data, err)
	}
}

func (s *server) createComment(group community.CommentGroup, replies bool) func(http.ResponseWriter, *http.Request, caller) {
	return func(w http.ResponseWriter, r *http.Request, c caller) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		in, ok := commentInput(w, r)
		if !ok {
			return
		}
		data, err := s.community.CreateComment(r.Context(), group, id, c.account.ID, replies, in)
		s.replyCommunity(w, r, 201, data, err)
	}
}

func (s *server) updateComment(group community.CommentGroup) func(http.ResponseWriter, *http.Request, caller) {
	return func(w http.ResponseWriter, r *http.Request, c caller) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		in, ok := commentInput(w, r)
		if !ok {
			return
		}
		data, err := s.community.UpdateComment(r.Context(), group, id, c.account.ID, in)
		s.replyCommunity(w, r, 200, data, err)
	}
}

func (s *server) deleteComment(group community.CommentGroup) func(http.ResponseWriter, *http.Request, caller) {
	return func(w http.ResponseWriter, r *http.Request, c caller) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		err := s.community.DeleteComment(r.Context(), group, id, c.account.ID)
		s.replyCommunity(w, r, 204, nil, err)
	}
}

func (s *server) voteComment(group community.CommentGroup) func(http.ResponseWriter, *http.Request, caller) {
	return func(w http.ResponseWriter, r *http.Request, c caller) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		choice, ok := voteInput(w, r)
		if !ok {
			return
		}
		data, err := s.community.VoteComment(r.Context(), group, id, c.account.ID, choice)
		s.replyCommunity(w, r, 200, data, err)
	}
}

func (s *server) reportComment(group community.CommentGroup) func(http.ResponseWriter, *http.Request, caller) {
	return func(w http.ResponseWriter, r *http.Request, c caller) {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		reason, details, ok := reportInput(w, r, false)
		if !ok {
			return
		}
		err := s.community.ReportComment(r.Context(), group, id, c.account.ID, reason, details)
		s.replyCommunity(w, r, 204, nil, err)
	}
}
