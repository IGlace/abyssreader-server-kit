// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/IGlace/abyssreader-server-kit/internal/catalog"
	"github.com/IGlace/abyssreader-server-kit/internal/community"
	"github.com/IGlace/abyssreader-server-kit/internal/config"
	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

func (s *server) listCollections(mine bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := queryReader{v: r.URL.Query()}
		p := communityPage(&q)
		p.Text = q.searchText()
		p.Sort = q.option("sort", "latest", "latest", "popular", "trending", "alphabetical")
		def := "desc"
		if p.Sort == "alphabetical" {
			def = "asc"
		}
		p.Asc = q.option("order", def, "asc", "desc") == "asc"
		if !q.valid(w) {
			return
		}
		data, err := s.community.ListCollections(r.Context(), communityUser(r), mine, p, s.Config.PublicURL)
		s.replyCommunity(w, r, 200, data, err)
	}
}

func (s *server) readCollection(bySlug bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var id int64
		var slug string
		if bySlug {
			slug = r.PathValue("slug")
			if n := utf8.RuneCountInString(slug); n < 1 || n > 255 {
				writeValidation(w, []fieldError{{Field: "slug", Code: "invalid"}})
				return
			}
		} else {
			var ok bool
			id, ok = pathID(w, r)
			if !ok {
				return
			}
		}
		var countView func(int64) bool
		if r.Method == http.MethodGet {
			countView = func(id int64) bool {
				_, fresh := s.views.allow(s.clientKey(r) + " collection " + strconv.FormatInt(id, 10))
				return fresh
			}
		}
		data, err := s.community.ReadCollection(r.Context(), id, slug, communityUser(r), s.Config.PublicURL, countView)
		s.replyCommunity(w, r, 200, data, err)
	}
}

func (s *server) saveCollection(update bool) func(http.ResponseWriter, *http.Request, caller) {
	return func(w http.ResponseWriter, r *http.Request, c caller) {
		var id int64
		status := http.StatusCreated
		if update {
			var ok bool
			id, ok = pathID(w, r)
			if !ok {
				return
			}
			status = http.StatusOK
		}
		o, ok := readObject(w, r)
		if !ok {
			return
		}
		name := o.str("name")
		contentText(o, "name", name, 255)
		desc := optionalString(o, "description")
		if desc != nil {
			contentText(o, "description", *desc, 10000)
		}
		ids := collectionIDs(o)
		if o.failed(w) {
			return
		}
		data, err := s.community.SaveCollection(r.Context(), id, c.account.ID, community.CollectionInput{
			Name: name, Description: desc, TitleIDs: ids,
		}, s.Config.PublicURL)
		if errors.Is(err, community.ErrTitles) {
			writeValidation(w, []fieldError{{Field: "titleIds", Code: "unknown_value"}})
			return
		}
		s.replyCommunity(w, r, status, data, err)
	}
}

func collectionIDs(o *jsonObject) []int64 {
	o.order = append(o.order, "titleIds")
	var ids []int64
	if raw, ok := o.fields["titleIds"]; !ok || string(raw) == "null" {
		o.check("titleIds", "required")
		return nil
	} else if json.Unmarshal(raw, &ids) != nil {
		o.check("titleIds", "invalid")
		return nil
	}
	if len(ids) > community.MaxCollectionTitles {
		o.check("titleIds", "too_many")
		return nil
	}
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id < 1 || id > maxID || seen[id] {
			o.check("titleIds", "invalid")
			break
		}
		seen[id] = true
	}
	return ids
}

func (s *server) deleteCollection(w http.ResponseWriter, r *http.Request, c caller) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := s.community.DeleteCollection(r.Context(), id, c.account.ID)
	s.replyCommunity(w, r, 204, nil, err)
}

func (s *server) listCollectionTitles(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	q := queryReader{v: r.URL.Query()}
	p := communityPage(&q)
	sort := q.option("sort", "added", "added", "latest", "popular", "trending", "alphabetical")
	def := "desc"
	if sort == "added" || sort == "alphabetical" {
		def = "asc"
	}
	asc := q.option("order", def, "asc", "desc") == "asc"
	if !q.valid(w) {
		return
	}
	result, err := s.catalog.Search(r.Context(), catalog.Query{CollectionID: id, Page: p.Page, Size: p.Size, Sort: catalog.Sort(sort), Asc: asc})
	if errors.Is(err, catalog.ErrNotFound) {
		err = community.ErrNotFound
	}
	var items []titleBody
	if err == nil {
		items, err = s.titleBodies(r, result.Titles)
	}
	s.replyCommunity(w, r, 200, titlesPage{
		Items: items, Page: p.Page, Size: p.Size, TotalItems: result.Total, HasMore: int64(p.Page)*int64(p.Size) < result.Total,
	}, err)
}

func (s *server) voteCollection(w http.ResponseWriter, r *http.Request, c caller) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	choice, ok := voteInput(w, r)
	if !ok {
		return
	}
	data, err := s.community.VoteCollection(r.Context(), id, c.account.ID, choice, s.Config.PublicURL)
	s.replyCommunity(w, r, 200, data, err)
}

func (s *server) reportCollection(w http.ResponseWriter, r *http.Request, c caller) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	reason, details, ok := reportInput(w, r, true)
	if !ok {
		return
	}
	err := s.community.ReportCollection(r.Context(), id, c.account.ID, reason, details)
	s.replyCommunity(w, r, 204, nil, err)
}

func (s *server) importCollection(w http.ResponseWriter, r *http.Request, c caller) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := s.sync.ImportCollection(r.Context(), c.account.ID, id, s.Config.FeatureOn(config.HostedChapters))
	switch {
	case errors.Is(err, usersync.ErrNoAccount):
		writeSessionInvalid(w)
	case errors.Is(err, usersync.ErrCollectionMissing):
		notFound(w, r)
	case errors.Is(err, usersync.ErrCollectionImportLimit):
		writeValidation(w, []fieldError{{Field: "favorite", Code: "too_many"}})
	default:
		s.replyCommunity(w, r, 204, nil, err)
	}
}
