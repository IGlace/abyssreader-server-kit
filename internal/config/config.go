// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package config reads the server's settings. Environment variables are the only way to
// configure the server kit: the setup writes them into the operator's .env file.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// Operator holds the operator details published in /api/status: who runs the server.
type Operator struct {
	Name         string
	Contact      string  // a mailto: or https:// URI
	PrivacyURL   *string // nil when the operator publishes none
	TermsURL     *string
	CopyrightURL *string
}

// Config is every setting of the server.
type Config struct {
	ServerName string // the server's display name in /api/status
	Operator   Operator
	SignUpOpen bool   // false: the sign-up route answers 403 sign_up_closed
	AccessKey  string // empty: no access key is required

	// PublicURL is the server's public address, an https origin with no path ("http://" only on
	// a loopback host, for local testing). The server builds its absolute image URLs from it.
	PublicURL string
	DataDir   string // the simple setup's one volume: database, images and backups
	Listen    string // host:port the HTTP server listens on

	SessionIdleTimeout time.Duration // a session unused this long stops working
	SignUpsPerHour     int           // per client IP; 0 switches the limit off
	SignInsPerMinute   int           // password checks per client IP; 0 switches the limit off
	// Wrong passwords per account per 15 minutes, from any address, before its password checks
	// wait; 0 switches the limit off.
	SignInFailuresPerAccount int
	TrustedProxies           []netip.Prefix // whose X-Forwarded-For names the client; empty = none
	LogLevel                 slog.Level

	// Features lists the optional features the operator switched on (ABYSS_FEATURES), sorted.
	Features            []string
	AccountBackupMaxMiB int
	// Hosted chapters: the chapter and page-list routes, and the page images, per client
	// per minute; 0 switches a limit off.
	HostedRequestsPerMinute int
	PageImagesPerMinute     int
	// Library and sync: pushes and pulls per account per minute; 0 switches a limit off.
	SyncPushesPerMinute int
	SyncPullsPerMinute  int

	// ErrorReportingDSN is a Sentry-protocol DSN (GlitchTip or Sentry) the server sends its
	// errors to, without emails, IP addresses or request bodies; empty: no error reporting.
	ErrorReportingDSN string
	// The built-in backups' schedule ("daily", "weekly" or "off") and how many are kept.
	BackupSchedule string
	BackupKeep     int
}

const (
	defaultDataDir = "/data"
	maxIdleDays    = 3650
)

const (
	// DefaultHostedRequestsPerMinute covers chapter lists and page lists.
	DefaultHostedRequestsPerMinute = 600
	// DefaultPageImagesPerMinute covers requests for hosted page images.
	DefaultPageImagesPerMinute = 1800
)

// DefaultTrustedProxies is trusted for X-Forwarded-For when ABYSS_TRUSTED_PROXIES is empty:
// loopback and the private ranges. That is safe because the simple setup publishes its port on
// 127.0.0.1 only, so every peer is the Docker bridge gateway, Caddy or a proxy on the host.
// A server whose port is reachable from other machines sets the exact proxies, or "none".
const DefaultTrustedProxies = "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"

// DataDir returns the data folder alone, for the admin commands, which need nothing else.
func DataDir(getenv func(string) string) string {
	if v := strings.TrimSpace(getenv("ABYSS_DATA_DIR")); v != "" {
		return v
	}
	return defaultDataDir
}

// Load reads every setting through getenv (os.Getenv in production). The error lists every
// setting that is wrong, by name, and never prints a secret's value.
func Load(getenv func(string) string) (Config, error) {
	var errs []error
	bad := func(name, format string, args ...any) {
		errs = append(errs, fmt.Errorf("%s: %s", name, fmt.Sprintf(format, args...)))
	}
	get := func(name string) string { return strings.TrimSpace(getenv(name)) }

	c := Config{DataDir: DataDir(getenv)}

	check := func(name string, err error) {
		if err != nil {
			bad(name, "%v", err)
		}
	}
	c.ServerName = get("ABYSS_SERVER_NAME")
	check("ABYSS_SERVER_NAME", CheckName(c.ServerName))
	c.Operator.Name = get("ABYSS_OPERATOR_NAME")
	check("ABYSS_OPERATOR_NAME", CheckName(c.Operator.Name))

	c.Operator.Contact = get("ABYSS_OPERATOR_CONTACT")
	check("ABYSS_OPERATOR_CONTACT", CheckContact(c.Operator.Contact))

	link := func(name string) *string {
		v := get(name)
		if v == "" {
			return nil
		}
		if err := CheckLink(v); err != nil {
			bad(name, "%v", err)
			return nil
		}
		return &v
	}
	c.Operator.PrivacyURL = link("ABYSS_OPERATOR_PRIVACY_URL")
	c.Operator.TermsURL = link("ABYSS_OPERATOR_TERMS_URL")
	c.Operator.CopyrightURL = link("ABYSS_OPERATOR_COPYRIGHT_URL")

	var err error
	c.PublicURL, err = NormalizePublicURL(get("ABYSS_PUBLIC_URL"))
	check("ABYSS_PUBLIC_URL", err)

	c.SignUpOpen = true
	if v := get("ABYSS_SIGN_UP_OPEN"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			bad("ABYSS_SIGN_UP_OPEN", "must be true or false")
		}
		c.SignUpOpen = b
	}

	c.AccessKey = get("ABYSS_ACCESS_KEY")
	if c.AccessKey != "" {
		if err := CheckAccessKey(c.AccessKey); err != nil {
			bad("ABYSS_ACCESS_KEY", "%v", err)
			c.AccessKey = ""
		}
	}

	c.ErrorReportingDSN = get("ABYSS_ERROR_REPORTING_DSN")
	if c.ErrorReportingDSN != "" {
		if err := CheckErrorReportingDSN(c.ErrorReportingDSN); err != nil {
			bad("ABYSS_ERROR_REPORTING_DSN", "%v", err)
			c.ErrorReportingDSN = ""
		}
	}

	c.Listen = ":8080"
	if v := get("ABYSS_LISTEN"); v != "" {
		c.Listen = v
	}

	number := func(name string, def, min, max int) int {
		v := get(name)
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < min || n > max {
			bad(name, "must be a whole number from %d to %d", min, max)
			return def
		}
		return n
	}
	c.SessionIdleTimeout = time.Duration(number("ABYSS_SESSION_IDLE_DAYS", 90, 1, maxIdleDays)) * 24 * time.Hour
	c.SignUpsPerHour = number("ABYSS_SIGN_UPS_PER_HOUR", 10, 0, 1_000_000)
	c.SignInsPerMinute = number("ABYSS_SIGN_INS_PER_MINUTE", 10, 0, 1_000_000)
	c.SignInFailuresPerAccount = number("ABYSS_SIGN_IN_FAILURES_PER_ACCOUNT", 10, 0, 1_000_000)
	c.HostedRequestsPerMinute = number("ABYSS_HOSTED_REQUESTS_PER_MINUTE", DefaultHostedRequestsPerMinute, 0, 1_000_000)
	c.PageImagesPerMinute = number("ABYSS_PAGE_IMAGES_PER_MINUTE", DefaultPageImagesPerMinute, 0, 1_000_000)
	c.SyncPushesPerMinute = number("ABYSS_SYNC_PUSHES_PER_MINUTE", 120, 0, 1_000_000)
	c.AccountBackupMaxMiB = number("ABYSS_ACCOUNT_BACKUP_MAX_MIB", 20, 10, 128)
	c.SyncPullsPerMinute = number("ABYSS_SYNC_PULLS_PER_MINUTE", 300, 0, 1_000_000)

	c.BackupSchedule = DefaultBackupSchedule
	if v := strings.ToLower(get("ABYSS_BACKUP_SCHEDULE")); v != "" {
		check("ABYSS_BACKUP_SCHEDULE", CheckBackupSchedule(v))
		c.BackupSchedule = v
	}
	c.BackupKeep = number("ABYSS_BACKUP_KEEP", DefaultBackupKeep, 1, MaxBackupKeep)

	var problems []string
	c.Features, problems = features(get("ABYSS_FEATURES"))
	for _, p := range problems {
		bad("ABYSS_FEATURES", "%s", p)
	}

	proxies := get("ABYSS_TRUSTED_PROXIES")
	switch strings.ToLower(proxies) {
	case "":
		proxies = DefaultTrustedProxies
	case "none":
		proxies = ""
	}
	for _, part := range strings.Split(proxies, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			c.TrustedProxies = append(c.TrustedProxies, p.Masked())
		} else if a, err := netip.ParseAddr(part); err == nil {
			c.TrustedProxies = append(c.TrustedProxies, netip.PrefixFrom(a, a.BitLen()))
		} else {
			bad("ABYSS_TRUSTED_PROXIES", "%q is not an IP address or CIDR range", part)
		}
	}

	c.LogLevel = slog.LevelInfo
	if v := get("ABYSS_LOG_LEVEL"); v != "" {
		if err := c.LogLevel.UnmarshalText([]byte(v)); err != nil {
			bad("ABYSS_LOG_LEVEL", "must be debug, info, warn or error")
		}
	}

	if len(errs) > 0 {
		return Config{}, fmt.Errorf("configuration:\n%w", errors.Join(errs...))
	}
	return c, nil
}
