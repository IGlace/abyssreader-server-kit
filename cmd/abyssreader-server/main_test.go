// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func freePort(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// serveForTest starts the real server; the returned function stops it.
func serveForTest(t *testing.T, env map[string]string) (string, func()) {
	t.Helper()
	addr := freePort(t)
	env["ABYSS_LISTEN"] = addr
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int)
	var out bytes.Buffer
	go func() { done <- run(ctx, []string{"serve"}, &out, &out, envOf(env)) }()
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("tcp", addr); err == nil {
			c.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return "http://" + addr, func() {
		cancel()
		if code := <-done; code != 0 {
			t.Errorf("serve exited %d:\n%s", code, out.String())
		}
	}
}

func baseEnv(t *testing.T) map[string]string {
	return map[string]string{
		"ABYSS_DATA_DIR": t.TempDir(),
		// Tests invoking catalog admin commands control lock contention themselves.
		"ABYSS_BACKUP_SCHEDULE":  "off",
		"ABYSS_SERVER_NAME":      "Lantern Library",
		"ABYSS_OPERATOR_NAME":    "Lantern Library team",
		"ABYSS_OPERATOR_CONTACT": "mailto:operator@lantern.example.org",
		"ABYSS_PUBLIC_URL":       "https://lantern.example.org",
	}
}

func post(t *testing.T, url string, body map[string]any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func TestServeHealthcheckAndAdminCommands(t *testing.T) {
	env := baseEnv(t)
	base, stop := serveForTest(t, env)
	defer stop()

	var errOut bytes.Buffer
	if code := run(context.Background(), []string{"healthcheck"}, &errOut, &errOut, envOf(env)); code != 0 {
		t.Fatalf("healthcheck = %d: %s", code, errOut.String())
	}
	if status, m := post(t, base+"/api/v1/auth/sign-up", map[string]any{"displayName": "quietreader", "email": "reader@example.com", "password": "correct-horse-7"}); status != 201 {
		t.Fatalf("sign-up = %d %v", status, m)
	}

	// reset-password prints a temporary password that signs in; the old one stops working.
	var out bytes.Buffer
	if code := run(context.Background(), []string{"admin", "users", "reset-password", "reader@example.com"}, &out, &out, envOf(env)); code != 0 {
		t.Fatalf("reset-password = %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "It works for 72 hours, until ") {
		t.Fatalf("the output must say how long the password works: %s", out.String())
	}
	lines := strings.Split(out.String(), "\n")
	if len(lines) < 3 {
		t.Fatalf("output: %s", out.String())
	}
	temp := strings.TrimSpace(lines[2])
	if status, _ := post(t, base+"/api/v1/auth/sign-in", map[string]any{"email": "reader@example.com", "password": "correct-horse-7"}); status != 400 {
		t.Fatalf("old password after reset = %d", status)
	}
	if status, m := post(t, base+"/api/v1/auth/sign-in", map[string]any{"email": "reader@example.com", "password": temp}); status != 200 {
		t.Fatalf("temporary password %q = %d %v", temp, status, m)
	}

	// disable → 403 account_disabled; enable undoes it.
	out.Reset()
	if code := run(context.Background(), []string{"admin", "users", "disable", "reader@example.com"}, &out, &out, envOf(env)); code != 0 {
		t.Fatalf("disable = %d: %s", code, out.String())
	}
	if status, m := post(t, base+"/api/v1/auth/sign-in", map[string]any{"email": "reader@example.com", "password": temp}); status != 403 || m["code"] != "account_disabled" {
		t.Fatalf("sign-in of a disabled account = %d %v", status, m)
	}
	if code := run(context.Background(), []string{"admin", "users", "enable", "reader@example.com"}, &out, &out, envOf(env)); code != 0 {
		t.Fatalf("enable = %d: %s", code, out.String())
	}
	if status, _ := post(t, base+"/api/v1/auth/sign-in", map[string]any{"email": "reader@example.com", "password": temp}); status != 200 {
		t.Fatalf("sign-in after enable = %d", status)
	}

	// An unknown email is exit 1 with a message.
	out.Reset()
	if code := run(context.Background(), []string{"admin", "users", "disable", "nobody@example.com"}, &out, &out, envOf(env)); code != 1 || !strings.Contains(out.String(), "No account") {
		t.Fatalf("unknown email = %d: %s", code, out.String())
	}
}

func TestUsageErrors(t *testing.T) {
	env := envOf(baseEnv(t))
	for _, args := range [][]string{{"nope"}, {"admin"}, {"admin", "users", "delete", "a@example.com"}, {"admin", "users", "disable"}, {"serve", "extra"}} {
		var out bytes.Buffer
		if code := run(context.Background(), args, &out, &out, env); code != 2 || !strings.Contains(out.String(), "Usage:") {
			t.Errorf("%v = %d: %s", args, code, out.String())
		}
	}
}

func TestServeRefusesABadConfiguration(t *testing.T) {
	var out bytes.Buffer
	code := run(context.Background(), []string{"serve"}, &out, &out, envOf(map[string]string{"ABYSS_DATA_DIR": t.TempDir()}))
	if code != 1 || !strings.Contains(out.String(), "ABYSS_SERVER_NAME: required") {
		t.Fatalf("serve without settings = %d: %s", code, out.String())
	}
}

func TestHealthcheckFailsWithoutAServer(t *testing.T) {
	var out bytes.Buffer
	if code := run(context.Background(), []string{"healthcheck"}, &out, &out, envOf(map[string]string{"ABYSS_LISTEN": freePort(t)})); code != 1 {
		t.Fatalf("healthcheck with nothing listening = %d", code)
	}
}

// N6: the admin commands never create a database; a wrong data folder fails clearly.
func TestAdminCommandsNeedAnExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	code := run(context.Background(), []string{"admin", "users", "disable", "reader@example.com"}, &out, &out, envOf(map[string]string{"ABYSS_DATA_DIR": dir}))
	if code != 1 || !strings.Contains(out.String(), "no database at") || !strings.Contains(out.String(), "ABYSS_DATA_DIR") {
		t.Fatalf("admin on an empty folder = %d: %s", code, out.String())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("the admin command created %v", entries)
	}
	code = run(context.Background(), []string{"admin", "users", "disable", "reader@example.com"}, &out, &out, envOf(map[string]string{"ABYSS_DATA_DIR": filepath.Join(dir, "missing")}))
	if code != 1 {
		t.Fatalf("admin on a missing folder = %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Fatal("the admin command created the missing folder")
	}
}
