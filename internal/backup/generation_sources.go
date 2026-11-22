// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/IGlace/abyssreader-server-kit/internal/covers"
)

// Index filenames by digest on first use of a source. Old generations are visited
// newest-first; an intact recent snapshot usually supplies every requested image.
type imageSource struct {
	path      string
	files     map[string][]string
	probeOnly bool
}

type imageSources struct {
	history     []*imageSource
	live        *imageSource
	wanted      map[string]bool
	owned       map[string]bool
	damagedPins []*imageSource
	salvage     []*imageSource
}

// Missing/non-kit metadata identifies a foreign entry. A filesystem error
// leaves ownership unknown and must stop the operation, especially deletion.
func isForeignSource(err error) bool {
	var pathErr *os.PathError
	return os.IsNotExist(err) || !errors.As(err, &pathErr)
}

func findSources(ctx context.Context, data string, prior []Generation, refs []string) (imageSources, error) {
	s := imageSources{live: &imageSource{path: data}, wanted: make(map[string]bool, len(refs)), owned: make(map[string]bool, len(prior))}
	if len(refs) == 0 {
		return s, nil
	}
	for _, rel := range refs {
		s.wanted[filepath.Base(rel)[:64]] = true
	}
	for _, gen := range prior {
		s.owned[gen.Name] = true
		s.history = append(s.history, &imageSource{path: filepath.Join(data, "backups", gen.Name)})
	}
	root := filepath.Join(data, pins)
	if err := checkType(root, true); os.IsNotExist(err) {
		return s, nil
	} else if err != nil {
		return s, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return s, err
	}
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return s, err
		}
		if !entry.IsDir() || !pinID.MatchString(entry.Name()) {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		source, receiptErr := pinSource(dir)
		if receiptErr != nil && !isForeignSource(receiptErr) {
			return s, receiptErr
		}
		payload := filepath.Join(dir, "payload")
		if err = checkType(payload, true); err != nil {
			if !isForeignSource(err) {
				return s, err
			}
			continue
		}
		m, manifestErr := header(payload)
		if manifestErr != nil && !isForeignSource(manifestErr) {
			return s, manifestErr
		}
		if receiptErr != nil || manifestErr != nil || generation(m.generationTime()) != source {
			// An identifiable kit receipt or kit payload marks a plausible damaged
			// pin. Never adopt an arbitrary directory as a pin or link its files.
			if receiptErr == nil || manifestErr == nil {
				s.damagedPins = append(s.damagedPins, &imageSource{path: payload, probeOnly: true})
			}
			continue
		}
		s.history = append(s.history, &imageSource{path: payload})
	}
	return s, nil
}

// Discover malformed canonical generation directories only after ordinary
// sources fail. Their names permit looking for bytes, never linking or pruning.
func (s *imageSources) salvageRoots(ctx context.Context) ([]*imageSource, error) {
	if s.salvage != nil {
		return s.salvage, nil
	}
	root := filepath.Join(s.live.path, "backups")
	dirs, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	roots := make([]*imageSource, 0)
	for i := len(dirs) - 1; i >= 0; i-- {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		entry := dirs[i]
		if entry.IsDir() && ValidGeneration(entry.Name()) && !s.owned[entry.Name()] {
			roots = append(roots, &imageSource{path: filepath.Join(root, entry.Name()), probeOnly: true})
		}
	}
	s.salvage = append(roots, s.damagedPins...)
	return s.salvage, nil
}

// Probe only the fixed kit image locations; unrelated salvage-directory
// contents can be arbitrarily large. Never traverse a linked folder.
func (s *imageSource) probe(rel string) ([]string, error) {
	var paths []string
	if err := checkType(s.path, true); err != nil {
		if isForeignSource(err) {
			return nil, nil
		}
		return nil, err
	}
	hash := filepath.Base(rel)[:64]
	for _, folder := range []string{"covers", "pages"} {
		dir := filepath.Join(s.path, folder)
		if err := checkType(dir, true); err != nil {
			if isForeignSource(err) {
				continue
			}
			return nil, err
		}
		for _, ext := range []string{".jpg", ".png", ".webp"} {
			path := filepath.Join(dir, hash+ext)
			if err := checkType(path, false); err != nil {
				if isForeignSource(err) {
					continue
				}
				return nil, err
			}
			paths = append(paths, path)
		}
	}
	return paths, nil
}

func (s *imageSource) candidates(ctx context.Context, rel string, wanted map[string]bool) ([]string, error) {
	if s.probeOnly {
		return s.probe(rel)
	}
	if s.files == nil {
		s.files = make(map[string][]string)
		for _, folder := range []string{"covers", "pages"} {
			dir := filepath.Join(s.path, folder)
			if err := checkType(dir, true); os.IsNotExist(err) || errors.Is(err, errUnexpectedType) {
				continue
			} else if err != nil {
				return nil, err
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				return nil, err
			}
			for _, e := range entries {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				name := e.Name()
				if !e.Type().IsRegular() || len(name) < 64 || !wanted[name[:64]] || !covers.ValidName(name) {
					continue
				}
				hash := name[:64]
				s.files[hash] = append(s.files[hash], filepath.Join(dir, name))
			}
		}
	}
	paths := s.files[filepath.Base(rel)[:64]]
	// Prefer the original filename within a generation.
	exact := filepath.Join(s.path, rel)
	for i, path := range paths {
		if path == exact {
			paths[0], paths[i] = paths[i], paths[0]
			break
		}
	}
	return paths, nil
}

// visit attempts each matching regular file until use accepts its bytes. A
// checksum mismatch is recoverable; an actual reader or writer error propagates.
func (s imageSources) visit(ctx context.Context, roots []*imageSource, rel string, use func(string) error) (found, offered bool, err error) {
	for _, root := range roots {
		paths, e := root.candidates(ctx, rel, s.wanted)
		if e != nil {
			return false, offered, e
		}
		for _, path := range paths {
			offered = true
			if use == nil {
				return true, true, nil
			}
			e = use(path)
			switch {
			case e == nil:
				return true, true, nil
			case errors.Is(e, errImageChecksum):
				slog.Warn("damaged image copy; looking for another local copy", "path", path, "image", rel)
			case os.IsNotExist(e), errors.Is(e, errUnexpectedType):
			default:
				return false, offered, e
			}
		}
	}
	return false, offered, nil
}

func (s *imageSources) capture(ctx context.Context, rel, dst string, checks imageChecks, headroom *int64) (bool, error) {
	found, hadLink, err := s.visit(ctx, s.history, rel, func(path string) error { return linkVerifiedImage(path, dst, checks) })
	if err != nil || found {
		return found, err
	}
	var liveBudget int64
	var hadLive bool
	copyFile := func(path string, salvage bool) error {
		st, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() {
			return errUnexpectedType
		}
		// Preflight budgets a hard link, the largest live candidate, or, if no
		// live candidate exists, the largest damaged-history candidate.
		var extra int64
		if hadLink {
			extra = st.Size()
		} else if !salvage {
			hadLive = true
			liveBudget = max(liveBudget, st.Size())
		} else if hadLive {
			extra = max(0, st.Size()-liveBudget)
		}
		if extra > *headroom {
			return fmt.Errorf("insufficient space to recover image %s: need %d additional bytes, have %d bytes of headroom", rel, extra, *headroom)
		}
		if err = copyVerifiedImage(path, dst, checks, salvage); err != nil {
			return err
		}
		*headroom -= extra
		return nil
	}
	found, _, err = s.visit(ctx, []*imageSource{s.live}, rel, func(path string) error { return copyFile(path, false) })
	if err != nil || found {
		return found, err
	}
	roots, err := s.salvageRoots(ctx)
	if err != nil {
		return false, err
	}
	found, _, err = s.visit(ctx, roots, rel, func(path string) error { return copyFile(path, true) })
	return found, err
}

// DeletionGuard independently rechecks an exported normal snapshot. A digest
// recoverable from any owned local source forbids deleting this volume with
// an archive that declares that image missing.
func DeletionGuard(ctx context.Context, data, name string) ([]string, error) {
	if !ValidGeneration(name) {
		return nil, errors.New("invalid preserved generation")
	}
	root := filepath.Join(data, "backups")
	if err := checkType(root, true); err != nil {
		return nil, err
	}
	dir := filepath.Join(root, name)
	m, err := header(dir)
	if err != nil {
		return nil, err
	}
	if m.Raw {
		return nil, errors.New("raw preservation requires independent verification of the full volume before deletion")
	}
	if generation(m.generationTime()) != name {
		return nil, errors.New("not an owned preserved generation")
	}
	if err = VerifyRecovery(ctx, dir); err != nil {
		return nil, err
	}
	if len(m.Missing) == 0 {
		return nil, nil
	}
	prior, err := List(ctx, data)
	if err != nil {
		return nil, err
	}
	sources, err := findSources(ctx, data, prior, m.Missing)
	if err != nil {
		return nil, err
	}
	roots := append([]*imageSource{sources.live}, sources.history...)
	checks := make(imageChecks)
	for _, rel := range m.Missing {
		expected := filepath.Base(rel)[:64]
		check := func(path string) error {
			sum, e := checks.checksum(path)
			if e != nil {
				return e
			}
			if sum != expected {
				return errImageChecksum
			}
			return nil
		}
		found, _, err := sources.visit(ctx, roots, rel, check)
		if err == nil && !found {
			salvage, e := sources.salvageRoots(ctx)
			if e != nil {
				return nil, e
			}
			found, _, err = sources.visit(ctx, salvage, rel, check)
		}
		if err != nil {
			return nil, err
		}
		if found {
			return nil, fmt.Errorf("refusing volume deletion: preserved archive lacks %s, but a verified local copy exists", rel)
		}
	}
	return m.Missing, nil
}
