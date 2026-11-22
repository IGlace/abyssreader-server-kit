// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// A damaged catalog cannot enumerate its media or say which old snapshots
// can repair it. Before deleting that volume, save the complete stopped tree
// outside it. This salvage tar is deliberately separate from a kit restore tar.
func ExportStoppedVolume(ctx context.Context, data string, out io.Writer) error {
	if err := checkType(data, true); err != nil {
		return err
	}
	tw := tar.NewWriter(out)
	type original struct {
		name string
		info os.FileInfo
	}
	links := make(map[imageID]original)
	err := filepath.WalkDir(data, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if path == data {
			return nil
		}
		rel, err := filepath.Rel(data, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		h := &tar.Header{Name: filepath.ToSlash(rel), Mode: 0600, Typeflag: tar.TypeReg, Size: info.Size()}
		if info.IsDir() {
			h.Typeflag = tar.TypeDir
			h.Mode = 0700
			h.Size = 0
			return tw.WriteHeader(h)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("cannot preserve special volume entry %s; keep the volume", rel)
		}
		id := physicalImageID(info, rel)
		if prior, ok := links[id]; ok && os.SameFile(prior.info, info) {
			h.Typeflag = tar.TypeLink
			h.Size = 0
			h.Linkname = prior.name
			return tw.WriteHeader(h)
		}
		links[id] = original{h.Name, info}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, writeErr := io.Copy(tw, f)
		return errors.Join(writeErr, f.Close())
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

// Re-encode independently from the still-stopped volume; verify every member
// and byte without extracting an operator's files or needing a second disk copy.
func CheckStoppedVolumeArchive(ctx context.Context, data string, in io.Reader) error {
	want, got := sha256.New(), sha256.New()
	if err := ExportStoppedVolume(ctx, data, want); err != nil {
		return err
	}
	if _, err := io.Copy(got, in); err != nil {
		return err
	}
	if !bytes.Equal(want.Sum(nil), got.Sum(nil)) {
		return errors.New("full-volume salvage archive does not match the stopped data volume; deletion refused")
	}
	return nil
}
