// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

type Generation struct {
	Name       string
	Created    time.Time
	Raw        bool
	Incomplete bool
	Missing    []string
}

// List reports marked generations newest first without reading image payloads.
func List(ctx context.Context, data string) ([]Generation, error) {
	return readGenerations(ctx, filepath.Join(data, "backups"), 0)
}

// Latest reads names newest-first until it finds the first kit-owned manifest.
func Latest(data string) (Generation, bool, error) {
	entries, err := readGenerations(context.Background(), filepath.Join(data, "backups"), 1)
	if err != nil || len(entries) == 0 {
		return Generation{}, false, err
	}
	return entries[0], true, nil
}

func readGenerations(ctx context.Context, root string, limit int) ([]Generation, error) {
	if err := checkType(root, true); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	dirs, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var entries []Generation
	for i := len(dirs) - 1; i >= 0; i-- {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		entry := dirs[i]
		if !entry.IsDir() || !ValidGeneration(entry.Name()) {
			continue
		}
		m, err := header(filepath.Join(root, entry.Name()))
		if err != nil {
			if !isForeignSource(err) {
				return nil, err
			}
			continue
		}
		if generation(m.generationTime()) != entry.Name() {
			continue
		}
		entries = append(entries, Generation{Name: entry.Name(), Created: m.Created, Raw: m.Raw, Incomplete: m.Incomplete, Missing: m.Missing})
		if limit > 0 && len(entries) == limit {
			break
		}
	}
	return entries, nil
}

// VerifyRecovery accepts a declared incomplete generation only when every
// omitted path is a snapshot reference and every remaining member verifies.
func VerifyRecovery(ctx context.Context, dir string) error {
	return verify(ctx, dir, verifyPreserved, nil)
}

func ExportRecovery(ctx context.Context, dir string, out io.Writer) error {
	if err := VerifyRecovery(ctx, dir); err != nil {
		return err
	}
	return writeArchive(ctx, dir, out)
}

// VerifyRecoveryArchive verifies the exported bytes without staging a transaction.
func VerifyRecoveryArchive(ctx context.Context, data string, in io.Reader, expected ...string) (err error) {
	scratch, err := NewScratch(data, ".staging-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, RemoveScratch(scratch)) }()
	payload := filepath.Join(scratch, "payload")
	for _, dir := range []string{payload, filepath.Join(payload, "covers"), filepath.Join(payload, "pages")} {
		if err = os.Mkdir(dir, 0700); err != nil {
			return err
		}
	}
	if err = extractBounded(ctx, payload, in); err != nil {
		return err
	}
	if len(expected) > 0 {
		if len(expected) != 1 || !ValidGeneration(expected[0]) {
			return errors.New("invalid generation for archive verification")
		}
		// Bind the verified external bytes (including omitted paths or raw digest)
		// to the generation checked by the volume-deletion guard.
		source := filepath.Join(data, "backups", expected[0])
		if err = checkType(source, true); err != nil {
			return err
		}
		want, e := digest(filepath.Join(source, manifestFile))
		if e != nil {
			return e
		}
		got, e := digest(filepath.Join(payload, manifestFile))
		if e != nil {
			return e
		}
		if want != got {
			return errors.New("exported archive belongs to a different generation; keeping the volume")
		}
	}
	return VerifyRecovery(ctx, payload)
}
