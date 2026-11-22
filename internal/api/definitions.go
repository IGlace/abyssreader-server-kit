// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// The core "sources and definitions" routes of a server without definitions may return
// an empty key list and an empty manifest. Signing keys, the manifest's entries,
// the definitions themselves and their health counts come with definition support; until then every source
// title has hasDefinition false.

// hasDefinition reports whether the definitions manifest has a source's slug.
func (s *server) hasDefinition(sourceSlug string) bool { return false }

type definitionKeyBody struct{} // keyId, publicKey, endorsement once definitions are supported

type manifestEntryBody struct{} // sourceSlug, definitionVersion, contentHash, … when definitions are supported

// listDefinitionKeys answers GET /api/v1/definitions/keys.
func (s *server) listDefinitionKeys(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, listBody[definitionKeyBody]{Items: []definitionKeyBody{}})
}

// getDefinitionManifest answers GET /api/v1/definitions/manifest, with the ETag the contract
// requires and 304 when If-None-Match names it.
func (s *server) getDefinitionManifest(w http.ResponseWriter, r *http.Request) {
	body, _ := json.Marshal(listBody[manifestEntryBody]{Items: []manifestEntryBody{}})
	sum := sha256.Sum256(body)
	etag := `"m-` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("ETag", etag)
	// The app revalidates with If-None-Match; nothing may serve it a stored copy unasked.
	w.Header().Set("Cache-Control", "no-cache")
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

// getDefinition answers GET /api/v1/definitions/{slug}: no source has a definition here.
func (s *server) getDefinition(w http.ResponseWriter, r *http.Request) {
	if !sourceSlugPattern.MatchString(r.PathValue("slug")) {
		writeValidation(w, []fieldError{{Field: "slug", Code: "invalid"}})
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "This server has no definition for this source.")
}

// reportDefinitionHealth answers POST /api/v1/definitions/{slug}/health: an unknown slug is
// 404, and no slug has a definition here.
func (s *server) reportDefinitionHealth(w http.ResponseWriter, r *http.Request) {
	if !sourceSlugPattern.MatchString(r.PathValue("slug")) {
		writeValidation(w, []fieldError{{Field: "slug", Code: "invalid"}})
		return
	}
	writeError(w, http.StatusNotFound, "not_found", "This server has no definition for this source.")
}
