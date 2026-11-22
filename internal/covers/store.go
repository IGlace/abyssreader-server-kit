// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package covers

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Dir is the covers folder inside the data folder: <data>/covers/<sha256>.<ext>. The simple
// setup keeps it in the /data volume beside the database, so a backup of /data has both.
const Dir = "covers"

// Store keeps covers as files named by the SHA-256 of their bytes: the same image is stored
// once, and a name always means the same bytes, so its URL can be cached forever.
type Store struct{ dir string }

// OpenStore opens (creating it if needed) the covers folder of a data folder.
func OpenStore(dataDir string) (*Store, error) {
	return OpenDir(filepath.Join(dataDir, Dir))
}

// OpenDir opens (creating it if needed) a folder of images named by their SHA-256, with the
// same rules as the covers folder; the page images of hosted chapters have their own.
func OpenDir(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("image folder: %w", err)
	}
	return &Store{dir: dir}, nil
}

var namePattern = regexp.MustCompile(`^[0-9a-f]{64}\.(jpg|png|webp)$`)

// ValidName reports whether name can be a stored cover's name; nothing else is ever opened.
func ValidName(name string) bool { return namePattern.MatchString(name) }

// NameOf is the stored name of an image.
func NameOf(img Image) string {
	sum := sha256.Sum256(img.Bytes)
	return hex.EncodeToString(sum[:]) + img.Format.Ext
}

// Put stores img and returns its name. Storing an image that is already there does nothing.
func (s *Store) Put(img Image) (string, error) {
	name := NameOf(img)
	path := filepath.Join(s.dir, name)
	if _, err := os.Stat(path); err == nil {
		return name, nil
	}
	tmp, err := os.CreateTemp(s.dir, ".incoming-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // a no-op once renamed
	if _, err := tmp.Write(img.Bytes); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return name, nil
}

// Names lists the stored images (files with a stored image's name), for finding the ones no
// record names. Anything else in the folder is not the store's and is left alone.
func (s *Store) Names() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.Type().IsRegular() && ValidName(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// Prune deletes stored images not named by a record and incomplete temporary files.
// The caller holds the admin lock so concurrent writes cannot produce transient orphans.
func (s *Store) Prune(referenced []string) error {
	keep := make(map[string]bool, len(referenced))
	for _, name := range referenced {
		keep[name] = true
	}
	stored, err := s.Names()
	if err != nil {
		return err
	}
	for _, name := range stored {
		if !keep[name] {
			if err := s.Remove(name); err != nil {
				return err
			}
		}
	}
	return s.removeIncoming()
}

// removeIncoming deletes the half-written files a stopped Put left behind. Only one writer may
// run at a time (the admin commands hold a lock), so none of them is still being written.
func (s *Store) removeIncoming() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasPrefix(e.Name(), ".incoming-") {
			if err := os.Remove(filepath.Join(s.dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

// Has reports whether the image name is stored.
func (s *Store) Has(name string) bool {
	if !ValidName(name) {
		return false
	}
	st, err := os.Stat(filepath.Join(s.dir, name))
	return err == nil && st.Mode().IsRegular()
}

// Remove deletes a stored cover; a cover that is not there is no error.
func (s *Store) Remove(name string) error {
	if !ValidName(name) {
		return fmt.Errorf("not a cover name: %q", name)
	}
	err := os.Remove(filepath.Join(s.dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Open opens a stored cover for serving, with its format.
func (s *Store) Open(name string) (*os.File, Format, error) {
	if !ValidName(name) {
		return nil, Format{}, fs.ErrNotExist
	}
	f, ok := FormatOfExt(filepath.Ext(name))
	if !ok {
		return nil, Format{}, fs.ErrNotExist
	}
	file, err := os.Open(filepath.Join(s.dir, name))
	return file, f, err
}
