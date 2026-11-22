// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/IGlace/abyssreader-server-kit/internal/catalog"
	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

// maxPushBody caps a push's body. The app keeps a batch near 512 KiB (500 changes at most); a
// larger body is a 400 validation_failed (the push declares no 413).
const maxPushBody = 1 << 20

// perAccount wraps a signed-in handler with a per-account request budget: 429 rate_limited
// with Retry-After once it is spent. A per-account budget, unlike a per-client one, holds for
// an account whatever address it pushes from.
func (s *server) perAccount(l *limiter, h func(http.ResponseWriter, *http.Request, caller)) func(http.ResponseWriter, *http.Request, caller) {
	return func(w http.ResponseWriter, r *http.Request, c caller) {
		if wait, ok := l.allow("account " + strconv.FormatInt(c.account.ID, 10)); !ok {
			writeRateLimited(w, wait)
			return
		}
		h(w, r, c)
	}
}

// getLibrary answers GET /api/v1/me/library: one of the caller's lists, as titles, with the
// search's paging, q, sort and order. A title the server hides (a hosted-only title while
// hosted-chapters is off) is left out, as everywhere, and comes back with the feature.
func (s *server) getLibrary(w http.ResponseWriter, r *http.Request, c caller) {
	q := &queryReader{v: r.URL.Query()}
	list, ok := q.single("list")
	switch {
	case !ok && len(q.v["list"]) == 0:
		q.bad("list", "required")
	case !ok:
	case list != string(usersync.Favorite) && list != string(usersync.Planning) && list != string(usersync.Blacklist):
		q.bad("list", "unknown_value")
	}
	query := q.titleQuery()
	if len(q.fields) > 0 {
		writeValidation(w, q.fields)
		return
	}
	query.Library = &catalog.Library{UserID: c.account.ID, Kind: list}
	s.answerTitles(w, r, query)
}

// syncPush answers POST /api/v1/sync/push. The envelope (the sync schema's pushEnvelope: one
// object, only `items`, 1 to 500 objects) is a 400; each item is then judged alone.
func (s *server) syncPush(w http.ResponseWriter, r *http.Request, c caller) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxPushBody))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeValidation(w, []fieldError{{Field: "items", Code: "too_large"}})
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", "The request body could not be read.")
		return
	}
	items, field := pushEnvelope(b)
	if field != nil {
		writeValidation(w, []fieldError{*field})
		return
	}
	out, err := s.sync.Push(r.Context(), c.account.ID, items)
	if errors.Is(err, usersync.ErrNoAccount) {
		writeSessionInvalid(w) // the account was deleted while the push ran
		return
	}
	if err != nil {
		s.storeFailed(w, "sync push", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// pushEnvelope checks the push body against the schema's pushEnvelope and returns its items.
func pushEnvelope(b []byte) ([]json.RawMessage, *fieldError) {
	dec := json.NewDecoder(bytes.NewReader(b))
	var body map[string]json.RawMessage
	if err := dec.Decode(&body); err != nil {
		return nil, &fieldError{Field: "items", Code: "invalid"}
	}
	if body == nil {
		return nil, &fieldError{Field: "items", Code: "required"}
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, &fieldError{Field: "items", Code: "invalid"}
	}
	for name := range body {
		if name != "items" {
			return nil, &fieldError{Field: name, Code: "unknown_field"}
		}
	}
	raw, ok := body["items"]
	if !ok || string(raw) == "null" {
		return nil, &fieldError{Field: "items", Code: "required"}
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, &fieldError{Field: "items", Code: "invalid"}
	}
	switch {
	case len(items) == 0:
		return nil, &fieldError{Field: "items", Code: "too_short"}
	case len(items) > usersync.MaxBatch:
		return nil, &fieldError{Field: "items", Code: "too_many"}
	}
	for _, it := range items {
		if t := bytes.TrimLeft(it, " \t\r\n"); len(t) == 0 || t[0] != '{' {
			return nil, &fieldError{Field: "items", Code: "invalid"}
		}
	}
	return items, nil
}

// syncPull answers GET /api/v1/sync/pull.
func (s *server) syncPull(w http.ResponseWriter, r *http.Request, c caller) {
	q := &queryReader{v: r.URL.Query()}
	limit := q.integer("limit", usersync.DefaultPull, 1, usersync.MaxPull)
	var cursor *string
	if text, ok := q.single("cursor"); ok {
		if n := len(text); n < 1 || n > 200 { // the schema's cursor (the server's own are ASCII)
			q.bad("cursor", "invalid")
		}
		cursor = &text
	}
	if len(q.fields) > 0 {
		writeValidation(w, q.fields)
		return
	}
	page, err := s.sync.Pull(r.Context(), c.account.ID, cursor, limit)
	switch {
	case errors.Is(err, usersync.ErrBadCursor):
		writeValidation(w, []fieldError{{Field: "cursor", Code: "invalid"}})
	case errors.Is(err, usersync.ErrFullResync):
		writeJSON(w, http.StatusConflict, map[string]bool{"fullResyncRequired": true})
	case err != nil:
		s.storeFailed(w, "sync pull", err)
	default:
		writeJSON(w, http.StatusOK, page)
	}
}
