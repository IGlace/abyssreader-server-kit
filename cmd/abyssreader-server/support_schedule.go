// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/api"
	"github.com/IGlace/abyssreader-server-kit/internal/support"
)

const supportScheduleUsage = `  abyssreader-server support-schedule --stdin          report app support dates from a compat.json on stdin
`

func watchContractSupport(ctx context.Context, log *slog.Logger) {
	s, err := support.Embedded()
	if err != nil {
		log.Warn("Could not read the embedded app support schedule.")
		return
	}
	report := func(msg string) { log.Warn(msg) }
	s.Warn(api.ContractVersion, time.Now(), report)
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		s.Watch(ctx, api.ContractVersion, ticker.C, report)
	}()
}

func supportScheduleCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 || args[0] != "--stdin" {
		fmt.Fprint(stderr, supportScheduleUsage)
		return 2
	}
	s, err := support.Parse(stdin)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	lines := s.Report(api.ContractVersion, time.Now())
	if len(lines) == 0 {
		fmt.Fprintf(stdout, "No app support changes are scheduled for contract %s.\n", api.ContractVersion)
	}
	for _, line := range lines {
		fmt.Fprintln(stdout, line)
	}
	return 0
}
