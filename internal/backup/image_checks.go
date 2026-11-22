// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"fmt"
	"os"
	"path/filepath"
)

type imageID struct {
	device, inode uint64
	fallback      string
}

type checkedImage struct {
	info        os.FileInfo
	digest      string
	privateCopy string
}

// imageChecks lives for one locked creation/pruning operation. Store the actual
// digest (even on mismatch) so each hard link can compare its own expected name.
type imageChecks map[imageID]checkedImage

func sameImageVersion(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func (c imageChecks) checksum(path string) (string, error) {
	// Independent verification always reads the file.
	if c == nil {
		return digest(path)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s", errUnexpectedType, path)
	}
	if previous, ok := c.lookup(path, info); ok {
		return previous.digest, nil
	}
	sum, err := digest(path)
	if err != nil {
		return "", err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !sameImageVersion(info, after) {
		return "", fmt.Errorf("backup image changed during verification: %s", path)
	}
	c.remember(path, after, sum, "")
	return sum, nil
}

// A newly streamed private copy already has a checksum from the same write.
func (c imageChecks) recordCopy(path, sum string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s", errUnexpectedType, path)
	}
	c.remember(path, info, sum, path)
	return nil
}

func (c imageChecks) lookup(path string, info os.FileInfo) (checkedImage, bool) {
	previous, ok := c[physicalImageID(info, filepath.Base(path))]
	return previous, ok && sameImageVersion(previous.info, info)
}

func (c imageChecks) remember(path string, info os.FileInfo, sum, privateCopy string) {
	c[physicalImageID(info, filepath.Base(path))] = checkedImage{info: info, digest: sum, privateCopy: privateCopy}
}
