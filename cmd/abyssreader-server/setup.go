// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/IGlace/abyssreader-server-kit/internal/backup"
	"github.com/IGlace/abyssreader-server-kit/internal/config"
	"github.com/IGlace/abyssreader-server-kit/internal/errorreport"
	"github.com/IGlace/abyssreader-server-kit/internal/setup"
)

// The setup's exit codes besides 0, 1 (refused or failed) and 2 (usage); install.sh reads them.
const (
	exitCancelled   = 3 // the operator said no, or the input ended
	exitInterrupted = 4 // Ctrl-C, a closed SSH session (SIGHUP) or SIGTERM
)

// setupStdin is where the setup reads its answers; tests replace it.
var setupStdin io.Reader = os.Stdin

// setupCommand runs `setup --work DIR --image REF`: install.sh starts it in the kit
// image with the work folder mounted. It asks its questions only on a terminal; with
// DIR/answers.env it asks nothing.
func setupCommand(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	work := fs.String("work", "", "the work folder install.sh mounts")
	image := fs.String("image", "", "the server image compose.yaml pins (an exact tag)")
	if err := fs.Parse(args); err != nil || *work == "" || *image == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: abyssreader-server setup --work DIR --image REF")
		return 2
	}
	o := setup.Options{WorkDir: *work, Image: *image, KitVersion: version, In: stdin, Out: stdout}
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		o.Interactive = true
		o.ReadSecret = func() (string, error) {
			b, err := term.ReadPassword(int(f.Fd()))
			return string(b), err
		}
	}
	if err := setup.Run(ctx, o); err != nil {
		fmt.Fprintln(stderr, err)
		switch {
		case errors.Is(err, setup.ErrCancelled):
			return exitCancelled
		case errors.Is(err, setup.ErrInterrupted):
			return exitInterrupted
		}
		return 1
	}
	return 0
}

// status asks the local server's /api/status, the route the app reads first, and prints its
// name, contract versions and switched-on features. install.sh waits on it.
func status(ctx context.Context, stdout, stderr io.Writer, getenv func(string) string) int {
	// Local preservation and audit details remain available when the stopped service cannot answer HTTP.
	defer func() {
		// Local operator diagnostics stay separate from the public HTTP contract.
		latest, found, err := backup.Latest(config.DataDir(getenv))
		if err != nil {
			fmt.Fprintln(stderr, "Could not read the latest backup status:", err)
		} else if found && latest.Raw {
			fmt.Fprintf(stdout, "Latest backup %s is raw, unverified. It is for manual salvage only.\n", latest.Name)
		} else if found && latest.Incomplete {
			fmt.Fprintf(stdout, "Latest backup %s is incomplete. Unavailable images: %s\n", latest.Name, strings.Join(latest.Missing, ", "))
		}

		note, err := backup.LastAbandonment(config.DataDir(getenv))
		if err != nil {
			fmt.Fprintln(stderr, "Could not read abandonment audit:", err)
		} else if note != "" {
			fmt.Fprintln(stdout, note)
		}
	}()
	base, err := localURL(getenv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/status", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(stderr, "no answer from /api/status:", err)
		return 1
	}
	defer resp.Body.Close()
	var body struct {
		Name              string `json:"name"`
		AccessKeyRequired bool   `json:"accessKeyRequired"`
		SignUpOpen        bool   `json:"signUpOpen"`
		Contracts         []struct {
			Version  string   `json:"version"`
			Features []string `json:"features"`
		} `json:"contracts"`
	}
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(stderr, "/api/status answered", resp.Status)
		return 1
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil || body.Name == "" {
		fmt.Fprintln(stderr, "/api/status did not answer a status")
		return 1
	}
	var contracts []string
	for _, c := range body.Contracts {
		f := "no optional features"
		if len(c.Features) > 0 {
			f = strings.Join(c.Features, ", ")
		}
		contracts = append(contracts, fmt.Sprintf("contract %s (%s)", c.Version, f))
	}
	fmt.Fprintf(stdout, "%s: %s; sign-up %s; access key %s\n", body.Name, strings.Join(contracts, ", "),
		map[bool]string{true: "open", false: "closed"}[body.SignUpOpen],
		map[bool]string{true: "required", false: "not required"}[body.AccessKeyRequired])
	return 0
}

// adminErrorsTest sends one test event through the server's own error reporting, with an email
// and an address in it that the scrubber must remove: the operator sees in GlitchTip or Sentry
// that events arrive, and without personal data.
func adminErrorsTest(stdout, stderr io.Writer, getenv func(string) string) int {
	dsn := strings.TrimSpace(getenv("ABYSS_ERROR_REPORTING_DSN"))
	if dsn == "" {
		fmt.Fprintln(stderr, "ABYSS_ERROR_REPORTING_DSN is not set: error reporting is off.")
		return 1
	}
	if err := config.CheckErrorReportingDSN(dsn); err != nil {
		fmt.Fprintln(stderr, "ABYSS_ERROR_REPORTING_DSN:", err)
		return 1
	}
	h, flush, err := errorreport.Setup(dsn, version, slog.NewTextHandler(stdout, nil))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	slog.New(h).Error("test event from abyssreader-server admin errors test",
		"example", "reader@example.com from 203.0.113.9 (both must read [email] and [ip])")
	flush()
	fmt.Fprintln(stdout, "Sent one test event. In GlitchTip or Sentry it reads [email] and [ip] instead of the example address.")
	return 0
}
