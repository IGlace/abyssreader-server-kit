// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

//go:build linux || darwin || freebsd || dragonfly

package backup

import (
	"math"
	"os"
	"syscall"
)

// Available returns bytes writable by the current user on the target volume.
func Available(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	if st.Bavail <= 0 {
		return 0, nil
	}
	// Clamp before multiplication on filesystems that report effectively unlimited space.
	blocks, size := uint64(st.Bavail), uint64(st.Bsize)
	if size > 0 && blocks > math.MaxInt64/size {
		return math.MaxInt64, nil
	}
	return int64(blocks * size), nil
}

func physicalImageID(info os.FileInfo, _ string) imageID {
	stat := info.Sys().(*syscall.Stat_t)
	return imageID{device: uint64(stat.Dev), inode: uint64(stat.Ino)}
}
