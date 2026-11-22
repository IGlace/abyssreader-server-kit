// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import "errors"

var (
	ErrTooLarge = errors.New("backup exceeds its byte limit")
	ErrInvalid  = errors.New("invalid backup")
	ErrCount    = errors.New("backup exceeds its record limit")
)

// File contains only the records needed for a restore. Dropped counts marked or
// position chapters beyond the retention limit.
type File struct {
	Entries     []Entry
	SourceNames map[int64]string
	Dropped     int
}

type Entry struct {
	Source                     int64
	URL, Title                 string
	Favorite                   bool
	Modified, FavoriteModified int64 // seconds
	Extra                      *Extra
	Chapters                   []Chapter
	History                    *History
}

const derivedChapterNameIndexField = 5

type Extra struct {
	Website                   string
	Lists                     []string
	Vote                      string
	PositionOnlyChapter       bool
	derivedChapterNameIndexes []int // export only; one-based wire indexes
}

type Chapter struct {
	URL, Name      string
	Read, Bookmark bool
	positionOnly   bool // this decoded chapter only supplies fields for history
	nameIsFallback bool
	Page           int64
	Number         float32
	Modified       int64 // seconds
}

type History struct {
	URL      string
	LastRead int64 // milliseconds
}
