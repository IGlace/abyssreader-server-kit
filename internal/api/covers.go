// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"net/http"

	"github.com/IGlace/abyssreader-server-kit/internal/covers"
)

// coversPath is where stored covers are served, outside the base path: images load with no
// headers, so neither the access key nor a session applies. The URL is
// ABYSS_PUBLIC_URL + coversPath + the file's name, the SHA-256 of its bytes.
const coversPath = "/covers/"

// serveCover answers GET and HEAD /covers/{name}. A name always means the same bytes, so the
// answer may be cached forever.
func (s *server) serveCover(w http.ResponseWriter, r *http.Request) {
	if s.Covers == nil {
		notFound(w, r)
		return
	}
	serveImage(w, r, s.Covers, r.PathValue("name"))
}

// serveImage serves one stored image of a content-addressed store. Its Content-Type comes from
// the format the import sniffed from its bytes (the file's extension), never from the request.
func serveImage(w http.ResponseWriter, r *http.Request, store *covers.Store, name string) {
	f, format, err := store.Open(name)
	if err != nil {
		notFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeServerError(w)
		return
	}
	h := w.Header()
	h.Set("Content-Type", format.ContentType)
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("ETag", `"`+name+`"`)
	http.ServeContent(w, r, "", st.ModTime(), f)
}
