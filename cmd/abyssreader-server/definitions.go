// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/IGlace/abyssreader-server-kit/internal/backup"
	"github.com/IGlace/abyssreader-server-kit/internal/config"
	"github.com/IGlace/abyssreader-server-kit/internal/definitions"
)

const definitionUsage = `  abyssreader-server definitions keygen [FILE]           create a private signing key
  abyssreader-server definitions resolve FAMILY SOURCE OUTPUT
                                                         merge a family and source into a definition
  abyssreader-server definitions lint DEFINITION          check a resolved definition
  abyssreader-server definitions sign [--key FILE] VERSION DEFINITION OUTPUT
                                                         create a signed publication bundle
  abyssreader-server definitions publish [--trust-key KEY_ID] BUNDLE
                                                         publish a signed bundle to this server
  abyssreader-server definitions revoke SLUG VERSION      publish a signed revocation
  abyssreader-server definitions revoke --key FILE SLUG VERSION OUTPUT
                                                         create an offline revocation bundle
  abyssreader-server definitions rotate                   endorse a new key and re-sign every entry
  abyssreader-server definitions health [SLUG]            show aggregate definition error counts
`

func ensureDefinitionKey(data string, db *sql.DB) error {
	unlock, err := lockData(data)
	if errors.Is(err, errLocked) {
		// Let an active administrator finish. A two-seed file can already
		// sign with the committed key and will be reconciled on next startup.
		return nil
	}
	if err != nil {
		return err
	}
	defer unlock()
	active, err := definitions.ActiveKey(context.Background(), db)
	if errors.Is(err, sql.ErrNoRows) {
		return definitions.EnsureLocalKey(data)
	}
	if err != nil {
		return err
	}
	return definitions.ReconcileLocalKey(definitions.KeyPath(data), active.KeyID)
}

func definitionCommand(ctx context.Context, args []string, out, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 || len(args) <= 2 && (args[len(args)-1] == "--help" || args[len(args)-1] == "-h") {
		fmt.Fprint(out, definitionUsage)
		return 0
	}
	err := runDefinition(ctx, args[0], args[1:], out, getenv)
	if errors.Is(err, errDefinitionUsage) {
		fmt.Fprint(stderr, definitionUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, terminalText(err.Error()))
		return 1
	}
	return 0
}

var errDefinitionUsage = errors.New("invalid definitions command")

func runDefinition(ctx context.Context, command string, args []string, out io.Writer, getenv func(string) string) error {
	data := config.DataDir(getenv)
	path := definitions.KeyPath(data)
	switch command {
	case "keygen":
		switch len(args) {
		case 0:
			if err := definitions.EnsureSecretsFolder(data); err != nil {
				return err
			}
		case 1:
			path = args[0]
		default:
			return errDefinitionUsage
		}
		fingerprint, err := definitions.CreateKey(path)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "Signing key fingerprint:", fingerprint)
		return nil
	case "resolve":
		if len(args) != 3 {
			return errDefinitionUsage
		}
		family, err := readDefinitionInput(args[0], definitions.MaxContent)
		if err != nil {
			return err
		}
		source, err := readDefinitionInput(args[1], definitions.MaxContent)
		if err != nil {
			return err
		}
		resolved, err := definitions.Resolve(family, source)
		if err != nil {
			return err
		}
		return writeDefinitionOutput(args[2], resolved)
	case "lint":
		if len(args) != 1 {
			return errDefinitionUsage
		}
		raw, err := readDefinitionInput(args[0], definitions.MaxContent)
		if err != nil {
			return err
		}
		result, err := definitions.Lint(raw)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "Valid definition:", result.Slug)
		return nil
	case "sign", "revoke":
		if len(args) >= 2 && args[0] == "--key" {
			signer, err := definitions.ReadSigner(args[1], "")
			if err != nil {
				return err
			}
			return outputSignedDefinition(command, args[2:], signer, out)
		}
	case "publish", "rotate", "health":
	default:
		return errDefinitionUsage
	}
	if err := backup.Ready(data); err != nil {
		return err
	}
	switch command {
	case "publish", "revoke", "rotate":
		unlock, err := lockData(data)
		if err != nil {
			return err
		}
		defer unlock()
	}
	var result error
	code := withDatabase(ctx, io.Discard, getenv, func(db *sql.DB) int {
		result = definitionStoreCommand(ctx, db, path, command, args, out)
		return 0
	})
	if code != 0 {
		return errors.New("could not open the installed kit database")
	}
	return result
}

func definitionStoreCommand(ctx context.Context, db *sql.DB, keyPath, command string, args []string, out io.Writer) error {
	switch command {
	case "sign":
		signer, err := activeDefinitionSigner(ctx, db, keyPath)
		if err != nil {
			return err
		}
		return outputSignedDefinition(command, args, signer, out)
	case "publish":
		return publishDefinitionBundle(ctx, db, keyPath, args, out)
	case "revoke":
		if len(args) != 2 {
			return errDefinitionUsage
		}
		version, err := definitionVersion(args[1])
		if err != nil {
			return err
		}
		signer, err := activeDefinitionSigner(ctx, db, keyPath)
		if err != nil {
			return err
		}
		bundle, err := definitions.SignRevocation(signer, args[0], version)
		if err != nil {
			return err
		}
		if err := definitions.Publish(ctx, db, bundle, bundle.KeyID); err != nil {
			return err
		}
		fmt.Fprintf(out, "Revoked %s at version %d\n", bundle.SourceSlug, bundle.DefinitionVersion)
	case "rotate":
		if len(args) != 0 {
			return errDefinitionUsage
		}
		fingerprint, err := definitions.Rotate(ctx, db, keyPath)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, "Signing key fingerprint:", fingerprint)
	case "health":
		if len(args) > 1 {
			return errDefinitionUsage
		}
		slug := ""
		if len(args) == 1 {
			slug = args[0]
		}
		counts, err := definitions.Health(ctx, db, slug)
		if err != nil {
			return err
		}
		if len(counts) == 0 {
			fmt.Fprintln(out, "No definition health reports.")
		}
		for _, item := range counts {
			fmt.Fprintf(out, "%s\t%s\t%d\n", item.Slug, item.Kind, item.Count)
		}
	default:
		return errDefinitionUsage
	}
	return nil
}

func publishDefinitionBundle(ctx context.Context, db *sql.DB, keyPath string, args []string, out io.Writer) error {
	trusted := ""
	if len(args) == 3 && args[0] == "--trust-key" {
		trusted = args[1]
		keyID, err := hex.DecodeString(trusted)
		if err != nil || len(keyID) != 16 || strings.ToLower(trusted) != trusted {
			return errors.New("--trust-key requires the complete keyId from the trusted offline bundle (32 lower-case hexadecimal digits)")
		}
		args = args[2:]
	}
	if len(args) != 1 {
		return errDefinitionUsage
	}
	raw, err := readDefinitionInput(args[0], 6*definitions.MaxContent)
	if err != nil {
		return err
	}
	var bundle definitions.Bundle
	if json.Unmarshal(raw, &bundle) != nil {
		return errors.New("invalid signed definition bundle")
	}
	pub, err := base64.StdEncoding.DecodeString(bundle.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("invalid public key in signed bundle")
	}
	fingerprint := definitions.Fingerprint(pub)
	initial := ""
	if trusted != "" {
		if bundle.KeyID != trusted {
			return errors.New("the signed bundle's key ID does not match --trust-key")
		}
		initial = bundle.KeyID
	} else if signer, err := definitions.ReadSigner(keyPath, ""); err == nil {
		initial = definitions.PublicKey(signer).KeyID
	}
	if err := definitions.Publish(ctx, db, bundle, initial); err != nil {
		return err
	}
	fmt.Fprintf(out, "Published %s at version %d. Signing key fingerprint: %s\n",
		bundle.SourceSlug, bundle.DefinitionVersion, fingerprint)
	return nil
}

func activeDefinitionSigner(ctx context.Context, db *sql.DB, path string) (ed25519.PrivateKey, error) {
	active, err := definitions.ActiveKey(ctx, db)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return definitions.ReadSigner(path, active.KeyID)
}

func definitionVersion(raw string) (int64, error) {
	version, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || version < 1 {
		return 0, errors.New("expected a version between 1 and 2147483647")
	}
	return version, nil
}

func outputSignedDefinition(command string, args []string, signer ed25519.PrivateKey, out io.Writer) error {
	if len(args) != 3 {
		return errDefinitionUsage
	}
	var bundle definitions.Bundle
	var err error
	if command == "revoke" {
		version, err := definitionVersion(args[1])
		if err != nil {
			return err
		}
		bundle, err = definitions.SignRevocation(signer, args[0], version)
		if err != nil {
			return err
		}
	} else {
		version, err := definitionVersion(args[0])
		if err != nil {
			return err
		}
		raw, err := readDefinitionInput(args[1], definitions.MaxContent)
		if err != nil {
			return err
		}
		bundle, err = definitions.Sign(signer, version, raw)
		if err != nil {
			return err
		}
	}
	raw, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	if err := writeDefinitionOutput(args[2], raw); err != nil {
		return err
	}
	fmt.Fprintln(out, "Signing key fingerprint:", definitions.Fingerprint(signer.Public().(ed25519.PublicKey)))
	return nil
}

func readDefinitionInput(path string, max int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(max) {
		return nil, errors.New("definition input is not a regular file within the size limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, int64(max)+1))
	if len(raw) > max {
		return nil, errors.New("definition input exceeds the size limit")
	}
	return raw, err
}

func writeDefinitionOutput(path string, raw []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(append(raw, '\n'))
	if err := errors.Join(err, file.Close()); err != nil {
		os.Remove(path)
		return err
	}
	return nil
}
