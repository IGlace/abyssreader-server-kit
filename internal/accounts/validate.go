// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package accounts

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The kit's limits. Each one is wider than the contract's floor (owner decision 2026-11-19):
// a display name of 3–32 ASCII letters and digits and a password of 8–64 printable ASCII
// characters are always accepted.
const (
	displayNameMin = 3
	displayNameMax = 32
	passwordMin    = 8
	passwordMax    = 128
	emailMax       = 254
	urlMax         = 2048
)

// FieldError is one invalid field, with a code from the contract's open FieldError enum.
type FieldError struct {
	Field string
	Code  string
}

// ValidationError lists every invalid field of a request (400 validation_failed).
type ValidationError struct {
	Fields []FieldError
}

func (v *ValidationError) Error() string {
	parts := make([]string, len(v.Fields))
	for i, f := range v.Fields {
		parts[i] = f.Field + ": " + f.Code
	}
	return "validation failed: " + strings.Join(parts, ", ")
}

func (v *ValidationError) check(field, code string) {
	if code != "" {
		v.Fields = append(v.Fields, FieldError{Field: field, Code: code})
	}
}

func (v *ValidationError) failed() bool { return len(v.Fields) > 0 }

// Letters and digits of any script, and inner spaces, '.', '_' and '-'.
var displayNameChars = regexp.MustCompile(`^[\p{L}\p{N}](?:[\p{L}\p{N} ._-]*[\p{L}\p{N}._-])?$`)

// CheckDisplayName returns a FieldError code for a bad value, or "".
func CheckDisplayName(s string) string {
	switch n := utf8.RuneCountInString(s); {
	case n < displayNameMin:
		return "too_short"
	case n > displayNameMax:
		return "too_long"
	case !displayNameChars.MatchString(s):
		return "invalid"
	}
	return ""
}

// CheckPassword returns a FieldError code for a bad value, or "".
func CheckPassword(s string) string {
	if !utf8.ValidString(s) {
		return "invalid"
	}
	switch n := utf8.RuneCountInString(s); {
	case n < passwordMin:
		return "too_short"
	case n > passwordMax:
		return "too_long"
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "invalid"
		}
	}
	return ""
}

// CheckEmail returns a FieldError code for a bad value, or "". It accepts any syntactically
// valid address with no deliverability check, in the form the contract's `format: email`
// accepts too: a dot-atom local part (no quotes, no backslashes, no comments) and an LDH host
// name (letters, digits and hyphens; no domain literal).
func CheckEmail(s string) string {
	if s == "" {
		return "required"
	}
	if len(s) > emailMax {
		return "too_long"
	}
	at := strings.LastIndexByte(s, '@')
	if at < 1 {
		return "invalid"
	}
	local, domain := s[:at], s[at+1:]
	if len(local) > 64 || !localPart.MatchString(local) {
		return "invalid"
	}
	if len(domain) == 0 || len(domain) > 253 {
		return "invalid"
	}
	for _, l := range strings.Split(domain, ".") {
		if !hostLabel.MatchString(l) {
			return "invalid"
		}
	}
	return ""
}

var (
	localPart = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+/=?^_`{|}~-]+(\\.[A-Za-z0-9!#$%&'*+/=?^_`{|}~-]+)*$")
	hostLabel = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
)

var httpsURL = regexp.MustCompile(`^https://[^\s/?#]+[^\s]*$`)

// CheckHTTPSURL returns a FieldError code for a bad value, or "".
func CheckHTTPSURL(s string) string {
	if len(s) > urlMax {
		return "too_long"
	}
	u, err := url.Parse(s)
	if err != nil || !httpsURL.MatchString(s) || u.Host == "" || u.User != nil {
		return "invalid"
	}
	return ""
}
