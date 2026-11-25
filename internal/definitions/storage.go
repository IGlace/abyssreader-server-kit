// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package definitions

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
)

type reader interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// ActiveKey is the one public key currently used to sign the manifest.
func ActiveKey(ctx context.Context, db reader) (Key, error) {
	var key Key
	var by, endorsement sql.NullString
	err := db.QueryRowContext(ctx, `SELECT key_id, public_key, endorsed_by, endorsement FROM definition_key WHERE slot=1`).
		Scan(&key.KeyID, &key.PublicKey, &by, &endorsement)
	if by.Valid {
		key.Endorsement = &Endorsement{ByKeyID: by.String, Signature: endorsement.String}
	}
	return key, err
}

func Manifest(ctx context.Context, db reader) ([]Entry, error) {
	rows, err := db.QueryContext(ctx, `SELECT source_slug, version, content_hash, signature, key_id, min_app_version
		FROM operator_definitions CROSS JOIN definition_key WHERE slot=1 ORDER BY source_slug`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Entry{}
	for rows.Next() {
		var entry Entry
		if err := rows.Scan(&entry.SourceSlug, &entry.DefinitionVersion, &entry.ContentHash, &entry.Signature,
			&entry.KeyID, &entry.MinAppVersion); err != nil {
			return nil, err
		}
		entry.Revoked = entry.ContentHash == nil
		items = append(items, entry)
	}
	return items, rows.Err()
}

func ActiveSources(ctx context.Context, db reader) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT source_slug FROM operator_definitions WHERE content IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := make(map[string]bool)
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		found[slug] = true
	}
	return found, rows.Err()
}

func Get(ctx context.Context, db reader, slug string) (Content, error) {
	var c Content
	err := db.QueryRowContext(ctx, `SELECT content, content_hash, signature, key_id, version
		FROM operator_definitions CROSS JOIN definition_key
		WHERE source_slug=? AND content IS NOT NULL AND slot=1`, slug).
		Scan(&c.Content, &c.ContentHash, &c.Signature, &c.KeyID, &c.DefinitionVersion)
	return c, err
}

// Publish verifies signed bytes before writing. Only the local signing key or
// an explicitly authorized offline key may establish the initial server key.
func Publish(ctx context.Context, db *sql.DB, bundle Bundle, initialKeyID string) error {
	if err := bundle.verify(); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	active, err := ActiveKey(ctx, tx)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if bundle.KeyID != initialKeyID {
			return errors.New("initial publication requires the kit signing key or explicit trust in the offline signing key")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO definition_key (slot, key_id, public_key) VALUES (1, ?, ?)`,
			bundle.KeyID, bundle.PublicKey); err != nil {
			return err
		}
	case err != nil:
		return err
	case active.KeyID != bundle.KeyID || active.PublicKey != bundle.PublicKey:
		return errors.New("the signed bundle does not use this server's active signing key")
	}
	if err := putBundle(ctx, tx, bundle); err != nil {
		return err
	}
	return tx.Commit()
}

func putBundle(ctx context.Context, tx *sql.Tx, bundle Bundle) error {
	const maxSources = 2048
	result, err := tx.ExecContext(ctx, `INSERT INTO operator_definitions (source_slug, version, content, content_hash, signature, min_app_version)
		SELECT ?, ?, ?, ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM operator_definitions WHERE source_slug=?) OR (SELECT count(*) FROM operator_definitions) < ?
		ON CONFLICT (source_slug) DO UPDATE SET version=excluded.version, content=excluded.content,
		content_hash=excluded.content_hash, signature=excluded.signature, min_app_version=excluded.min_app_version
		WHERE version < excluded.version`,
		bundle.SourceSlug, bundle.DefinitionVersion, bundle.Content, bundle.ContentHash, bundle.Signature,
		bundle.MinAppVersion, bundle.SourceSlug, maxSources)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return errors.New("publish requires a version newer than the stored version and room in the manifest")
	}
	return nil
}

// Rotate endorses a new key, re-signs every entry at its next version and
// publishes the replacement atomically. The private file is written before
// committing the registry, with both keys so a failed commit keeps the old key.
func Rotate(ctx context.Context, db *sql.DB, keyPath string) (string, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	old, err := ActiveKey(ctx, tx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errors.New("publish a definition before rotating its signing key")
	}
	if err != nil {
		return "", err
	}
	ring, err := readKeyring(keyPath)
	if err != nil {
		return "", err
	}
	previous, err := privateKey(ring, old.KeyID)
	if err != nil {
		return "", err
	}
	next, err := freshKeyring()
	if err != nil {
		return "", err
	}
	signer, err := privateKey(next, "")
	if err != nil {
		return "", err
	}
	newKey := PublicKey(signer)
	endorsement := signature(previous, endorsementStatement(old, newKey))
	entries, err := Manifest(ctx, tx)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if e.DefinitionVersion == MaxVersion {
			return "", fmt.Errorf("cannot rotate: %s has reached the maximum definition version", e.SourceSlug)
		}
		e.DefinitionVersion++
		sig := signature(signer, e.statement())
		if _, err := tx.ExecContext(ctx, `UPDATE operator_definitions SET version=?, signature=? WHERE source_slug=?`,
			e.DefinitionVersion, sig, e.SourceSlug); err != nil {
			return "", err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE definition_key SET key_id=?, public_key=?, endorsed_by=?, endorsement=? WHERE slot=1`,
		newKey.KeyID, newKey.PublicKey, old.KeyID, endorsement); err != nil {
		return "", err
	}
	ready := keyring{Default: old.KeyID, Seeds: map[string][]byte{
		old.KeyID:    previous.Seed(),
		newKey.KeyID: signer.Seed(),
	}}
	if err := writeKeyring(keyPath, ready, true); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	// Subsequent signing consults the public registry, so interruption here is
	// also safe. A successful cleanup leaves only the current private key.
	if err := writeKeyring(keyPath, next, true); err != nil {
		return "", fmt.Errorf("rotation committed; could not remove the retired key from its private file: %w", err)
	}
	return Fingerprint(signer.Public().(ed25519.PublicKey)), nil
}

// ResetPublication removes the archive's stale signed offers and health counts
// during a confirmed restore. Its database is offline and the outer operation
// will keep the original files available until the replacement boots.
func ResetPublication(ctx context.Context, db *sql.DB, keep *Key) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"definition_health", "operator_definitions", "definition_key"} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return err
		}
	}
	if keep != nil {
		var by, endorsement *string
		if keep.Endorsement != nil {
			by = &keep.Endorsement.ByKeyID
			endorsement = &keep.Endorsement.Signature
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO definition_key (slot,key_id,public_key,endorsed_by,endorsement)
			VALUES (1,?,?,?,?)`, keep.KeyID, keep.PublicKey, by, endorsement); err != nil {
			return err
		}
	}
	return tx.Commit()
}
