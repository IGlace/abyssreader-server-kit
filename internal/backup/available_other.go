// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

//go:build !linux && !darwin && !freebsd && !dragonfly

package backup

import (
	"fmt"
	"os"
	"runtime"
)

func Available(path string) (int64, error) {
	return 0, fmt.Errorf("backup free-space checks are unavailable on %s", runtime.GOOS)
}

// SameFile still guards cache hits when portable file IDs are unavailable.
func physicalImageID(_ os.FileInfo, name string) imageID { return imageID{fallback: name} }
