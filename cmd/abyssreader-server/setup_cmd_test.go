// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The installer's two commands: status (install.sh waits on it) and setup.

func TestStatusCommand(t *testing.T) {
	env := baseEnv(t)
	env["ABYSS_ACCESS_KEY"] = "an-access-key-of-24-chars"
	_, stop := serveForTest(t, env)
	defer stop()
	var out, errOut bytes.Buffer
	if code := run(context.Background(), []string{"status"}, &out, &errOut, envOf(env)); code != 0 {
		t.Fatalf("status exited %d: %s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "Lantern Library: contract 1.") || !strings.Contains(got, "access key required") || !strings.Contains(got, "sign-up open") {
		t.Fatalf("status printed %q", got)
	}
	if strings.Contains(got, env["ABYSS_ACCESS_KEY"]) {
		t.Fatal("status printed the access key")
	}
}

func TestStatusFailsWithoutAServer(t *testing.T) {
	var out bytes.Buffer
	if code := run(context.Background(), []string{"status"}, &out, &out, envOf(map[string]string{"ABYSS_LISTEN": freePort(t)})); code != 1 {
		t.Fatalf("status without a server exited %d", code)
	}
}

func TestSetupCommandWithAnAnswersFile(t *testing.T) {
	work := t.TempDir()
	answers := "ABYSS_PUBLIC_URL=https://demo.example.org\nABYSS_INSTALL_HTTPS=own-proxy\nABYSS_SERVER_NAME=Demo\n" +
		"ABYSS_OPERATOR_NAME=Team\nABYSS_OPERATOR_CONTACT=mailto:team@example.org\nABYSS_ACCESS_KEY=generate\n"
	if err := os.WriteFile(filepath.Join(work, "answers.env"), []byte(answers), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	setupStdin = strings.NewReader("")
	code := run(context.Background(), []string{"setup", "--work", work, "--image", "ghcr.io/iglace/abyssreader-server-kit:1.0.0"}, &out, &errOut, envOf(nil))
	if code != 0 {
		t.Fatalf("setup exited %d: %s", code, errOut.String())
	}
	env, err := os.ReadFile(filepath.Join(work, "out", ".env"))
	if err != nil || !strings.Contains(string(env), `ABYSS_SERVER_NAME="Demo"`) {
		t.Fatalf("%v\n%s", err, env)
	}
	if !strings.Contains(out.String(), "Access key:       yes (in .env, not shown)") {
		t.Errorf("summary: %s", out.String())
	}
	// The setup refuses to run without an answers file when stdin is not a terminal.
	if err := os.Remove(filepath.Join(work, "answers.env")); err != nil {
		t.Fatal(err)
	}
	errOut.Reset()
	if code := run(context.Background(), []string{"setup", "--work", work, "--image", "ghcr.io/iglace/abyssreader-server-kit:1.0.0"}, &out, &errOut, envOf(nil)); code != 1 || !strings.Contains(errOut.String(), "no terminal") {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if code := run(context.Background(), []string{"setup", "--work", work}, &out, &errOut, envOf(nil)); code != 2 {
		t.Fatalf("setup without --image exited %d", code)
	}
}

func TestServeRefusesABadDSNWithoutShowingIt(t *testing.T) {
	env := baseEnv(t)
	env["ABYSS_ERROR_REPORTING_DSN"] = "https://secretkey@glitchtip.example.org/notanumber"
	var out bytes.Buffer
	if code := run(context.Background(), []string{"serve"}, &out, &out, envOf(env)); code != 1 {
		t.Fatalf("serve exited %d", code)
	}
	if !strings.Contains(out.String(), "ABYSS_ERROR_REPORTING_DSN") || strings.Contains(out.String(), "secretkey") {
		t.Fatalf("%s", out.String())
	}
}

func TestAdminErrorsTestNeedsADSN(t *testing.T) {
	var out bytes.Buffer
	if code := run(context.Background(), []string{"admin", "errors", "test"}, &out, &out, envOf(nil)); code != 1 || !strings.Contains(out.String(), "not set") {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if code := run(context.Background(), []string{"admin", "errors", "test"}, &out, &out, envOf(map[string]string{"ABYSS_ERROR_REPORTING_DSN": "http://k@h/1"})); code != 1 {
		t.Fatalf("an http DSN must be refused: %d", code)
	}
}

func TestSetupExitsWithItsOwnCodeWhenInterrupted(t *testing.T) {
	work := t.TempDir()
	pr, pw := io.Pipe()
	defer pw.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the signal arrived
	var out, errOut bytes.Buffer
	// Not a terminal, so give it an answers file: the interrupt still wins before anything is written.
	if err := os.WriteFile(filepath.Join(work, "answers.env"), []byte("ABYSS_SERVER_NAME=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setupStdin = pr
	code := run(ctx, []string{"setup", "--work", work, "--image", "ghcr.io/iglace/abyssreader-server-kit:1.0.0"}, &out, &errOut, envOf(nil))
	if code != exitInterrupted {
		t.Fatalf("exit %d, want %d: %s", code, exitInterrupted, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(work, "out")); err == nil {
		t.Fatal("an interrupted setup wrote files")
	}
}
