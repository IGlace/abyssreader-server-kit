// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package definitions

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const MaxVersion = 2147483647

type Key struct {
	KeyID       string       `json:"keyId"`
	PublicKey   string       `json:"publicKey"`
	Endorsement *Endorsement `json:"endorsement"`
}

type Endorsement struct {
	ByKeyID   string `json:"byKeyId"`
	Signature string `json:"signature"`
}

type Entry struct {
	SourceSlug        string  `json:"sourceSlug"`
	DefinitionVersion int64   `json:"definitionVersion"`
	ContentHash       *string `json:"contentHash"`
	Signature         string  `json:"signature"`
	KeyID             string  `json:"keyId"`
	MinAppVersion     *string `json:"minAppVersion"`
	Revoked           bool    `json:"revoked"`
}

type Bundle struct {
	Entry
	PublicKey string  `json:"publicKey"`
	Content   *string `json:"content"`
}

type Content struct {
	Content           string `json:"content"`
	ContentHash       string `json:"contentHash"`
	Signature         string `json:"signature"`
	KeyID             string `json:"keyId"`
	DefinitionVersion int64  `json:"definitionVersion"`
}

func Fingerprint(key ed25519.PublicKey) string {
	hash := sha256.Sum256(key)
	text := strings.ToUpper(hex.EncodeToString(hash[:4]))
	return text[:4] + "-" + text[4:]
}

func publicKeyID(key ed25519.PublicKey) string {
	hash := sha256.Sum256(key)
	return hex.EncodeToString(hash[:16])
}

func PublicKey(signer ed25519.PrivateKey) Key {
	key := signer.Public().(ed25519.PublicKey)
	return Key{KeyID: publicKeyID(key), PublicKey: base64.StdEncoding.EncodeToString(key)}
}

func validSlug(slug string) bool {
	switch slug {
	case "keys", "manifest", "hosted":
		return false
	default:
		return len(slug) <= 64 && slugPattern.MatchString(slug)
	}
}

func validIdentity(slug string, version int64) bool {
	return validSlug(slug) && version >= 1 && version <= MaxVersion
}

func (e Entry) statement() []byte {
	if e.Revoked {
		return fmt.Appendf(nil, "abyssreader-revocation-v2\n%s\n%d", e.SourceSlug, e.DefinitionVersion)
	}
	return fmt.Appendf(nil, "abyssreader-definition-v2\n%s\n%d\n%s", e.SourceSlug, e.DefinitionVersion, *e.ContentHash)
}

func endorsementStatement(old, next Key) []byte {
	return fmt.Appendf(nil, "abyssreader-key-endorsement-v1\n%s\n%s\n%s", old.KeyID, next.KeyID, next.PublicKey)
}

func signature(signer ed25519.PrivateKey, statement []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(signer, statement))
}

func sign(signer ed25519.PrivateKey, entry Entry, text *string) Bundle {
	key := PublicKey(signer)
	entry.KeyID = key.KeyID
	entry.Signature = signature(signer, entry.statement())
	return Bundle{Entry: entry, PublicKey: key.PublicKey, Content: text}
}

// Sign hashes the exact UTF-8 bytes, including any whitespace in the file.
func Sign(signer ed25519.PrivateKey, version int64, raw []byte) (Bundle, error) {
	meta, err := Lint(raw)
	if err != nil {
		return Bundle{}, err
	}
	if !validIdentity(meta.Slug, version) {
		return Bundle{}, errors.New("definition version must be between 1 and 2147483647")
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	text := string(raw)
	return sign(signer, Entry{
		SourceSlug: meta.Slug, DefinitionVersion: version,
		ContentHash: &hash, MinAppVersion: meta.MinAppVersion,
	}, &text), nil
}

func SignRevocation(signer ed25519.PrivateKey, slug string, version int64) (Bundle, error) {
	if !validIdentity(slug, version) {
		return Bundle{}, errors.New("expected a source slug and version between 1 and 2147483647")
	}
	return sign(signer, Entry{SourceSlug: slug, DefinitionVersion: version, Revoked: true}, nil), nil
}

func (b Bundle) verify() error {
	if !validIdentity(b.SourceSlug, b.DefinitionVersion) {
		return errors.New("invalid signed source slug or version")
	}
	key, err := base64.StdEncoding.Strict().DecodeString(b.PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize || publicKeyID(key) != b.KeyID ||
		base64.StdEncoding.EncodeToString(key) != b.PublicKey {
		return errors.New("invalid signing public key")
	}
	if b.Revoked {
		if b.Content != nil || b.ContentHash != nil || b.MinAppVersion != nil {
			return errors.New("a revocation cannot include definition content")
		}
	} else {
		if b.Content == nil || b.ContentHash == nil {
			return errors.New("signed definition is missing content or hash")
		}
		meta, err := Lint([]byte(*b.Content))
		if err != nil {
			return err
		}
		hash := sha256.Sum256([]byte(*b.Content))
		if *b.ContentHash != hex.EncodeToString(hash[:]) || b.SourceSlug != meta.Slug ||
			!equalVersion(b.MinAppVersion, meta.MinAppVersion) {
			return errors.New("signed definition metadata does not match its content")
		}
	}
	sig, err := base64.StdEncoding.Strict().DecodeString(b.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(sig) != b.Signature ||
		!ed25519.Verify(key, b.statement(), sig) {
		return errors.New("invalid definition signature")
	}
	return nil
}

func equalVersion(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
