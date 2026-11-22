// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package setup

import (
	"strings"
	"testing"
)

func TestSetupLeavesUnsetOtherSettingsAtServerDefaults(t *testing.T) {
	first := runSetup(t, map[string]string{AnswersFile: ownProxyAnswers}, Options{})
	if first.err != nil {
		t.Fatal(first.err)
	}
	for _, r := range []*run{first, runSetup(t, map[string]string{
		CurrentFile: first.file(t, ".env"),
		AnswersFile: "ABYSS_SERVER_NAME=Renamed\n",
	}, Options{})} {
		if r.err != nil {
			t.Fatal(r.err)
		}
		env := r.env(t)
		envFile := r.file(t, ".env")
		for _, key := range []string{"ABYSS_SIGN_INS_PER_MINUTE", "ABYSS_LOG_LEVEL", "ABYSS_TRUSTED_PROXIES"} {
			if _, ok := env[key]; ok {
				t.Errorf("setup wrote an active value for unset %s", key)
			}
			if !strings.Contains(envFile, "\n# "+key+"=") {
				t.Errorf("setup did not show a commented default for %s", key)
			}
		}
	}
}

func TestSetupKeepsExplicitOtherSettings(t *testing.T) {
	first := runSetup(t, map[string]string{
		AnswersFile: ownProxyAnswers + "ABYSS_SIGN_INS_PER_MINUTE=10\nABYSS_LOG_LEVEL=debug\n",
	}, Options{})
	if first.err != nil {
		t.Fatal(first.err)
	}
	r := runSetup(t, map[string]string{
		CurrentFile: first.file(t, ".env"),
		AnswersFile: "ABYSS_SERVER_NAME=Renamed\n",
	}, Options{})
	if r.err != nil {
		t.Fatal(r.err)
	}
	env := r.env(t)
	if env["ABYSS_SIGN_INS_PER_MINUTE"] != "10" || env["ABYSS_LOG_LEVEL"] != "debug" {
		t.Error("configure lost explicit overrides, including one that equals the server default")
	}
}
