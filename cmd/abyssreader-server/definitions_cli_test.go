// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/definitions"
)

func TestDefinitionKeygenCreatesPrivateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "offline.key")
	var out, stderr bytes.Buffer
	code := run(t.Context(), []string{"definitions", "keygen", path}, &out, &stderr, func(string) string { return "" })
	if code != 0 {
		t.Fatalf("keygen exit %d: %s", code, stderr.String())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("private file mode is %o", info.Mode().Perm())
	}
	if !strings.HasPrefix(out.String(), "Signing key fingerprint: ") || strings.Count(out.String(), "\n") != 1 {
		t.Fatalf("unexpected keygen output")
	}
	out.Reset()
	stderr.Reset()
	if run(t.Context(), []string{"definitions", "keygen", path}, &out, &stderr, func(string) string { return "" }) == 0 {
		t.Fatal("keygen replaced an existing key")
	}
}

func TestDefinitionCLIWorkflow(t *testing.T) {
	dir := t.TempDir()
	getenv := func(k string) string {
		if k == "ABYSS_DATA_DIR" {
			return dir
		}
		return ""
	}
	db, err := openDatabase(t.Context(), getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := ensureDefinitionKey(dir, db); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "resolved.json")
	bundle := filepath.Join(dir, "bundle.json")
	fixtures := filepath.Join("..", "..", "examples", "definitions")
	call := func(want int, args ...string) string {
		t.Helper()
		var out, stderr bytes.Buffer
		code := run(t.Context(), append([]string{"definitions"}, args...), &out, &stderr, getenv)
		if code != want {
			t.Fatalf("%s exit = %d, want %d: %s", args[0], code, want, stderr.String())
		}
		return out.String()
	}
	call(0, "resolve", filepath.Join(fixtures, "family.json"), filepath.Join(fixtures, "source.json"), path)
	call(0, "lint", path)
	call(0, "sign", "1", path, bundle)
	call(0, "publish", bundle)
	call(1, "publish", bundle)
	first, err := definitions.ActiveKey(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	call(0, "rotate")
	second, err := definitions.ActiveKey(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if first.KeyID == second.KeyID || second.Endorsement == nil || second.Endorsement.ByKeyID != first.KeyID {
		t.Fatal("rotation did not endorse a new key")
	}
	keyBytes, _ := base64.StdEncoding.DecodeString(first.PublicKey)
	signature, _ := base64.StdEncoding.DecodeString(second.Endorsement.Signature)
	statement := []byte("abyssreader-key-endorsement-v1\n" + first.KeyID + "\n" + second.KeyID + "\n" + second.PublicKey)
	if !ed25519.Verify(keyBytes, statement, signature) {
		t.Fatal("rotation endorsement signature did not verify")
	}
	content, err := definitions.Get(t.Context(), db, "paper-lantern")
	if err != nil || content.KeyID != second.KeyID || content.DefinitionVersion != 2 {
		t.Fatalf("rotation did not re-sign and advance version: %v", err)
	}
	call(1, "revoke", "paper-lantern", "2")
	call(0, "revoke", "paper-lantern", "3")
	items, err := definitions.Manifest(t.Context(), db)
	if err != nil || len(items) != 1 || !items[0].Revoked || items[0].DefinitionVersion != 3 {
		t.Fatalf("revoke did not publish the version floor: %v", err)
	}
}

func TestDefinitionOfflineSigningRequiresKeyIDTrust(t *testing.T) {
	dir := t.TempDir()
	getenv := func(k string) string {
		if k == "ABYSS_DATA_DIR" {
			return dir
		}
		return ""
	}
	db, err := openDatabase(t.Context(), getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := ensureDefinitionKey(dir, db); err != nil {
		t.Fatal(err)
	}
	resolved, bundle, offline := filepath.Join(dir, "resolved.json"), filepath.Join(dir, "bundle.json"), filepath.Join(dir, "offline.key")
	fixtures := filepath.Join("..", "..", "examples", "definitions")
	call := func(want int, args ...string) string {
		t.Helper()
		var out, stderr bytes.Buffer
		code := run(t.Context(), append([]string{"definitions"}, args...), &out, &stderr, getenv)
		if code != want {
			t.Fatalf("%s exit = %d, want %d: %s", args[0], code, want, stderr.String())
		}
		return out.String()
	}
	call(0, "keygen", offline)
	call(0, "resolve", filepath.Join(fixtures, "family.json"), filepath.Join(fixtures, "source.json"), resolved)
	call(0, "sign", "--key", offline, "1", resolved, bundle)
	call(1, "publish", bundle)
	call(1, "publish", "--trust-key", "AAAA-BBBB", bundle)
	signer, err := definitions.ReadSigner(offline, "")
	if err != nil {
		t.Fatal(err)
	}
	call(0, "publish", "--trust-key", definitions.PublicKey(signer).KeyID, bundle)
	revocation := filepath.Join(dir, "revoke.json")
	call(0, "revoke", "--key", offline, "paper-lantern", "2", revocation)
	call(0, "publish", revocation)
}
