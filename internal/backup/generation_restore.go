// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/IGlace/abyssreader-server-kit/internal/covers"
	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

// ExportGeneration turns a generation into a portable, self-contained tar stream. Verify before
// emitting the first byte. The caller holds the same lock as pruning.
func ExportGeneration(ctx context.Context, dir string, out io.Writer) error {
	if err := Verify(ctx, dir); err != nil {
		return err
	}
	return writeArchive(ctx, dir, out)
}

func writeArchive(ctx context.Context, dir string, out io.Writer) error {
	tw := tar.NewWriter(out)
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if err = tw.WriteHeader(&tar.Header{Name: rel, Mode: 0600, Size: info.Size()}); err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		return err
	}
	return tw.Close()
}

const transaction = ".kit-restore"

var liveFiles = []string{Database, Database + "-wal", Database + "-shm", Database + "-journal", "covers", "pages"}

func validMember(name string) bool {
	if isRawDBFile(name) || name == manifestFile {
		return true
	}
	folder, image, ok := strings.Cut(name, "/")
	return ok && (folder == "covers" || folder == "pages") && covers.ValidName(image)
}

// Stage verifies an archive against the image's embedded schema before publishing it.
func Stage(ctx context.Context, data string, in io.Reader) error {
	return StageWithLimit(ctx, data, in, store.MaxVersion())
}

const maxArchiveEntries = 1000000

// Bound the whole tar stream (including metadata hidden by archive/tar) and
// periodically recheck shared disk capacity while a large member is being read.
type stagingInput struct {
	source     io.Reader
	budget     int64
	untilCheck int64
	space      func() (int64, error)
}

func (in *stagingInput) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if in.budget <= 0 {
		return 0, errors.New("restore input exceeds available free space")
	}
	if in.untilCheck == 0 {
		free, err := in.space()
		if err != nil {
			return 0, err
		}
		if free <= SpaceReserve {
			return 0, fmt.Errorf("insufficient free space while staging: %d bytes available, reserve %d", free, SpaceReserve)
		}
		in.untilCheck = min(8<<20, free-SpaceReserve)
	}
	n, err := in.source.Read(p[:min(int64(len(p)), in.budget, in.untilCheck)])
	in.untilCheck -= int64(n)
	in.budget -= int64(n)
	return n, err
}

func StageWithLimit(ctx context.Context, data string, in io.Reader, maxSchema int64) error {
	return stage(ctx, data, maxSchema, "", func(next string) error {
		return extractBounded(ctx, next, in)
	})
}

func extractBounded(ctx context.Context, next string, in io.Reader) error {
	free, err := Available(next)
	if err != nil {
		return err
	}
	budget := free - SpaceReserve
	input := &stagingInput{source: in, budget: budget, space: func() (int64, error) { return Available(next) }}
	return extractArchive(ctx, next, input, budget, maxArchiveEntries)
}

func extractArchive(ctx context.Context, next string, in io.Reader, budget int64, limit int) error {
	tr := tar.NewReader(in)
	for i := 0; ; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("invalid kit archive: %w", err)
		}
		if i >= limit {
			return errors.New("kit archive has too many entries")
		}
		if !validMember(h.Name) || h.Typeflag != tar.TypeReg {
			return errors.New("invalid kit archive member")
		}
		// Allow metadata and filesystem allocation overhead even for tiny images.
		if budget < 4096 || h.Size > budget-4096 {
			return errors.New("insufficient free space to stage kit archive")
		}
		budget -= h.Size + 4096
		if err = writeFile(filepath.Join(next, h.Name), tr); err != nil {
			return err
		}
	}
}

func stage(ctx context.Context, data string, maxSchema int64, pin string, populate func(string) error) error {
	if present, err := exists(filepath.Join(data, transaction)); err != nil {
		return err
	} else if present {
		return errors.New("a restore is already staged; run install.sh recover")
	}

	tmp, err := NewScratch(data, ".staging-")
	if err != nil {
		return err
	}
	defer RemoveScratch(tmp)
	next := filepath.Join(tmp, "new")
	dirs := []string{next, filepath.Join(next, "covers"), filepath.Join(next, "pages"), filepath.Join(tmp, "old")}
	for _, dir := range dirs {
		if err = os.Mkdir(dir, 0700); err != nil {
			return err
		}
	}
	if err = populate(next); err != nil {
		return err
	}
	if err = Verify(ctx, next); err != nil {
		return err
	}
	if err = CheckSchema(ctx, next, maxSchema); err != nil {
		return err
	}
	if pin != "" {
		if err = writeFile(filepath.Join(tmp, "pin"), strings.NewReader(pin)); err != nil {
			return err
		}
	}
	for _, p := range []string{dirs[1], dirs[2], next, dirs[3], tmp} {
		if err = syncPath(p); err != nil {
			return err
		}
	}
	return moveDurably(tmp, filepath.Join(data, transaction))
}

func DescribeStage(ctx context.Context, data string) (string, error) {
	return Description(ctx, filepath.Join(data, transaction, "new"))
}

// Apply preserves the CLI's rejection of an accidental second swap. Recovery can call
// ResumeApply after any interruption, including loss of the first call's success response.
func Apply(data string) error {
	done, err := exists(filepath.Join(data, transaction, "applied"))
	if err != nil {
		return err
	}
	if done {
		return errors.New("restore has already been applied")
	}
	return ResumeApply(data)
}

// moveDurably flushes the destination before the source so later steps may rely on
// the directory entries as their on-disk progress record.
func moveDurably(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return err
	}
	if err := syncPath(filepath.Dir(to)); err != nil {
		return err
	}
	if filepath.Dir(from) != filepath.Dir(to) {
		return syncPath(filepath.Dir(from))
	}
	return nil
}

func ResumeApply(data string) error {
	tx := filepath.Join(data, transaction)
	old := filepath.Join(tx, "old")
	next := filepath.Join(tx, "new")
	undoing, err := exists(filepath.Join(tx, "undoing"))
	if err != nil {
		return err
	}
	if undoing {
		return errors.New("restore rollback has started")
	}
	done, err := exists(filepath.Join(tx, "applied"))
	if err != nil || done {
		return err
	}
	started, err := exists(filepath.Join(tx, "applying"))
	if err != nil {
		return err
	}
	if !started {
		if err = Verify(context.Background(), next); err != nil {
			return err
		}
		if err = mark(tx, "applying"); err != nil {
			return err
		}
	}
	for _, name := range liveFiles {
		saved := filepath.Join(old, name)
		present, err := exists(saved)
		if err != nil {
			return err
		}
		if present {
			continue
		}
		absent, err := exists(filepath.Join(old, "absent-"+name))
		if err != nil {
			return err
		}
		if absent {
			continue
		}
		live := filepath.Join(data, name)
		hasLive, err := exists(live)
		if err != nil {
			return err
		}
		if hasLive {
			err = moveDurably(live, saved)
		} else {
			err = mark(old, "absent-"+name)
		}
		if err != nil {
			return err
		}
	}
	// A previous attempt may have renamed the last original and failed its fsync.
	// Flush both parents even when this call found every original already saved.
	if err = syncPath(old); err != nil {
		return err
	}
	if err = syncPath(data); err != nil {
		return err
	}
	for _, name := range []string{Database, "covers", "pages"} {
		from := filepath.Join(next, name)
		left, err := exists(from)
		if err != nil {
			return err
		}
		live := filepath.Join(data, name)
		if left {
			if err = moveDurably(from, live); err != nil {
				return err
			}
		} else if _, err = os.Lstat(live); err != nil {
			return fmt.Errorf("missing installed %s: %w", name, err)
		}
	}
	if err = syncPath(data); err != nil {
		return err
	}
	if err = syncPath(next); err != nil {
		return err
	}
	return mark(tx, "applied")
}

func Undo(data string) error {
	tx := filepath.Join(data, transaction)
	old := filepath.Join(tx, "old")
	done, err := exists(filepath.Join(tx, "undone"))
	if err != nil || done {
		return err
	}
	if err = mark(tx, "undoing"); err != nil {
		return err
	}
	for _, name := range liveFiles {
		live := filepath.Join(data, name)
		saved := filepath.Join(old, name)
		present, err := exists(saved)
		if err != nil {
			return err
		}
		if present {
			if err = os.RemoveAll(live); err != nil {
				return err
			}
			if err = moveDurably(saved, live); err != nil {
				return err
			}
			continue
		}
		absent, err := exists(filepath.Join(old, "absent-"+name))
		if err != nil {
			return err
		}
		if absent {
			if err = os.RemoveAll(live); err != nil {
				return err
			}
			if err = syncPath(data); err != nil {
				return err
			}
		}
	}
	if err = syncPath(data); err != nil {
		return err
	}
	if err = syncPath(old); err != nil {
		return err
	}
	return mark(tx, "undone")
}

// Atomically detach a completed tree before recursive cleanup. A crash during cleanup
// leaves an owned scratch directory for the next invocation.
func discard(parent, name string) error {
	tmp, err := NewScratch(parent, ".staging-")
	if err != nil {
		return err
	}
	defer RemoveScratch(tmp)
	source := filepath.Join(parent, name)
	present, err := exists(source)
	if err != nil || !present {
		return err
	}
	return moveDurably(source, filepath.Join(tmp, "discard"))
}

func Finish(data string) error { return discard(data, transaction) }

// Ready refuses new operations on a volume whose previous recovery is still pending.
func Ready(data string) error {
	active, err := pending(data)
	if err != nil {
		return err
	}
	if active {
		return errors.New("pending kit operation on the data volume; run install.sh recover")
	}
	return nil
}
