// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The checks of single settings. Load applies them, and so does the setup (`abyssreader-server
// setup`, run by install.sh), so an answer the setup takes is one the server takes. None of them
// puts the checked value in its error: some are secrets.

const (
	// MaxName is the contract's limit for the server's and the operator's names.
	MaxName = 80
	// MinAccessKey is the shortest access key the server takes.
	MinAccessKey = 16
	maxLink      = 2048

	DefaultBackupSchedule = "daily"
	DefaultBackupKeep     = 14
	MaxBackupKeep         = 1000
)

// BackupSchedules are the values of ABYSS_BACKUP_SCHEDULE.
var BackupSchedules = []string{"daily", "weekly", "off"}

var (
	contactPattern = regexp.MustCompile(`^(mailto:[^\s@]+@[^\s@]+|https://[^\s]+)$`)
	httpsURL       = regexp.MustCompile(`^https://[^\s/?#]+[^\s]*$`)
	accessKeyChars = regexp.MustCompile(`^[\x21-\x7e]+$`)
	publicURL      = regexp.MustCompile(`^https://[a-z0-9.-]+(:[0-9]{1,5})?$`)
	localURL       = regexp.MustCompile(`^http://(127\.0\.0\.1|localhost|\[::1\])(:[0-9]{1,5})?$`)
)

// CheckName checks the server's or the operator's name: 1 to 80 characters, no control
// characters (a line break, a tab).
func CheckName(v string) error {
	switch n := utf8.RuneCountInString(v); {
	case n == 0:
		return errors.New("required")
	case n > MaxName:
		return fmt.Errorf("at most %d characters, got %d", MaxName, n)
	case !utf8.ValidString(v) || strings.ContainsFunc(v, unicode.IsControl):
		return errors.New("must not contain line breaks, tabs or other control characters")
	}
	return nil
}

// CheckContact checks the operator's contact: a mailto: address or an https:// link.
func CheckContact(v string) error {
	switch {
	case v == "":
		return errors.New("required (mailto:address or an https:// link)")
	case !contactPattern.MatchString(v) || len(v) > maxLink:
		return errors.New("must be mailto:address or an https:// link")
	}
	return nil
}

// CheckLink checks an optional operator link (privacy, terms, copyright): https:// only.
func CheckLink(v string) error {
	if !httpsURL.MatchString(v) || len(v) > maxLink {
		return errors.New("must be an https:// link")
	}
	return nil
}

// NormalizePublicURL checks the server's public address and returns it lower-cased without a
// trailing slash: https://host or https://host:port with no path; http:// only on a loopback
// host, for local testing.
func NormalizePublicURL(v string) (string, error) {
	u := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(v), "/"))
	switch {
	case u == "":
		return "", errors.New("required: the server's https:// address, for example https://reader.example.org")
	case !publicURL.MatchString(u) && !localURL.MatchString(u), !validPort(u):
		return "", errors.New("must be https://host or https://host:port with no path (http:// only on 127.0.0.1, localhost or [::1], for local testing)")
	}
	return u, nil
}

// CheckAccessKey checks an access key: at least 16 printable ASCII characters, no spaces.
func CheckAccessKey(v string) error {
	if len(v) < MinAccessKey || !accessKeyChars.MatchString(v) {
		return fmt.Errorf("at least %d printable ASCII characters without spaces (value not shown)", MinAccessKey)
	}
	return nil
}

// CheckErrorReportingDSN checks a Sentry-protocol DSN (GlitchTip or Sentry):
// https://KEY@host/PROJECT, https only.
func CheckErrorReportingDSN(v string) error {
	const msg = "must be a Sentry or GlitchTip DSN, https://KEY@host/PROJECT-ID (value not shown)"
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User == nil || u.User.Username() == "" ||
		u.RawQuery != "" || u.Fragment != "" || strings.ContainsFunc(v, unicode.IsSpace) {
		return errors.New(msg)
	}
	path := strings.TrimSuffix(u.Path, "/")
	project := path[strings.LastIndex(path, "/")+1:]
	if _, err := strconv.ParseUint(project, 10, 64); err != nil {
		return errors.New(msg)
	}
	return nil
}

// CheckBackupSchedule checks ABYSS_BACKUP_SCHEDULE: daily, weekly or off.
func CheckBackupSchedule(v string) error {
	if !slices.Contains(BackupSchedules, v) {
		return fmt.Errorf("must be %s", strings.Join(BackupSchedules, ", "))
	}
	return nil
}

// CheckFeatures checks an ABYSS_FEATURES list and returns it sorted, each name once.
func CheckFeatures(v string) ([]string, error) {
	on, problems := features(v)
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "; "))
	}
	return on, nil
}

// validPort reports whether an origin's port, if it has one, is 1–65535.
func validPort(origin string) bool {
	i := strings.LastIndex(origin, ":")
	if i < len("https:") || strings.HasSuffix(origin, "]") {
		return true
	}
	n, err := strconv.Atoi(origin[i+1:])
	return err == nil && n >= 1 && n <= 65535
}
