// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

//go:build unix

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

var errLocked = errors.New("locked")

// lockData takes the data folder's admin lock without waiting; errLocked when another
// command holds it.
func lockData(dataDir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dataDir, lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
