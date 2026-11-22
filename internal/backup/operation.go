// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const operation = ".kit-operation"
const permit = ".kit-boot-permit"

func exists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func removeIfPresent(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func mark(dir, name string) error {
	path := filepath.Join(dir, name)
	err := writeFile(path, strings.NewReader(""))
	if os.IsExist(err) {
		err = checkType(path, false)
	}
	if err != nil {
		return err
	}
	return syncPath(dir)
}

// BeginOperation makes a plain container restart fail closed before any installer change.
func BeginOperation(data string) error { return mark(data, operation) }

func pending(data string) (bool, error) {
	for _, name := range []string{operation, transaction} {
		present, err := exists(filepath.Join(data, name))
		if present || err != nil {
			return present, err
		}
	}
	return false, nil
}

// PermitBoot arms a single validation boot. The installer passes this value only to
// the selected server container. Its restart policy can never replay a consumed permit.
func PermitBoot(data string) (string, error) {
	active, err := pending(data)
	if err != nil {
		return "", err
	}
	if !active {
		return "", errors.New("no pending kit operation")
	}
	var nonce [32]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(nonce[:])
	path := filepath.Join(data, permit)
	if err = removeIfPresent(path); err != nil {
		return "", err
	}
	if err = writeFile(path, strings.NewReader(token)); err != nil {
		return "", err
	}
	return token, syncPath(data)
}

func CheckBoot(data, token string) error {
	active, err := pending(data)
	if err != nil || !active {
		return err
	}
	denied := errors.New("pending kit operation: run install.sh recover before starting the server")
	if len(token) != 64 {
		return denied
	}
	path := filepath.Join(data, permit)
	if err = checkType(path, false); err != nil {
		return denied
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	want, err := io.ReadAll(io.LimitReader(file, 65))
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(want, []byte(token)) != 1 {
		return denied
	}
	// Only the process whose unlink succeeds may open SQLite.
	if err = os.Remove(path); err != nil {
		return fmt.Errorf("consume kit boot permit: %w", err)
	}
	return syncPath(data)
}

func EndOperation(data string) error {
	if err := removeIfPresent(filepath.Join(data, permit)); err != nil {
		return err
	}
	if err := removeIfPresent(filepath.Join(data, operation)); err != nil {
		return err
	}
	return syncPath(data)
}
