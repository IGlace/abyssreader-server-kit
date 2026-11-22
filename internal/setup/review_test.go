// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package setup

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// Regression checks for the installer setup.

// A $ reference in an answers file would be replaced by Docker Compose (here: by nothing), so
// a key such as abcdefghijklmnop$xyz would silently lose its end. It is refused, by name.
func TestDollarReferencesAreRefused(t *testing.T) {
	for _, line := range []string{"ABYSS_ACCESS_KEY=abcdefghijklmnop$xyz", `ABYSS_ACCESS_KEY="abcdefghijklmnop${xyz}"`, "ABYSS_SERVER_NAME=a ${HOME:-x}"} {
		r := runSetup(t, map[string]string{AnswersFile: ownProxyAnswers + line + "\n"}, Options{})
		if r.err == nil {
			t.Errorf("%s: want an error", line)
			continue
		}
		key, _, _ := strings.Cut(line, "=")
		if !strings.Contains(r.err.Error(), key) || strings.Contains(r.err.Error(), "abcdefghijklmnop") || !strings.Contains(r.err.Error(), "$$") {
			t.Errorf("%s: %v", line, r.err)
		}
		r.noOutput(t)
	}
	// $$ and single quotes are a literal $.
	r := runSetup(t, map[string]string{AnswersFile: ownProxyAnswers + "ABYSS_ACCESS_KEY=abcdefghijklmnop$$xyz\nABYSS_OPERATOR_NAME='The $team'\n"}, Options{})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if env := r.env(t); env[KeyAccessKey] != "abcdefghijklmnop$xyz" || env[KeyOperatorName] != "The $team" {
		t.Fatalf("%q %q", env[KeyAccessKey], env[KeyOperatorName])
	}
}

func TestQuoteWritesDollarAsDoubleDollar(t *testing.T) {
	q, err := Quote("a$b")
	if err != nil || q != `"a$$b"` {
		t.Fatalf("%s %v", q, err)
	}
}

// Ctrl-C (or a closed SSH session) cancels the context: a waiting question returns at once.
func TestInterruptStopsAWaitingQuestion(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *run, 1)
	go func() {
		r := &run{dir: t.TempDir()}
		r.err = Run(ctx, Options{WorkDir: r.dir, Image: testImage, KitVersion: "1.0.0", Interactive: true, In: pr, Out: &r.out,
			Lookup: func(context.Context, string) ([]string, error) { return nil, errors.New("no DNS") }})
		done <- r
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case r := <-done:
		if !errors.Is(r.err, ErrInterrupted) {
			t.Fatalf("want ErrInterrupted, got %v", r.err)
		}
		r.noOutput(t)
		if !strings.Contains(r.out.String(), "Ctrl-C") {
			t.Errorf("the first prompt does not say how to cancel:\n%s", r.out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the setup kept waiting after the interrupt")
	}
}

func TestInterruptStopsTheSecretQuestion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	o := interactive("https://reader.example.org", "2", "", "Lantern", "Team", "mailto:t@example.org", "", "", "", "", "", "", "", "")
	o.ReadSecret = func() (string, error) { cancel(); select {} }
	r := &run{dir: t.TempDir()}
	o.WorkDir, o.Image, o.KitVersion, o.Out = r.dir, testImage, "1.0.0", &r.out
	errc := make(chan error, 1)
	go func() { errc <- Run(ctx, o) }()
	select {
	case err := <-errc:
		if !errors.Is(err, ErrInterrupted) {
			t.Fatalf("%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the DSN question kept waiting after the interrupt")
	}
}

func TestComposeUsesTheShortEnvFileForm(t *testing.T) {
	r := runSetup(t, map[string]string{AnswersFile: ownProxyAnswers}, Options{})
	if r.err != nil {
		t.Fatal(r.err)
	}
	c := r.file(t, "compose.yaml")
	if !strings.Contains(c, "    env_file:\n      - .env\n") || strings.Contains(c, "required:") {
		t.Fatalf("compose.yaml:\n%s", c)
	}
}
