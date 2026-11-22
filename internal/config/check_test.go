// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package config

import (
	"strings"
	"testing"
)

// Installer settings and the single-setting checks that setup shares with Load.

func TestErrorReportingDSN(t *testing.T) {
	m := minimal()
	m["ABYSS_ERROR_REPORTING_DSN"] = "https://0123abcd@glitchtip.example.org/7"
	c, err := Load(env(m))
	if err != nil || c.ErrorReportingDSN != m["ABYSS_ERROR_REPORTING_DSN"] {
		t.Fatalf("%q %v", c.ErrorReportingDSN, err)
	}
	if c, _ := Load(env(minimal())); c.ErrorReportingDSN != "" {
		t.Fatal("no DSN by default")
	}
	for _, bad := range []string{
		"http://0123abcd@glitchtip.example.org/7", // https only
		"https://glitchtip.example.org/7",         // no key
		"https://0123abcd@glitchtip.example.org/", // no project
		"https://0123abcd@glitchtip.example.org/x",
		"https://0123abcd@/7",
		"https://0123abcd@glitchtip.example.org/7?x=1",
		"https://0123 abcd@glitchtip.example.org/7",
		"secret-value-not-a-url",
	} {
		m["ABYSS_ERROR_REPORTING_DSN"] = bad
		_, err := Load(env(m))
		if err == nil || !strings.Contains(err.Error(), "ABYSS_ERROR_REPORTING_DSN") {
			t.Errorf("%q must be refused: %v", bad, err)
			continue
		}
		if strings.Contains(err.Error(), bad) || strings.Contains(err.Error(), "0123abcd") {
			t.Errorf("the error prints the DSN: %v", err)
		}
	}
}

func TestBackupSettings(t *testing.T) {
	c, err := Load(env(minimal()))
	if err != nil || c.BackupSchedule != "daily" || c.BackupKeep != 14 {
		t.Fatalf("defaults %q %d %v", c.BackupSchedule, c.BackupKeep, err)
	}
	m := minimal()
	m["ABYSS_BACKUP_SCHEDULE"] = "Weekly"
	m["ABYSS_BACKUP_KEEP"] = "12"
	if c, err = Load(env(m)); err != nil || c.BackupSchedule != "weekly" || c.BackupKeep != 12 {
		t.Fatalf("weekly/12: %q %d %v", c.BackupSchedule, c.BackupKeep, err)
	}
	for k, v := range map[string]string{"ABYSS_BACKUP_SCHEDULE": "hourly", "ABYSS_BACKUP_KEEP": "0"} {
		m := minimal()
		m[k] = v
		if _, err := Load(env(m)); err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("%s=%s must be refused: %v", k, v, err)
		}
	}
}

func TestNamesRefuseControlCharacters(t *testing.T) {
	for _, v := range []string{"Lantern\nLibrary", "Lantern\tLibrary", "Lantern\x00", "bad \xff utf-8"} {
		if CheckName(v) == nil {
			t.Errorf("%q must be refused", v)
		}
		m := minimal()
		m["ABYSS_SERVER_NAME"] = v
		if _, err := Load(env(m)); err == nil {
			t.Errorf("Load takes the server name %q", v)
		}
	}
	for _, v := range []string{`Lantern "Library" $HOME \ 'x'`, strings.Repeat("é", MaxName)} {
		if err := CheckName(v); err != nil {
			t.Errorf("%q: %v", v, err)
		}
	}
	if CheckName(strings.Repeat("é", MaxName+1)) == nil {
		t.Error("81 characters must be refused")
	}
}

func TestNormalizePublicURLMatchesLoad(t *testing.T) {
	if u, err := NormalizePublicURL(" https://Reader.Example.org/ "); err != nil || u != "https://reader.example.org" {
		t.Fatalf("%q %v", u, err)
	}
	if _, err := NormalizePublicURL("https://reader.example.org/kit"); err == nil {
		t.Fatal("a path must be refused")
	}
}

func TestCheckFeatures(t *testing.T) {
	if on, err := CheckFeatures(" hosted-chapters, hosted-chapters "); err != nil || len(on) != 1 {
		t.Fatalf("%v %v", on, err)
	}
	if _, err := CheckFeatures("comments"); err == nil || !strings.Contains(err.Error(), "not in this kit version yet") {
		t.Fatalf("comments: %v", err)
	}
	if _, err := CheckFeatures("nope"); err == nil {
		t.Fatal("an unknown feature must be refused")
	}
}
