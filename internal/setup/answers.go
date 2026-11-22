// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package setup

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/IGlace/abyssreader-server-kit/internal/config"
)

// The HTTPS modes.
const (
	// Caddy: a Caddy container in the stack gets a Let's Encrypt certificate and answers on
	// ports 80 and 443; the server's own port is not published.
	Caddy = "caddy"
	// OwnProxy: the server listens on 127.0.0.1:<port> only, behind the operator's nginx,
	// Cloudflare Tunnel or other proxy (the demo server's mode).
	OwnProxy = "own-proxy"
)

// The settings the setup asks for, by their .env names, in prompt order.
// ABYSS_INSTALL_* are install.sh's own: the server ignores them.
const (
	KeyPublicURL    = "ABYSS_PUBLIC_URL"
	KeyHTTPS        = "ABYSS_INSTALL_HTTPS"
	KeyPort         = "ABYSS_INSTALL_PORT"
	KeyServerName   = "ABYSS_SERVER_NAME"
	KeyOperatorName = "ABYSS_OPERATOR_NAME"
	KeyContact      = "ABYSS_OPERATOR_CONTACT"
	KeyPrivacy      = "ABYSS_OPERATOR_PRIVACY_URL"
	KeyTerms        = "ABYSS_OPERATOR_TERMS_URL"
	KeyCopyright    = "ABYSS_OPERATOR_COPYRIGHT_URL"
	KeySignUp       = "ABYSS_SIGN_UP_OPEN"
	KeyAccessKey    = "ABYSS_ACCESS_KEY"
	KeyFeatures     = "ABYSS_FEATURES"
	KeyDSN          = "ABYSS_ERROR_REPORTING_DSN"
	KeyBackup       = "ABYSS_BACKUP_SCHEDULE"
	KeyBackupKeep   = "ABYSS_BACKUP_KEEP"

	// KeyDNSCheck exists only in an answers file: "skip" installs in Caddy mode even when the
	// address does not resolve to this machine (yet).
	KeyDNSCheck = "ABYSS_INSTALL_DNS_CHECK"
)

// asked lists the asked settings in the order they are written to .env.
var asked = []string{KeyPublicURL, KeyHTTPS, KeyPort, KeyServerName, KeyOperatorName, KeyContact, KeyPrivacy,
	KeyTerms, KeyCopyright, KeySignUp, KeyAccessKey, KeyFeatures, KeyDSN, KeyBackup, KeyBackupKeep}

// secretKeys are never printed, shown as a default or put in an error.
var secretKeys = []string{KeyAccessKey, KeyDSN}

// others lists the server settings that setup does not ask about, with
// their defaults for the commented examples in .env. The server applies its own defaults.
var others = []struct{ Key, Default string }{
	{"ABYSS_SESSION_IDLE_DAYS", "90"},
	{"ABYSS_SIGN_UPS_PER_HOUR", "10"},
	{"ABYSS_SIGN_INS_PER_MINUTE", "10"},
	{"ABYSS_SIGN_IN_FAILURES_PER_ACCOUNT", "10"},
	{"ABYSS_TRUSTED_PROXIES", ""},
	{"ABYSS_HOSTED_REQUESTS_PER_MINUTE", strconv.Itoa(config.DefaultHostedRequestsPerMinute)},
	{"ABYSS_PAGE_IMAGES_PER_MINUTE", strconv.Itoa(config.DefaultPageImagesPerMinute)},
	{"ABYSS_ACCOUNT_BACKUP_MAX_MIB", "20"},
	{"ABYSS_SYNC_PUSHES_PER_MINUTE", "120"},
	{"ABYSS_SYNC_PULLS_PER_MINUTE", "300"},
	{"ABYSS_LOG_LEVEL", "info"},
}

// fixed are settings the installed stack depends on (the data volume at /data, the port 8080
// inside the container): an answers file may not set them.
var fixed = []string{"ABYSS_DATA_DIR", "ABYSS_LISTEN"}

const (
	defaultPort = 8080
	// accessKeyBytes of randomness make a 43-character base64url key.
	accessKeyBytes = 32
)

// Answers are the setup's answers.
type Answers struct {
	PublicURL       string
	HTTPS           string // Caddy or OwnProxy
	Port            int    // OwnProxy: the port on 127.0.0.1
	ServerName      string
	OperatorName    string
	OperatorContact string
	PrivacyURL      string // optional, "" for none
	TermsURL        string
	CopyrightURL    string
	SignUpOpen      bool
	AccessKey       string   // "" for no access key
	Features        []string // sorted
	DSN             string   // "" for no error reporting
	BackupSchedule  string
	BackupKeep      int
}

// defaults are a fresh install's answers before any question.
func defaults() Answers {
	return Answers{HTTPS: Caddy, Port: defaultPort, SignUpOpen: true,
		BackupSchedule: config.DefaultBackupSchedule, BackupKeep: config.DefaultBackupKeep}
}

// fromEnv reads the current answers from an existing .env. Values that do not pass their
// check are kept as they are: the questions then show them, and the final check names them.
func fromEnv(env map[string]string) Answers {
	a := defaults()
	get := func(k string) string { return strings.TrimSpace(env[k]) }
	a.PublicURL = get(KeyPublicURL)
	if v := get(KeyHTTPS); v != "" {
		a.HTTPS = v
	}
	if n, err := strconv.Atoi(get(KeyPort)); err == nil {
		a.Port = n
	}
	a.ServerName = get(KeyServerName)
	a.OperatorName = get(KeyOperatorName)
	a.OperatorContact = get(KeyContact)
	a.PrivacyURL = get(KeyPrivacy)
	a.TermsURL = get(KeyTerms)
	a.CopyrightURL = get(KeyCopyright)
	if b, err := strconv.ParseBool(get(KeySignUp)); err == nil {
		a.SignUpOpen = b
	}
	a.AccessKey = get(KeyAccessKey)
	if on, err := config.CheckFeatures(get(KeyFeatures)); err == nil {
		a.Features = on
	} else {
		a.Features = splitList(get(KeyFeatures))
	}
	a.DSN = get(KeyDSN)
	if v := strings.ToLower(get(KeyBackup)); v != "" {
		a.BackupSchedule = v
	}
	if n, err := strconv.Atoi(get(KeyBackupKeep)); err == nil {
		a.BackupKeep = n
	}
	return a
}

func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// env returns the answers as .env settings.
func (a Answers) env() map[string]string {
	return map[string]string{
		KeyPublicURL: a.PublicURL, KeyHTTPS: a.HTTPS, KeyPort: strconv.Itoa(a.Port),
		KeyServerName: a.ServerName, KeyOperatorName: a.OperatorName, KeyContact: a.OperatorContact,
		KeyPrivacy: a.PrivacyURL, KeyTerms: a.TermsURL, KeyCopyright: a.CopyrightURL,
		KeySignUp: strconv.FormatBool(a.SignUpOpen), KeyAccessKey: a.AccessKey,
		KeyFeatures: strings.Join(a.Features, ","), KeyDSN: a.DSN,
		KeyBackup: a.BackupSchedule, KeyBackupKeep: strconv.Itoa(a.BackupKeep),
	}
}

// Host is the public address's host name (no port, no brackets).
func (a Answers) Host() string {
	u, err := url.Parse(a.PublicURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// checkPublicURL checks the public address for an HTTPS mode, with the server's rules
// (config.NormalizePublicURL) plus Caddy's: https, a domain name, no port.
func checkPublicURL(v, mode string) (string, error) {
	u, err := config.NormalizePublicURL(v)
	if err != nil {
		return "", err
	}
	if mode == Caddy {
		pu, _ := url.Parse(u)
		host := pu.Hostname()
		switch {
		case pu.Scheme != "https":
			return "", errors.New("with Caddy the address must be https://")
		case pu.Port() != "":
			return "", errors.New("with Caddy the address has no port (Caddy answers on 443)")
		case net.ParseIP(host) != nil || host == "localhost" || !strings.Contains(host, "."):
			return "", errors.New("with Caddy the address must be a domain name pointing at this machine, not an IP address")
		}
	}
	return u, nil
}

func checkHTTPS(v string) error {
	if v != Caddy && v != OwnProxy {
		return fmt.Errorf("must be %s or %s", Caddy, OwnProxy)
	}
	return nil
}

func checkPort(n int) error {
	if n < 1 || n > 65535 {
		return errors.New("must be a port number from 1 to 65535")
	}
	return nil
}

// problems checks every answer and returns one problem per setting, by name, in the order of
// the questions. It never puts a value in a problem.
func (a *Answers) problems() []string {
	var out []string
	bad := func(key string, err error) {
		if err != nil {
			out = append(out, fmt.Sprintf("%s: %v", key, err))
		}
	}
	if err := checkHTTPS(a.HTTPS); err != nil {
		bad(KeyHTTPS, err)
	} else {
		u, err := checkPublicURL(a.PublicURL, a.HTTPS)
		bad(KeyPublicURL, err)
		if err == nil {
			a.PublicURL = u
		}
	}
	if a.HTTPS == OwnProxy {
		bad(KeyPort, checkPort(a.Port))
	}
	bad(KeyServerName, config.CheckName(a.ServerName))
	bad(KeyOperatorName, config.CheckName(a.OperatorName))
	bad(KeyContact, config.CheckContact(a.OperatorContact))
	for k, v := range map[string]string{KeyPrivacy: a.PrivacyURL, KeyTerms: a.TermsURL, KeyCopyright: a.CopyrightURL} {
		if v != "" {
			bad(k, config.CheckLink(v))
		}
	}
	if a.AccessKey != "" {
		bad(KeyAccessKey, config.CheckAccessKey(a.AccessKey))
	}
	if on, err := config.CheckFeatures(strings.Join(a.Features, ",")); err != nil {
		bad(KeyFeatures, err)
	} else {
		a.Features = on
	}
	if a.DSN != "" {
		bad(KeyDSN, config.CheckErrorReportingDSN(a.DSN))
	}
	bad(KeyBackup, config.CheckBackupSchedule(a.BackupSchedule))
	if a.BackupKeep < 1 || a.BackupKeep > config.MaxBackupKeep {
		bad(KeyBackupKeep, fmt.Errorf("must be a whole number from 1 to %d", config.MaxBackupKeep))
	}
	sort.SliceStable(out, func(i, j int) bool { return keyOrder(out[i]) < keyOrder(out[j]) })
	return out
}

func keyOrder(problem string) int {
	key, _, _ := strings.Cut(problem, ":")
	if i := slices.Index(asked, key); i >= 0 {
		return i
	}
	return len(asked)
}

// newAccessKey makes a random access key: 32 bytes from the system's random source as
// base64url (43 characters, letters, digits, - and _), safe in a header and a .env file.
func newAccessKey() (string, error) {
	b := make([]byte, accessKeyBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
