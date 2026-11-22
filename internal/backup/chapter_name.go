// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

func exportChapterName(name, url string, number *float64) (label string, derived bool) {
	if strings.ContainsFunc(name, func(r rune) bool { return !chapterNameSpace(r) }) {
		return name, false
	}
	if number != nil && validChapterNameNumber(*number) {
		return "Chapter " + formatChapterNameNumber(*number), true
	}
	return chapterURLName(url), true
}

func validChapterNameNumber(n float64) bool { return n >= 0 && !math.IsInf(n, 0) }

// The official exporter uses Java's shortest decimal rendered in plain
// notation. Double.toString also considers two-digit decimals when a
// one-digit decimal round-trips, which affects the smallest subnormals.
func formatChapterNameNumber(n float64) string {
	if n == 0 {
		return "0"
	}
	short := strconv.FormatFloat(n, 'e', -1, 64)
	if short[1] == 'e' {
		two := strconv.FormatFloat(n, 'e', 1, 64)
		if parsed, err := strconv.ParseFloat(two, 64); err == nil && parsed == n && two[2] != '0' {
			exp, _ := strconv.Atoi(two[4:])
			switch {
			case exp < 0:
				return "0." + strings.Repeat("0", -exp-1) + two[:1] + two[2:3]
			case exp == 0:
				return two[:3]
			default:
				return two[:1] + two[2:3] + strings.Repeat("0", exp-1)
			}
		}
	}
	return strconv.FormatFloat(n, 'f', -1, 64)
}

// Java's end anchor also matches before a final NEL, which Kotlin trim keeps.
var chapterFileExtension = regexp.MustCompile(`\.[A-Za-z0-9]+\x{0085}?$`)

func chapterURLName(raw string) string {
	segment, hasQuery := chapterURLSegment(raw)
	unescaped, err := url.PathUnescape(segment)
	if err != nil {
		return "Chapter"
	}
	decoded := strings.TrimFunc(backupString([]byte(unescaped)), chapterNameSpace)
	if chapterFileExtension.MatchString(decoded) {
		return "Chapter"
	}
	if hasQuery {
		switch strings.ToLower(decoded) {
		case "read", "reader", "view", "viewer":
			return "Chapter"
		}
	}
	// Match the official display cleanup and its 200-code-point limit.
	var label strings.Builder
	count := 0
	space := false
	for _, r := range decoded {
		if chapterNameSpace(r) {
			space = count > 0
			continue
		}
		if !unicode.IsGraphic(r) {
			continue
		}
		if space {
			label.WriteByte(' ')
			count++
			if count == 200 {
				break
			}
			space = false
		}
		label.WriteRune(r)
		count++
		if count == 200 {
			break
		}
	}
	if label.Len() != 0 {
		return label.String()
	}
	return "Chapter"
}

func chapterURLSegment(raw string) (segment string, hasQuery bool) {
	raw = strings.TrimFunc(raw, chapterNameSpace)
	if strings.HasPrefix(raw, "//") || strings.ContainsFunc(raw, chapterNameSpace) {
		return "", false
	}
	path, _, _ := strings.Cut(raw, "#")
	if !strings.HasPrefix(path, "/") {
		scheme, tail, ok := strings.Cut(path, "://")
		if !ok || !(strings.EqualFold(scheme, "http") || strings.EqualFold(scheme, "https")) {
			return "", false
		}
		slash := strings.IndexAny(tail, "/?")
		if slash < 0 || tail[slash] != '/' {
			return "", false
		}
		path = tail[slash:]
	}
	path, _, hasQuery = strings.Cut(path, "?")
	path = strings.TrimRight(path, "/")
	return path[strings.LastIndexByte(path, '/')+1:], hasQuery
}

// Older exports used the raw path spelling. Keep that fallback heuristic for
// chapters without a marker so older backups merge names as they did before.
func legacyChapterURLName(raw string) string {
	segment, _ := chapterURLSegment(raw)
	var label strings.Builder
	count := 0
	for _, r := range segment {
		if unicode.IsPrint(r) {
			label.WriteRune(r)
			count++
			if count == 200 {
				break
			}
		}
	}
	return label.String()
}

// Kotlin Char.isWhitespace combines Java whitespace and separator categories.
func chapterNameSpace(r rune) bool {
	return r != '\u0085' && unicode.IsSpace(r) || r >= '\x1c' && r <= '\x1f'
}

// Mihon and existing AbyssReader backups do not distinguish a fallback
// from a supplied name. Recognize the emitted spellings when merging.
func isBackupNameFallback(c Chapter) bool {
	if c.Name == "" {
		return false
	}
	if c.Name == "Chapter" {
		return true
	}
	s, ok := strings.CutPrefix(c.Name, "Chapter ")
	if !ok {
		return c.Name == legacyChapterURLName(c.URL)
	}
	n, err := strconv.ParseFloat(s, 64)
	// Names are derived from the sync double before the number is narrowed
	// to the BackupChapter float32.
	return err == nil && validChapterNameNumber(n) && float32(n) == c.Number && s == formatChapterNameNumber(n)
}
