// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const pins = ".kit-pins"

var pinID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func pinDirectory(data, id string) (string, error) {
	if !pinID.MatchString(id) {
		return "", errors.New("invalid backup pin name")
	}
	root := filepath.Join(data, pins)
	if err := checkType(root, true); err != nil {
		return "", err
	}
	return filepath.Join(root, id), nil
}

func pinSource(path string) (string, error) {
	if err := checkType(path, true); err != nil {
		return "", err
	}
	source := filepath.Join(path, "source")
	if err := checkType(source, false); err != nil {
		return "", err
	}
	file, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer file.Close()
	// A pin receipt contains exactly one canonical generation name. Bound the
	// read even if operator data replaced the receipt.
	contents, err := io.ReadAll(io.LimitReader(file, int64(len(layout)+1)))
	if err != nil {
		return "", err
	}
	name := string(contents)
	if !ValidGeneration(name) {
		return "", errors.New("invalid pinned generation")
	}
	return name, nil
}

// linkPayload reconstructs the verified generation's flat directory structure.
// A staged database must get its own inode because SQLite will write to it.
func linkPayload(ctx context.Context, source, dest string, privateDB bool) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil || rel == "." || entry.IsDir() {
			return err
		}
		target := filepath.Join(dest, rel)
		if privateDB && rel == Database {
			return copyImage(path, target)
		}
		return os.Link(path, target)
	})
}

func Pin(ctx context.Context, data, name, id string) error {
	return pin(ctx, data, name, id, false)
}

func pin(ctx context.Context, data, name, id string, justVerified bool) error {
	if !ValidGeneration(name) || !pinID.MatchString(id) {
		return errors.New("invalid generation or pin name")
	}
	root := filepath.Join(data, pins)
	if err := os.Mkdir(root, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	final, err := pinDirectory(data, id)
	if err != nil {
		return err
	}
	if err = syncPath(data); err != nil {
		return err
	}
	present, err := exists(final)
	if err != nil {
		return err
	}
	if present {
		existing, err := pinSource(final)
		if err != nil {
			return err
		}
		if existing != name {
			return errors.New("pin already points to another generation")
		}
		return Verify(ctx, filepath.Join(final, "payload"))
	}
	history := filepath.Join(data, "backups")
	if err = checkType(history, true); err != nil {
		return err
	}
	source := filepath.Join(history, name)
	if !justVerified {
		if err = Verify(ctx, source); err != nil {
			return err
		}
	}
	tmp, err := NewScratch(root, ".staging-")
	if err != nil {
		return err
	}
	defer RemoveScratch(tmp)
	payload := filepath.Join(tmp, "payload")
	dirs := []string{payload, filepath.Join(payload, "covers"), filepath.Join(payload, "pages")}
	for _, dir := range dirs {
		if err = os.Mkdir(dir, 0700); err != nil {
			return err
		}
	}
	if err = linkPayload(ctx, source, payload, false); err != nil {
		return err
	}
	if err = writeFile(filepath.Join(tmp, "source"), strings.NewReader(name)); err != nil {
		return err
	}
	record, err := operationDetails(data)
	if err != nil {
		return err
	}
	if record.Kind == "upgrade" && record.Pin == id {
		if err = writeFile(filepath.Join(tmp, "image-schema"), strings.NewReader(fmt.Sprint(record.Supported))); err != nil {
			return err
		}
	}
	for _, dir := range []string{dirs[1], dirs[2], payload, tmp} {
		if err = syncPath(dir); err != nil {
			return err
		}
	}
	return moveDurably(tmp, final)
}

// StageGeneration uses local immutable images directly, without a tar export or a pin.
func StageGeneration(ctx context.Context, data, name string, maxSchema int64) error {
	if !ValidGeneration(name) {
		return errors.New("invalid backup generation")
	}
	root := filepath.Join(data, "backups")
	if err := checkType(root, true); err != nil {
		return err
	}
	return stageLocal(ctx, data, filepath.Join(root, name), "", maxSchema)
}

func StagePin(ctx context.Context, data, id string, maxSchema int64) error {
	dir, err := pinDirectory(data, id)
	if err != nil {
		return err
	}
	if _, err = pinSource(dir); err != nil {
		return err
	}
	tx := filepath.Join(data, transaction)
	present, err := exists(tx)
	if err != nil {
		return err
	}
	source := filepath.Join(dir, "payload")
	if present {
		if err = CheckSchema(ctx, source, maxSchema); err != nil {
			return err
		}
		staged, err := os.ReadFile(filepath.Join(tx, "pin"))
		if err != nil || string(staged) != id {
			return errors.New("another restore transaction is already staged")
		}
		return nil
	}
	return stageLocal(ctx, data, source, id, maxSchema)
}

func stageLocal(ctx context.Context, data, source, id string, maxSchema int64) error {
	if err := CheckSchema(ctx, source, maxSchema); err != nil {
		return err
	}
	if err := Verify(ctx, source); err != nil {
		return err
	}
	return stage(ctx, data, maxSchema, id, func(next string) error {
		free, err := Available(next)
		if err != nil {
			return err
		}
		info, err := os.Stat(filepath.Join(source, Database))
		if err != nil {
			return err
		}
		if info.Size() > free-SpaceReserve {
			return fmt.Errorf("insufficient free space for a writable copy of the pinned database (%d bytes)", info.Size())
		}
		return linkPayload(ctx, source, next, true)
	})
}

func Unpin(data, id string) error {
	dir, err := pinDirectory(data, id)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	present, err := exists(dir)
	if err != nil {
		return err
	}
	if present {
		if _, err = pinSource(dir); err != nil {
			return err
		}
	}
	return discard(filepath.Dir(dir), filepath.Base(dir))
}
