// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchiveMemberBound(t *testing.T) {
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for i := 0; i < 4; i++ {
		if err := w.WriteHeader(&tar.Header{Name: fmt.Sprintf("covers/%064x.png", i), Mode: 0600}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "covers"), 0700); err != nil {
		t.Fatal(err)
	}
	err := extractArchive(context.Background(), dir, &b, 1024*1024, 2)
	if err == nil || !strings.Contains(err.Error(), "entries") {
		t.Fatal(err)
	}
}

func TestStageHonoursConcurrentDiskUsage(t *testing.T) {
	const capacity = int64(16 << 20)
	free := capacity + SpaceReserve
	input := &stagingInput{source: strings.NewReader(strings.Repeat("x", int(capacity))), budget: capacity,
		space: func() (int64, error) { return free, nil }}
	if n, err := io.CopyN(io.Discard, input, 8<<20); err != nil || n != 8<<20 {
		t.Fatal(n, err)
	}
	free = SpaceReserve
	if n, err := input.Read(make([]byte, 512)); n != 0 || err == nil || !strings.Contains(err.Error(), "free space") {
		t.Fatal(n, err)
	}
}
