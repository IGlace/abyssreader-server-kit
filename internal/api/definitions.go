// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/IGlace/abyssreader-server-kit/internal/definitions"
)

func (s *server) listDefinitionKeys(w http.ResponseWriter, r *http.Request) {
	key, err := definitions.ActiveKey(r.Context(), s.DB)
	keys := []definitions.Key{}
	switch {
	case err == nil:
		keys = append(keys, key)
	case !errors.Is(err, sql.ErrNoRows):
		s.storeFailed(w, "read public definition key", err)
		return
	}
	writeJSON(w, http.StatusOK, listBody[definitions.Key]{Items: keys})
}

func (s *server) getDefinitionManifest(w http.ResponseWriter, r *http.Request) {
	entries, err := definitions.Manifest(r.Context(), s.DB)
	if err != nil {
		s.storeFailed(w, "read definition manifest", err)
		return
	}
	s.writeDefinitionJSON(w, r, listBody[definitions.Entry]{Items: entries})
}

func (s *server) writeDefinitionJSON(w http.ResponseWriter, r *http.Request, value any) {
	body, err := json.Marshal(value)
	if err != nil {
		s.storeFailed(w, "encode definitions response", err)
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	w.Header().Set("ETag", etag)
	// The app revalidates with If-None-Match. Keep shared caches out.
	w.Header().Set("Cache-Control", "private, no-cache")
	if etagMatches(r.Header.Values("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(append(body, '\n'))
}

// etagMatches is If-None-Match's weak comparison (RFC 9110 §13.1.2): "*", or any listed tag
// equal to etag with W/ ignored.
func etagMatches(headers []string, etag string) bool {
	for _, h := range headers {
		for _, tag := range strings.Split(h, ",") {
			tag = strings.TrimSpace(tag)
			if tag == "*" || strings.TrimPrefix(tag, "W/") == etag {
				return true
			}
		}
	}
	return false
}

func (s *server) getDefinition(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !sourceSlugPattern.MatchString(slug) {
		writeValidation(w, []fieldError{{Field: "slug", Code: "invalid"}})
		return
	}
	content, err := definitions.Get(r.Context(), s.DB, slug)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found", "This server has no definition for this source.")
	case err != nil:
		s.storeFailed(w, "read definition", err)
	default:
		s.writeDefinitionJSON(w, r, content)
	}
}

func (s *server) reportDefinitionHealth(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	if !sourceSlugPattern.MatchString(slug) {
		writeValidation(w, []fieldError{{Field: "slug", Code: "invalid"}})
		return
	}
	known, err := definitions.KnownSource(r.Context(), s.DB, slug)
	if err != nil {
		s.storeFailed(w, "read definition source", err)
		return
	}
	if !known {
		writeError(w, http.StatusNotFound, "not_found", "This server has no definition for this source.")
		return
	}
	body, ok := readObject(w, r)
	if !ok {
		return
	}
	var items []definitions.HealthItem
	raw, present := body.fields["items"]
	if !present || string(raw) == "null" {
		writeValidation(w, []fieldError{{Field: "items", Code: "required"}})
		return
	}
	if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
		writeValidation(w, []fieldError{{Field: "items", Code: "invalid"}})
		return
	}
	var fields []fieldError
	for i, item := range items {
		if !item.Valid() {
			fields = append(fields, fieldError{Field: "items." + strconv.Itoa(i), Code: "invalid"})
		}
	}
	if len(fields) > 0 {
		writeValidation(w, fields)
		return
	}
	if err := definitions.RecordHealth(r.Context(), s.DB, slug, items); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found", "This server has no definition for this source.")
			return
		}
		s.storeFailed(w, "record definition health", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
