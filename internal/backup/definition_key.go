// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/IGlace/abyssreader-server-kit/internal/definitions"
)

const keyBackupMember = definitions.SecretsFolder + "/" + definitions.KeyFile

func keyBackupInfo(dir string) (os.FileInfo, error) {
	folder := filepath.Join(dir, definitions.SecretsFolder)
	if err := checkType(folder, true); err != nil {
		return nil, err
	}
	info, err := os.Lstat(definitions.KeyPath(dir))
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 4096 {
		return nil, errors.New("invalid definition signing key file")
	}
	return info, nil
}

func copyDefinitionKey(data, backup string) (string, error) {
	if _, err := keyBackupInfo(data); os.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	if err := definitions.EnsureSecretsFolder(backup); err != nil {
		return "", err
	}
	target := definitions.KeyPath(backup)
	if err := copyImage(definitions.KeyPath(data), target); err != nil {
		return "", err
	}
	if err := syncSecrets(backup); err != nil {
		return "", err
	}
	return digest(target)
}

func syncSecrets(dir string) error {
	path := filepath.Join(dir, definitions.SecretsFolder)
	if err := checkType(path, true); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	return syncPath(path)
}

func restoreLivePath(data, name string) string {
	if name == definitions.KeyFile {
		return definitions.KeyPath(data)
	}
	return filepath.Join(data, name)
}
