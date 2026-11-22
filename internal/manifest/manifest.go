// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package manifest reads an import manifest: <folder>/manifest.json, checked against
// schemas/import-manifest.schema.json.
package manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/pb33f/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"github.com/IGlace/abyssreader-server-kit/schemas"
)

// FileName is the manifest's name inside the import folder.
const FileName = "manifest.json"

// Version is the manifestVersion this kit reads.
const Version = 1

// MaxSize caps manifest.json.
const MaxSize = 64 << 20

// Manifest is one import folder's manifest, with the schema's defaults filled in.
type Manifest struct {
	Dir     string   `json:"-"` // the import folder; coverFile paths are relative to it
	Sources []Source `json:"sources"`
	Titles  []Title  `json:"titles"`
}

// Source is a source the titles' source titles point to.
type Source struct {
	Slug         string   `json:"slug"`
	Name         string   `json:"name"`
	Website      string   `json:"website"`
	Lang         string   `json:"lang"`
	IconURL      *string  `json:"iconUrl"`
	TachiyomiIDs []string `json:"tachiyomiIds"`
}

// Title is one catalog title, read through its source titles on other sources, through its
// hosted source titles on this server (Hosted), or both.
type Title struct {
	Ref          string        `json:"ref"`
	Slug         string        `json:"slug"`
	Name         string        `json:"name"`
	AltNames     []string      `json:"altNames"`
	Description  *string       `json:"description"`
	Status       string        `json:"status"`
	Genres       []string      `json:"genres"`
	Authors      []Author      `json:"authors"`
	Rating       *float64      `json:"rating"`
	CoverURL     *string       `json:"coverUrl"`
	CoverFile    string        `json:"coverFile"`
	SourceTitles []SourceTitle `json:"sourceTitles"`
	// Parse guarantees a whole number from 1 to 9999. Accept JSON numbers
	// written with exponents too. Omitting an unknown year preserves old hashes.
	Year *float64 `json:"year,omitempty"`
	// omitempty keeps the Hash of a catalog title the same as before hosted titles existed.
	Hosted []Hosted `json:"hosted,omitempty"`
}

// Author is a person credited on a title.
type Author struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// SourceTitle is a title on one source. Lang is "" when the manifest leaves it to the source's.
type SourceTitle struct {
	Source string `json:"source"`
	URL    string `json:"url"`
	Lang   string `json:"lang"`
}

// Hash identifies the title's entry as the manifest gives it (after defaults): an import
// skips a title whose entry has not changed since the last import.
func (t Title) Hash() string {
	b, _ := json.Marshal(t) // a struct of strings, numbers and slices always marshals
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Load reads <dir>/manifest.json, checks it against the schema and fills in the defaults.
// The error explains every problem the schema found.
func Load(dir string) (*Manifest, error) {
	f, err := os.Open(filepath.Join(dir, FileName))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxSize {
		return nil, fmt.Errorf("%s is larger than %d MiB; split it into several import folders", FileName, MaxSize>>20)
	}
	m, err := Parse(b)
	if err != nil {
		return nil, err
	}
	m.Dir = dir
	return m, nil
}

// Parse checks a manifest's bytes against the schema and decodes them.
func Parse(b []byte) (*Manifest, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", FileName, err)
	}
	// An unknown version gets its own message: the rest of the file may follow rules this kit
	// does not know.
	if obj, ok := doc.(map[string]any); ok {
		if v, ok := obj["manifestVersion"].(json.Number); ok && v.String() != fmt.Sprint(Version) {
			return nil, fmt.Errorf("manifestVersion %s: this kit reads manifestVersion %d; upgrade the kit", v, Version)
		}
	}
	schema, err := schemas.ImportManifest()
	if err != nil {
		return nil, fmt.Errorf("the import manifest schema: %w", err)
	}
	if err := schema.Validate(doc); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			return nil, fmt.Errorf("%s does not match schemas/import-manifest.schema.json:\n%s", FileName, describe(ve))
		}
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	seen := map[string]bool{}
	tachiyomi := map[string]string{} // id → the source that lists it
	for i := range m.Sources {
		s := &m.Sources[i]
		if seen[s.Slug] {
			return nil, fmt.Errorf("%s: source %q is listed twice", FileName, s.Slug)
		}
		if s.Slug == HostedSourceSlug {
			return nil, fmt.Errorf("%s: %s", FileName, reservedSlug)
		}
		seen[s.Slug] = true
		if s.TachiyomiIDs == nil {
			s.TachiyomiIDs = []string{}
		}
		for j, raw := range s.TachiyomiIDs {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("%s: source %q: Tachiyomi id %q must be a positive signed 64-bit integer", FileName, s.Slug, raw)
			}
			id := strconv.FormatInt(n, 10)
			if other, ok := tachiyomi[id]; ok {
				if other == s.Slug {
					return nil, fmt.Errorf("%s: Tachiyomi id %s is listed twice under source %q", FileName, id, s.Slug)
				}
				return nil, fmt.Errorf("%s: Tachiyomi id %s is listed under the sources %q and %q; an id names one source", FileName, id, other, s.Slug)
			}
			tachiyomi[id] = s.Slug
			s.TachiyomiIDs[j] = id
		}
	}
	for i := range m.Titles {
		t := &m.Titles[i]
		if t.Slug == "" {
			t.Slug = t.Ref
		}
		if t.SourceTitles == nil {
			t.SourceTitles = []SourceTitle{}
		}
		for _, st := range t.SourceTitles {
			if st.Source == HostedSourceSlug {
				return nil, fmt.Errorf("%s: title %q: %s", FileName, t.Ref, reservedSlug)
			}
		}
		if err := t.checkHosted(); err != nil {
			return nil, fmt.Errorf("%s: %w", FileName, err)
		}
		if t.Status == "" {
			t.Status = "unknown"
		}
		if t.AltNames == nil {
			t.AltNames = []string{}
		}
		if t.Genres == nil {
			t.Genres = []string{}
		}
		if t.Authors == nil {
			t.Authors = []Author{}
		}
		for j := range t.Authors {
			if t.Authors[j].Role == "" {
				t.Authors[j].Role = "author"
			}
		}
	}
	return &m, nil
}

// describe lists the schema's findings one per line, each with where in the file it is.
func describe(ve *jsonschema.ValidationError) string {
	var buf bytes.Buffer
	p := message.NewPrinter(language.English)
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			where := "/"
			for _, p := range e.InstanceLocation {
				where += p + "/"
			}
			fmt.Fprintf(&buf, "  at %s: %s\n", where[:max(1, len(where)-1)], e.ErrorKind.LocalizedString(p))
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	return buf.String()
}
