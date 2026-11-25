// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/backup"
	"github.com/IGlace/abyssreader-server-kit/internal/definitions"
)

func TestDefinitionTrustRejectsDisplayFingerprintAndPrefixOnlyMatch(t *testing.T) {
	dir := t.TempDir()
	getenv := func(k string) string {
		if k == "ABYSS_DATA_DIR" {
			return dir
		}
		return ""
	}
	ctx := t.Context()
	db, err := openDatabase(ctx, getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// These two synthetic fixture keys share a display fingerprint.
	fixtureKey := func(index uint32) ed25519.PrivateKey {
		var seed [ed25519.SeedSize]byte
		binary.LittleEndian.PutUint32(seed[:], index)
		copy(seed[4:], "kit signing test fixture")
		return ed25519.NewKeyFromSeed(seed[:])
	}
	trusted := fixtureKey(3751)
	signer := fixtureKey(7845)
	fingerprint := definitions.Fingerprint(trusted.Public().(ed25519.PublicKey))
	trustedID := definitions.PublicKey(trusted).KeyID
	if fingerprint != definitions.Fingerprint(signer.Public().(ed25519.PublicKey)) ||
		trustedID == definitions.PublicKey(signer).KeyID {
		t.Fatal("expected distinct fixture keys with the same display fingerprint")
	}
	bundle, err := definitions.SignRevocation(signer, "paper-lantern", 1)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "offline.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		trust   string
		wantErr string
	}{
		{fingerprint, "requires the complete keyId"},
		{trustedID, "does not match --trust-key"},
		{bundle.KeyID, ""},
	}
	for _, tc := range cases {
		var stdout, stderr bytes.Buffer
		result := run(ctx, []string{"definitions", "publish", "--trust-key", tc.trust, path}, &stdout, &stderr, getenv)
		if (result == 0) != (tc.wantErr == "") || !strings.Contains(stderr.String(), tc.wantErr) {
			t.Fatalf("unexpected trust result, code %d: %s", result, &stderr)
		}
	}
}

func TestDefinitionInterruptedRotationKeepsCommittedPrivateKey(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed-%t", committed), func(t *testing.T) {
			dir := t.TempDir()
			getenv := func(k string) string {
				if k == "ABYSS_DATA_DIR" {
					return dir
				}
				return ""
			}
			ctx := t.Context()
			db, err := openDatabase(ctx, getenv)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := definitions.EnsureSecretsFolder(dir); err != nil {
				t.Fatal(err)
			}
			_, oldSigner, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			_, newSigner, err := ed25519.GenerateKey(nil)
			if err != nil {
				t.Fatal(err)
			}
			oldID, newID := definitions.PublicKey(oldSigner).KeyID, definitions.PublicKey(newSigner).KeyID
			active := oldSigner
			if committed {
				active = newSigner
			}
			entry, err := definitions.SignRevocation(active, "paper-lantern", 3)
			if err != nil {
				t.Fatal(err)
			}
			if err := definitions.Publish(ctx, db, entry, entry.KeyID); err != nil {
				t.Fatal(err)
			}
			path := definitions.KeyPath(dir)
			interrupted := map[string]any{
				"default": oldID,
				"seeds":   map[string]string{oldID: base64.StdEncoding.EncodeToString(oldSigner.Seed()), newID: base64.StdEncoding.EncodeToString(newSigner.Seed())},
			}
			data, err := json.Marshal(interrupted)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if err := ensureDefinitionKey(dir, db); err != nil {
				t.Fatal(err)
			}
			defaultSigner, err := definitions.ReadSigner(path, "")
			if err != nil || !defaultSigner.Equal(active) {
				t.Fatal("startup selected the wrong signing key")
			}
			retired := newID
			if committed {
				retired = oldID
			}
			if _, err := definitions.ReadSigner(path, retired); err == nil {
				t.Fatal("startup retained the inactive seed")
			}
		})
	}
}

func TestDefinitionRotationRejectsRetiredBundlesAndResignsContent(t *testing.T) {
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
	ctx := t.Context()
	path := definitions.KeyPath(dir)
	oldSigner, err := definitions.ReadSigner(path, "")
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) []byte {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join("..", "..", "examples", "definitions", name))
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	content, err := definitions.Resolve(read("family.json"), read("source.json"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := definitions.Sign(oldSigner, 1, content)
	if err != nil {
		t.Fatal(err)
	}
	if err := definitions.Publish(ctx, db, first, first.KeyID); err != nil {
		t.Fatal(err)
	}
	stale, err := definitions.Sign(oldSigner, 100, content)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := definitions.Rotate(ctx, db, path); err != nil {
		t.Fatal(err)
	}
	if err := definitions.Publish(ctx, db, stale, stale.KeyID); err == nil || !strings.Contains(err.Error(), "active signing key") {
		t.Fatalf("retired key with a larger version was accepted: %v", err)
	}
	active, err := definitions.ActiveKey(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := base64.StdEncoding.DecodeString(active.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	items, err := definitions.Manifest(ctx, db)
	if err != nil || len(items) != 1 || items[0].ContentHash == nil {
		t.Fatalf("missing re-signed manifest: %v", err)
	}
	served, err := definitions.Get(ctx, db, first.SourceSlug)
	if err != nil {
		t.Fatal(err)
	}
	item := items[0]
	if item.KeyID != active.KeyID || served.KeyID != active.KeyID ||
		item.DefinitionVersion != 2 || served.DefinitionVersion != item.DefinitionVersion ||
		served.ContentHash != *item.ContentHash || served.Content != string(content) {
		t.Fatal("rotated manifest and content are inconsistent")
	}
	statement := fmt.Appendf(nil, "abyssreader-definition-v2\n%s\n%d\n%s", first.SourceSlug, item.DefinitionVersion, served.ContentHash)
	for _, signature := range []string{item.Signature, served.Signature} {
		sig, err := base64.StdEncoding.DecodeString(signature)
		if err != nil || !ed25519.Verify(pub, statement, sig) {
			t.Fatal("rotated definition signature did not verify with the new key")
		}
	}
}

func TestDefinitionMaintenancePreviewAcceptsExplicitRestoreOverride(t *testing.T) {
	dir := t.TempDir()
	getenv := func(k string) string {
		if k == "ABYSS_DATA_DIR" {
			return dir
		}
		return ""
	}
	ctx := t.Context()
	db, err := openDatabase(ctx, getenv)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	path := definitions.KeyPath(dir)
	if err := ensureDefinitionKey(dir, db); err != nil {
		t.Fatal(err)
	}
	signer, err := definitions.ReadSigner(path, "")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := definitions.SignRevocation(signer, "paper-lantern", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := definitions.Publish(ctx, db, entry, entry.KeyID); err != nil {
		t.Fatal(err)
	}
	gen, err := backup.Create(ctx, dir, db, 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if err := backup.ExportGeneration(ctx, gen, &archive); err != nil {
		t.Fatal(err)
	}
	if _, err := definitions.Rotate(ctx, db, path); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"preview", "13"},
		{"preview", "13", "--allow-definition-rollback"},
		{"preview-generation", filepath.Base(gen), "13", "--allow-definition-rollback"},
	} {
		var out, stderr bytes.Buffer
		exit := maintenance(ctx, args, bytes.NewReader(archive.Bytes()), &out, &stderr, getenv)
		explicit := args[len(args)-1] == "--allow-definition-rollback"
		if (exit == 0) != explicit {
			t.Fatalf("preview exit = %d, override = %v: %s", exit, explicit, &stderr)
		}
		if explicit {
			if err := backup.DiscardPreview(dir, strings.TrimSpace(out.String())); err != nil {
				t.Fatal(err)
			}
		}
	}
}
