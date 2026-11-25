// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package definitions

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	SecretsFolder = "secrets"
	KeyFile       = "definition-signing-key"
)

type keyring struct {
	Default string            `json:"default"`
	Seeds   map[string][]byte `json:"seeds"`
}

func KeyPath(data string) string {
	return filepath.Join(data, SecretsFolder, KeyFile)
}

func privateKey(ring keyring, signingID string) (ed25519.PrivateKey, error) {
	if signingID == "" {
		signingID = ring.Default
	}
	seed := ring.Seeds[signingID]
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("the active signing key is not available in the private key file")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func readKeyring(path string) (keyring, error) {
	var ring keyring
	st, err := os.Lstat(path)
	if err != nil {
		return ring, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
		return ring, errors.New("definition key must be a regular file with mode 0600")
	}
	f, err := os.Open(path)
	if err != nil {
		return ring, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &ring) != nil ||
		len(ring.Seeds) == 0 || len(ring.Seeds) > 2 || ring.Seeds[ring.Default] == nil {
		return keyring{}, errors.New("invalid definition key file")
	}
	for id, seed := range ring.Seeds {
		if len(seed) != ed25519.SeedSize || PublicKey(ed25519.NewKeyFromSeed(seed)).KeyID != id {
			return keyring{}, errors.New("invalid definition key file")
		}
	}
	return ring, nil
}

func freshKeyring() (keyring, error) {
	key, signer, err := ed25519.GenerateKey(nil)
	if err != nil {
		return keyring{}, err
	}
	id := publicKeyID(key)
	return keyring{Default: id, Seeds: map[string][]byte{id: signer.Seed()}}, nil
}

func writeKeyring(path string, ring keyring, replace bool) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".signing-key-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := json.NewEncoder(file).Encode(ring); err != nil {
		file.Close()
		return errors.New("could not write definition signing key")
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return err
	}
	if replace {
		err = os.Rename(file.Name(), path)
	} else {
		err = os.Link(file.Name(), path)
	}
	if err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// CreateKey creates a new offline key or a new kit key and refuses to overwrite.
func CreateKey(path string) (string, error) {
	ring, err := freshKeyring()
	if err != nil {
		return "", err
	}
	if err = writeKeyring(path, ring, false); err != nil {
		return "", err
	}
	signer, err := privateKey(ring, "")
	if err != nil {
		return "", err
	}
	return Fingerprint(signer.Public().(ed25519.PublicKey)), nil
}

func EnsureSecretsFolder(data string) error {
	secretDir := filepath.Join(data, SecretsFolder)
	if err := os.Mkdir(secretDir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	st, err := os.Lstat(secretDir)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode().Perm() != 0700 {
		return errors.New("the kit secrets folder must be a directory with mode 0700")
	}
	return nil
}

func EnsureLocalKey(data string) error {
	if err := EnsureSecretsFolder(data); err != nil {
		return err
	}
	path := KeyPath(data)
	if _, err := readKeyring(path); !os.IsNotExist(err) {
		return err
	}
	if _, err := CreateKey(path); err != nil && !os.IsExist(err) {
		return err
	}
	_, err := readKeyring(path)
	return err
}

func ReadSigner(path, id string) (ed25519.PrivateKey, error) {
	ring, err := readKeyring(path)
	if err != nil {
		return nil, fmt.Errorf("read definition signing key: %w", err)
	}
	return privateKey(ring, id)
}

// LocalKeyID returns the server's effective private-key identity without exposing
// any key bytes. A two-seed ring uses the identity already committed in SQLite.
func LocalKeyID(path, activeID string) (string, error) {
	ring, err := readKeyring(path)
	if err != nil {
		return "", err
	}
	if _, ok := ring.Seeds[activeID]; ok {
		return activeID, nil
	}
	return ring.Default, nil
}

// ReconcileLocalKey removes the inactive seed after an interrupted rotation.
// A missing or unrelated one-seed file is valid when signing is offline.
func ReconcileLocalKey(path, activeID string) error {
	ring, err := readKeyring(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil || len(ring.Seeds) == 1 {
		return err
	}
	seed, ok := ring.Seeds[activeID]
	if !ok {
		return errors.New("the committed signing key is missing from the interrupted rotation file")
	}
	return writeKeyring(path, keyring{Default: activeID, Seeds: map[string][]byte{activeID: seed}}, true)
}
