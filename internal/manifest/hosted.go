// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// HostedSourceSlug is the slug of the server's own hosted source: the one
// source whose chapters and pages the server stores. A manifest cannot use it for anything else.
const HostedSourceSlug = "hosted"

const reservedSlug = `the source slug "hosted" is the server's own hosted source; give your source another slug`

// Hosted is one hosted source title: one language of a hosted title, with its chapters.
// The lang is its key inside the title.
type Hosted struct {
	Lang        string    `json:"lang"`
	ReadingMode *string   `json:"readingMode"`
	Rights      *Rights   `json:"rights"`
	Chapters    []Chapter `json:"chapters"`
}

// Rights are a hosted title's licence and credit facts; every field may be nil.
type Rights struct {
	Holder      *string `json:"holder"`
	Licence     *string `json:"licence"`
	LicenceURL  *string `json:"licenceUrl"`
	Attribution *string `json:"attribution"`
	Changes     *string `json:"changes"`
}

// Chapter is one hosted chapter. Its Ref is its key inside its hosted source title.
type Chapter struct {
	Ref         string   `json:"ref"`
	Number      *float64 `json:"number"`
	Name        *string  `json:"name"`
	Volume      *float64 `json:"volume"`
	PublishedAt *string  `json:"publishedAt"`
	Credits     *string  `json:"credits"`
	Pages       []Page   `json:"pages"`
}

// Page is one page file of the import folder; Width and Height are 0 when not declared.
type Page struct {
	File   string `json:"file"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// UnmarshalJSON takes a page as a path string or as {file, width, height}.
func (p *Page) UnmarshalJSON(b []byte) error {
	var file string
	if err := json.Unmarshal(b, &file); err == nil {
		*p = Page{File: file}
		return nil
	}
	type plain Page
	return json.Unmarshal(b, (*plain)(p))
}

// Hash identifies the chapter's entry as the manifest gives it (not its files' bytes).
func (c Chapter) Hash() string {
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Published is the chapter's publishedAt, or the zero time when the manifest gives none. The
// schema and Parse have checked its form.
func (c Chapter) Published() time.Time {
	if c.PublishedAt == nil {
		return time.Time{}
	}
	t, _ := parseTime(*c.PublishedAt)
	return t
}

func parseTime(s string) (time.Time, error) {
	if len(s) == len("2006-01-02") {
		return time.Parse("2006-01-02", s)
	}
	return time.Parse(time.RFC3339Nano, s)
}

// checkHosted refuses what the schema cannot see: a lang or a chapter ref listed twice, and a
// publishedAt that is no real date.
func (t *Title) checkHosted() error {
	langs := map[string]bool{}
	for i := range t.Hosted {
		h := &t.Hosted[i]
		if langs[h.Lang] {
			return fmt.Errorf("title %q: hosted lang %q is listed twice; a title has one hosted source title per language", t.Ref, h.Lang)
		}
		langs[h.Lang] = true
		if h.Chapters == nil {
			h.Chapters = []Chapter{}
		}
		refs := map[string]bool{}
		for _, c := range h.Chapters {
			if refs[c.Ref] {
				return fmt.Errorf("title %q (%s): chapter %q is listed twice", t.Ref, h.Lang, c.Ref)
			}
			refs[c.Ref] = true
			if c.PublishedAt != nil {
				if _, err := parseTime(*c.PublishedAt); err != nil {
					return fmt.Errorf("title %q (%s) chapter %q: publishedAt %q is not a date", t.Ref, h.Lang, c.Ref, *c.PublishedAt)
				}
			}
		}
	}
	if t.Hosted == nil {
		t.Hosted = []Hosted{}
	}
	return nil
}
