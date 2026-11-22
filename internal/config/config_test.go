// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package config

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func minimal() map[string]string {
	return map[string]string{
		"ABYSS_SERVER_NAME":      "Lantern Library",
		"ABYSS_OPERATOR_NAME":    "Lantern Library team",
		"ABYSS_OPERATOR_CONTACT": "mailto:operator@lantern.example.org",
		"ABYSS_PUBLIC_URL":       "https://lantern.example.org",
	}
}

func TestDefaults(t *testing.T) {
	c, err := Load(env(minimal()))
	if err != nil {
		t.Fatal(err)
	}
	if c.ServerName != "Lantern Library" || c.Operator.Name != "Lantern Library team" || c.Operator.Contact != "mailto:operator@lantern.example.org" {
		t.Fatalf("names: %+v", c)
	}
	if c.Operator.PrivacyURL != nil || c.Operator.TermsURL != nil || c.Operator.CopyrightURL != nil {
		t.Fatal("optional operator links must default to nil (null in /api/status)")
	}
	if !c.SignUpOpen || c.AccessKey != "" || c.DataDir != "/data" || c.Listen != ":8080" {
		t.Fatalf("defaults: %+v", c)
	}
	if c.SessionIdleTimeout != 90*24*time.Hour || c.SignUpsPerHour != 10 || c.SignInsPerMinute != 10 || c.SignInFailuresPerAccount != 10 {
		t.Fatalf("defaults: %+v", c)
	}
	// The port is published on loopback only, so every peer is the Docker bridge gateway or
	// a proxy on the host: loopback and the private ranges are trusted by default (S1).
	for _, a := range []string{"127.0.0.1", "::1", "10.1.2.3", "172.18.0.1", "192.168.1.5", "fd00::1"} {
		if !trusted(c.TrustedProxies, a) {
			t.Errorf("%s is not trusted by default", a)
		}
	}
	for _, a := range []string{"203.0.113.9", "2001:db8::1", "172.32.0.1"} {
		if trusted(c.TrustedProxies, a) {
			t.Errorf("public %s is trusted by default", a)
		}
	}
}

func TestEverySetting(t *testing.T) {
	m := minimal()
	m["ABYSS_OPERATOR_PRIVACY_URL"] = "https://lantern.example.org/privacy"
	m["ABYSS_OPERATOR_TERMS_URL"] = "https://lantern.example.org/terms"
	m["ABYSS_OPERATOR_COPYRIGHT_URL"] = "https://lantern.example.org/copyright"
	m["ABYSS_SIGN_UP_OPEN"] = "false"
	m["ABYSS_ACCESS_KEY"] = "k3y-of-sixteen-chars"
	m["ABYSS_DATA_DIR"] = "/tmp/abyss"
	m["ABYSS_LISTEN"] = "127.0.0.1:9000"
	m["ABYSS_SESSION_IDLE_DAYS"] = "30"
	m["ABYSS_SIGN_UPS_PER_HOUR"] = "0"
	m["ABYSS_SIGN_INS_PER_MINUTE"] = "5"
	m["ABYSS_SIGN_IN_FAILURES_PER_ACCOUNT"] = "4"
	m["ABYSS_TRUSTED_PROXIES"] = "172.16.0.0/12, 127.0.0.1"
	m["ABYSS_LOG_LEVEL"] = "debug"
	c, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if *c.Operator.PrivacyURL != m["ABYSS_OPERATOR_PRIVACY_URL"] || *c.Operator.TermsURL != m["ABYSS_OPERATOR_TERMS_URL"] || *c.Operator.CopyrightURL != m["ABYSS_OPERATOR_COPYRIGHT_URL"] {
		t.Fatal("operator links")
	}
	if c.SignUpOpen || c.AccessKey != "k3y-of-sixteen-chars" || c.DataDir != "/tmp/abyss" || c.Listen != "127.0.0.1:9000" {
		t.Fatalf("%+v", c)
	}
	if c.SessionIdleTimeout != 30*24*time.Hour || c.SignUpsPerHour != 0 || c.SignInsPerMinute != 5 || c.SignInFailuresPerAccount != 4 {
		t.Fatalf("%+v", c)
	}
	if len(c.TrustedProxies) != 2 || !c.TrustedProxies[0].Contains(netip.MustParseAddr("172.18.0.1")) || !c.TrustedProxies[1].Contains(netip.MustParseAddr("127.0.0.1")) {
		t.Fatalf("trusted proxies: %v", c.TrustedProxies)
	}
	if c.LogLevel.String() != "DEBUG" {
		t.Fatalf("log level %v", c.LogLevel)
	}
}

func trusted(ps []netip.Prefix, a string) bool {
	addr := netip.MustParseAddr(a)
	for _, p := range ps {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func TestTrustedProxiesNone(t *testing.T) {
	m := minimal()
	m["ABYSS_TRUSTED_PROXIES"] = "none"
	c, err := Load(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.TrustedProxies) != 0 {
		t.Fatalf("none = %v", c.TrustedProxies)
	}
}

func TestSessionIdleDaysIsCapped(t *testing.T) {
	m := minimal()
	m["ABYSS_SESSION_IDLE_DAYS"] = "3650"
	if c, err := Load(env(m)); err != nil || c.SessionIdleTimeout != 3650*24*time.Hour {
		t.Fatalf("3650 days: %v, %v", c.SessionIdleTimeout, err)
	}
	for _, v := range []string{"3651", "9999999999999"} {
		m["ABYSS_SESSION_IDLE_DAYS"] = v
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), "ABYSS_SESSION_IDLE_DAYS") {
			t.Errorf("%s days: %v; want an error", v, err)
		}
	}
}

func TestErrorsNameEveryBadSetting(t *testing.T) {
	m := map[string]string{
		"ABYSS_SERVER_NAME":          strings.Repeat("x", 81),
		"ABYSS_OPERATOR_CONTACT":     "operator@lantern.example.org",
		"ABYSS_OPERATOR_PRIVACY_URL": "http://lantern.example.org/privacy",
		"ABYSS_SIGN_UP_OPEN":         "maybe",
		"ABYSS_ACCESS_KEY":           "short",
		"ABYSS_SESSION_IDLE_DAYS":    "0",
		"ABYSS_SIGN_UPS_PER_HOUR":    "-1",
		"ABYSS_TRUSTED_PROXIES":      "not-an-ip",
		"ABYSS_LOG_LEVEL":            "loud",
	}
	_, err := Load(env(m))
	if err == nil {
		t.Fatal("want an error")
	}
	for _, name := range []string{"ABYSS_SERVER_NAME", "ABYSS_OPERATOR_NAME", "ABYSS_OPERATOR_CONTACT", "ABYSS_OPERATOR_PRIVACY_URL", "ABYSS_SIGN_UP_OPEN", "ABYSS_ACCESS_KEY", "ABYSS_SESSION_IDLE_DAYS", "ABYSS_SIGN_UPS_PER_HOUR", "ABYSS_TRUSTED_PROXIES", "ABYSS_LOG_LEVEL"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s:\n%v", name, err)
		}
	}
	if strings.Contains(err.Error(), "short") {
		t.Error("the error message must never print the access key")
	}
}

func TestContactAcceptsHTTPS(t *testing.T) {
	m := minimal()
	m["ABYSS_OPERATOR_CONTACT"] = "https://lantern.example.org/contact"
	if _, err := Load(env(m)); err != nil {
		t.Fatal(err)
	}
}

func TestDataDirAloneForAdminCommands(t *testing.T) {
	if got := DataDir(env(nil)); got != "/data" {
		t.Fatalf("DataDir default = %q", got)
	}
	if got := DataDir(env(map[string]string{"ABYSS_DATA_DIR": "/srv/abyss"})); got != "/srv/abyss" {
		t.Fatalf("DataDir = %q", got)
	}
}
