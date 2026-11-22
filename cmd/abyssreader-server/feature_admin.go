// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"bufio"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/config"
	"github.com/IGlace/abyssreader-server-kit/internal/features"
	"github.com/IGlace/abyssreader-server-kit/internal/reports"
)

const featureUsage = `  abyssreader-server admin reports [--page N] [--limit N] [--title REF|ID]
                                                       list reports, open first (default 50, max 100)
  abyssreader-server admin reports close ID            close one open report
  abyssreader-server admin reports close-title ID      close every open report for a title
  abyssreader-server admin reports close --account ID  close every open report by an account
  abyssreader-server admin purge FEATURE [--yes]       delete a switched-off feature's data
`

func featureAdmin(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer, getenv func(string) string) int {
	if args[0] == "purge" {
		return adminPurge(ctx, args[1:], in, out, stderr, getenv)
	}
	if len(args) == 1 || strings.HasPrefix(args[1], "-") {
		return listReports(ctx, args[1:], out, stderr, getenv)
	}
	var target reports.CloseTarget
	switch {
	case len(args) == 3 && args[1] == "close":
		target = reports.SingleReport
	case len(args) == 3 && args[1] == "close-title":
		target = reports.TitleReports
	case len(args) == 4 && args[1] == "close" && args[2] == "--account":
		target = reports.AccountReports
	default:
		fmt.Fprint(stderr, featureUsage)
		return 2
	}
	id, err := strconv.ParseInt(args[len(args)-1], 10, 64)
	if err != nil || id < 1 {
		fmt.Fprint(stderr, featureUsage)
		return 2
	}
	return withCatalogLock(ctx, stderr, getenv, func(db *sql.DB) int {
		n, err := reports.Close(ctx, db, target, id)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if n == 0 && target == reports.SingleReport {
			fmt.Fprintf(stderr, "No open report matched ID %d.\n", id)
			return 1
		}
		fmt.Fprintf(out, "Closed reports=%d\n", n)
		return 0
	})
}

func listReports(ctx context.Context, args []string, out, stderr io.Writer, getenv func(string) string) int {
	var q reports.ListOptions
	set := flag.NewFlagSet("admin reports", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.IntVar(&q.Page, "page", 1, "page number, starting at 1")
	set.IntVar(&q.Limit, "limit", 50, "reports per page (1 to 100)")
	set.Func("title", "title ref or ID", func(value string) error {
		if value == "" {
			return fmt.Errorf("missing title ref or ID")
		}
		q.Title = value
		return nil
	})
	if len(args) > 6 || set.Parse(args) != nil || set.NArg() != 0 {
		fmt.Fprint(stderr, featureUsage)
		return 2
	}
	if err := q.Validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	return withDatabase(ctx, stderr, getenv, func(db *sql.DB) int {
		page, err := reports.List(ctx, db, q)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(out, "ID  State  Title ID and name  Reason  Details  Reporter ID and email  Date (UTC)")
		shownOpen := 0
		for _, r := range page.Items {
			state := "closed"
			if !r.Closed {
				state = "open"
				shownOpen++
			}
			fmt.Fprintf(out, "%d  %s  %d %q  %s  %q  %d %q  %s\n", r.ID, state, r.TitleID, terminalText(r.Title), terminalText(r.Reason), terminalText(r.Details), r.UserID, terminalText(r.Email), time.UnixMilli(r.CreatedAt).UTC().Format(time.RFC3339))
		}
		fmt.Fprintf(out, "%d of %d open reports on this page.\n", shownOpen, page.Open)
		if int64(q.Page)*int64(q.Limit) < page.Total {
			fmt.Fprintf(out, "Next page: abyssreader-server admin reports --page %d --limit %d", q.Page+1, q.Limit)
			if q.Title != "" {
				fmt.Fprintf(out, " --title %s", q.Title)
			}
			fmt.Fprintln(out)
		}
		return 0
	})
}

func adminPurge(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer, getenv func(string) string) int {
	if len(args) < 1 || len(args) > 2 || (len(args) == 2 && args[1] != "--yes") {
		fmt.Fprint(stderr, featureUsage)
		return 2
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	for _, f := range features.Built {
		if args[0] != f.Name {
			continue
		}
		fmt.Fprintf(out, "Purge checks this command's environment. Before purging, restart the server with %s switched off.\n", f.Name)
		if cfg.FeatureOn(f.Name) {
			fmt.Fprintf(stderr, "Refusing to purge enabled feature %s.\n", f.Name)
			return 1
		}
		if len(args) == 1 {
			fmt.Fprintf(out, "Type %s to permanently delete its data: ", f.Name)
			scanner := bufio.NewScanner(io.LimitReader(in, 256))
			if !scanner.Scan() || scanner.Text() != f.Name {
				fmt.Fprintln(stderr, "Confirmation did not match. Data kept.")
				return 1
			}
		}
		return withCatalogLock(ctx, stderr, getenv, func(db *sql.DB) int {
			counts, err := f.Purge(ctx, db, cfg.DataDir, out)
			if counts != "" {
				fmt.Fprintln(out, counts)
			}
			if err != nil {
				fmt.Fprintln(stderr, "Purge stopped. Correct the error and run it again:", err)
				return 1
			}
			return 0
		})
	}
	fmt.Fprintln(stderr, "Unknown feature. Available:", strings.Join(config.Built, ", "))
	return 2
}
