// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"crypto/ed25519"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/definitions"
)

func TestPublishedDefinitionRoutes(t *testing.T) {
	h := newHarness(t, nil)
	fixtures := filepath.Join("..", "..", "examples", "definitions")
	read := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(fixtures, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	resolved, err := definitions.Resolve(read("family.json"), read("source.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, signer, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	bundle, err := definitions.Sign(signer, 1, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if err := definitions.Publish(ctx, h.db, bundle, bundle.KeyID); err != nil {
		t.Fatal(err)
	}
	h.importCatalog(obj{
		"manifestVersion": 1,
		"sources": []obj{{"slug": bundle.SourceSlug, "name": "Paper Lantern Library",
			"website": "https://lantern.example.com", "lang": "en"}},
		"titles": []obj{{"ref": "lantern-keeper", "slug": "lantern-keeper", "name": "The Lantern Keeper",
			"sourceTitles": []obj{{"source": bundle.SourceSlug, "url": "/lantern-keeper/"}}}},
	}, nil)
	assertSourceFlag := func(want bool) {
		t.Helper()
		title := h.do(http.MethodGet, "/api/v1/titles/by-slug/lantern-keeper", req{})
		if title.status != http.StatusOK {
			t.Fatalf("title: %d %s", title.status, title.raw)
		}
		source := title.body["sources"].([]any)[0].(map[string]any)
		if source["hasDefinition"] != want {
			t.Fatalf("hasDefinition = %v, want %v", source["hasDefinition"], want)
		}
	}
	assertSourceFlag(true)
	const root = "/api/v1/definitions/"
	keys := h.do(http.MethodGet, root+"keys", req{})
	if keys.status != http.StatusOK || len(keys.body["items"].([]any)) != 1 {
		t.Fatalf("keys: %d %s", keys.status, keys.raw)
	}
	for _, path := range []string{root + "manifest", root + bundle.SourceSlug} {
		first := h.do(http.MethodGet, path, req{})
		if first.status != http.StatusOK || first.header.Get("ETag") == "" {
			t.Fatalf("definition GET %s: %d %s", path, first.status, first.raw)
		}
		second := h.do(http.MethodGet, path, req{header: map[string]string{"If-None-Match": first.header.Get("ETag")}})
		if second.status != http.StatusNotModified {
			t.Fatalf("definition GET %s with tag: %d %s", path, second.status, second.raw)
		}
	}
	health := root + bundle.SourceSlug + "/health"
	if result := h.do(http.MethodPost, health, req{body: `{"items":[{"definitionVersion":1,"errorKind":"a_future_kind","count":2}]}`}); result.status != http.StatusNoContent {
		t.Fatalf("health: %d %s", result.status, result.raw)
	}
	if result := h.do(http.MethodPost, health, req{body: `{"items":[{"definitionVersion":1,"errorKind":"a future kind","count":2}]}`}); result.status != http.StatusBadRequest {
		t.Fatalf("malformed health: %d %s", result.status, result.raw)
	}
	counts, err := definitions.Health(ctx, h.db, bundle.SourceSlug)
	if err != nil || len(counts) != 1 || counts[0].Count != 2 {
		t.Fatalf("health not stored: %v %v", counts, err)
	}
	revocation, err := definitions.SignRevocation(signer, bundle.SourceSlug, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := definitions.Publish(ctx, h.db, revocation, bundle.KeyID); err != nil {
		t.Fatal(err)
	}
	assertSourceFlag(false)
	result := h.do(http.MethodGet, root+bundle.SourceSlug, req{})
	if result.status != http.StatusNotFound {
		t.Fatalf("revoked content remains visible: %d %s", result.status, result.raw)
	}
	result = h.do(http.MethodGet, root+"manifest", req{})
	items := result.body["items"].([]any)
	if result.status != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["revoked"] != true {
		t.Fatalf("signed revocation missing: %d %s", result.status, result.raw)
	}
}
