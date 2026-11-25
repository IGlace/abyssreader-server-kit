// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/netip"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/api"
	"github.com/IGlace/abyssreader-server-kit/internal/config"
	"github.com/IGlace/abyssreader-server-kit/internal/doctor"
	"github.com/IGlace/abyssreader-server-kit/internal/support"
)

const doctorUsage = `  abyssreader-server doctor [--expected-ip IP]         check DNS, TLS, status, images, disk and backups
`

func doctorCommand(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var expected []netip.Addr
	fs.Func("expected-ip", "a record the server name should resolve to (repeat for each A or AAAA address)", func(value string) error {
		addr, err := netip.ParseAddr(value)
		if err != nil || addr.Zone() != "" {
			return fmt.Errorf("expected an IPv4 or IPv6 address")
		}
		expected = append(expected, addr)
		return nil
	})
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(stdout, doctorUsage)
		fs.SetOutput(stdout)
		fs.PrintDefaults()
		return 0
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprint(stderr, doctorUsage)
		return 2
	}
	now := time.Now()
	schedule, err := support.Embedded()
	if err != nil {
		fmt.Fprintln(stdout, "WARN App support: Could not read the embedded schedule.")
	} else {
		schedule.Warn(api.ContractVersion, now, func(msg string) { fmt.Fprintln(stdout, "WARN App support:", msg) })
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		// Avoid echoing arbitrary values from invalid environment variables.
		fmt.Fprintln(stdout, "FAIL Configuration: Check the server settings with the install guide.")
		return 1
	}
	fmt.Fprintln(stdout, "OK Configuration: The kit accepts the configured settings.")
	code := 0
	for _, result := range doctor.Inspect(ctx, cfg, expected, now, doctor.LocalChecks()) {
		level := "OK"
		if result.Failed {
			code = 1
			level = "FAIL"
		}
		fmt.Fprintf(stdout, "%s %s: %s\n", level, result.Name, result.Message)
	}
	return code
}
