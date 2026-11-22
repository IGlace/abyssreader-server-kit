// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const scratchMarker = ".kit-scratch"

func markerData(base string) []byte { return []byte(format + "\n" + base + "\n") }

// CleanScratch removes abandoned private work directories after a day. The exact
// marker identifies its directory, so a moved marker never authorizes a deletion.
func CleanScratch(parent, prefix string) error {
	if prefix != ".writing-" && prefix != ".staging-" {
		return errors.New("invalid backup scratch prefix")
	}
	if err := checkType(parent, true); err != nil {
		return err
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-24 * time.Hour)
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		path := filepath.Join(parent, entry.Name())
		owned, err := ownedScratch(path, cutoff)
		if err != nil {
			return err
		}
		if owned {
			if err = removeScratchContents(path); err != nil {
				return fmt.Errorf("clean stale backup scratch: %w", err)
			}
		}
	}
	return nil
}

func NewScratch(parent, prefix string) (string, error) {
	if err := CleanScratch(parent, prefix); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(parent, prefix)
	if err != nil {
		return "", err
	}
	err = writeFile(filepath.Join(dir, scratchMarker), bytes.NewReader(markerData(filepath.Base(dir))))
	if err == nil {
		err = syncPath(dir)
	}
	if err == nil {
		err = syncPath(parent)
	}
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// RemoveScratch removes only an owned work directory. Keep the marker until the
// large payload is gone so cleanup can be resumed after power loss.
func RemoveScratch(dir string) error {
	if err := checkType(dir, true); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	base := filepath.Base(dir)
	if !strings.HasPrefix(base, ".writing-") && !strings.HasPrefix(base, ".staging-") {
		return errors.New("unexpected scratch directory name")
	}
	owned, err := ownedScratch(dir, time.Time{})
	if err != nil {
		return err
	}
	if !owned {
		return errors.New("invalid scratch marker")
	}
	return removeScratchContents(dir)
}

func ownedScratch(dir string, cutoff time.Time) (bool, error) {
	marker := filepath.Join(dir, scratchMarker)
	expected := markerData(filepath.Base(dir))
	st, err := os.Lstat(marker)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !st.Mode().IsRegular() || st.Size() != int64(len(expected)) || !cutoff.IsZero() && !st.ModTime().Before(cutoff) {
		return false, nil
	}
	actual, err := os.ReadFile(marker)
	return bytes.Equal(actual, expected), err
}

func removeScratchContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != scratchMarker {
			if err = os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
				return err
			}
		}
	}
	if err = syncPath(dir); err != nil {
		return err
	}
	if err = os.Remove(filepath.Join(dir, scratchMarker)); err != nil {
		return err
	}
	return os.Remove(dir)
}
