// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Command abyssreader-server is the AbyssReader server kit's server and its admin commands.
// Everything is configured through environment variables; see .env.example.
//
//	abyssreader-server [serve]                          run the server
//	abyssreader-server admin users reset-password EMAIL  print a new temporary password
//	abyssreader-server admin users disable EMAIL         switch an account off
//	abyssreader-server admin users enable EMAIL          switch it on again
//	abyssreader-server admin import [--remove-chapters] FOLDER
//	                                                    import FOLDER/manifest.json into the catalog
//	abyssreader-server admin titles remove REF           remove one title
//	abyssreader-server admin covers retry                download the covers that failed again
//	abyssreader-server healthcheck                       exit 0 when /healthz answers (container check)
//	abyssreader-server status                            print the local /api/status (install.sh waits on it)
//	abyssreader-server setup --work DIR --image REF      the installer's setup (run by install.sh)
//	abyssreader-server version
//
// There are no admin accounts: whoever has shell access to the host administers the server,
// through `docker compose exec server abyssreader-server admin …`.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/accounts"
	"github.com/IGlace/abyssreader-server-kit/internal/api"
	"github.com/IGlace/abyssreader-server-kit/internal/backup"
	"github.com/IGlace/abyssreader-server-kit/internal/catalog"
	"github.com/IGlace/abyssreader-server-kit/internal/config"
	"github.com/IGlace/abyssreader-server-kit/internal/covers"
	"github.com/IGlace/abyssreader-server-kit/internal/errorreport"
	"github.com/IGlace/abyssreader-server-kit/internal/manifest"
	"github.com/IGlace/abyssreader-server-kit/internal/pages"
	"github.com/IGlace/abyssreader-server-kit/internal/store"
	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

// version is set at build time (-ldflags "-X main.version=x.y.z").
var version = "dev"

// databaseFile is the simple setup's SQLite database inside the data folder.
const databaseFile = backup.Database

// lockFile, in the data folder, is held by every admin command that changes the catalog, so
// two imports (or an import and a removal) never run at once. The lock goes with the process.
const lockFile = "admin.lock"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

const usage = `Usage:
  abyssreader-server [serve]                           run the server
` + featureUsage + doctorUsage + supportScheduleUsage + definitionUsage + `  abyssreader-server admin users reset-password EMAIL  print a new temporary password
  abyssreader-server admin users disable EMAIL         switch an account off (403 account_disabled)
  abyssreader-server admin users enable EMAIL          switch a disabled account on again
  abyssreader-server admin import [--remove-chapters] FOLDER
                                                       import FOLDER/manifest.json into the catalog
                                                       (schemas/import-manifest.schema.json);
                                                       --remove-chapters lets it remove the hosted
                                                       chapters the manifest no longer lists
  abyssreader-server admin titles remove REF           remove the title with this ref
  abyssreader-server admin covers retry                download every cover URL that failed again
  abyssreader-server admin backup list                  show generations, completeness and unavailable images
  abyssreader-server admin backup now                   snapshot the database and images in /data/backups
  abyssreader-server admin backup export GENERATION     write a portable backup archive to stdout
  abyssreader-server admin errors test                 send one test event to ABYSS_ERROR_REPORTING_DSN
  abyssreader-server healthcheck                       exit 0 when the server answers /healthz
  abyssreader-server status                            exit 0 when the server answers /api/status, and
                                                       print its name and features
  abyssreader-server setup --work DIR --image REF      the questions of install.sh (docs/install.md):
                                                       reads DIR/current.env, DIR/answers.env, writes DIR/out/
  abyssreader-server version
Settings come from environment variables (ABYSS_…); see .env.example.
`

// run is the whole program; it returns the exit code (2 for a usage error).
func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 {
		args = []string{"serve"}
	}
	switch args[0] {
	case "serve":
		if len(args) != 1 {
			break
		}
		if err := serve(ctx, stdout, getenv); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "admin":
		if err := backup.Ready(config.DataDir(getenv)); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return admin(ctx, args[1:], stdout, stderr, getenv)
	case "maintenance":
		return maintenance(ctx, args[1:], os.Stdin, stdout, stderr, getenv)
	case "healthcheck":
		return healthcheck(ctx, stderr, getenv)
	case "definitions":
		return definitionCommand(ctx, args[1:], stdout, stderr, getenv)
	case "doctor":
		return doctorCommand(ctx, args[1:], stdout, stderr, getenv)
	case "support-schedule":
		return supportScheduleCommand(args[1:], os.Stdin, stdout, stderr)
	case "status":
		return status(ctx, stdout, stderr, getenv)
	case "setup":
		return setupCommand(ctx, args[1:], setupStdin, stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, "abyssreader-server", version, "(contract "+api.ContractVersion+")")
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprint(stderr, usage)
	return 2
}

func openDatabase(ctx context.Context, getenv func(string) string) (*sql.DB, error) {
	dir := config.DataDir(getenv)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("data folder: %w", err)
	}
	if err := backup.CheckBoot(dir, getenv("ABYSS_RECOVERY_TOKEN")); err != nil {
		return nil, err
	}
	return store.Open(ctx, filepath.Join(dir, databaseFile))
}

func serve(ctx context.Context, stdout io.Writer, getenv func(string) string) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	logHandler, flushErrors, err := errorreport.Setup(cfg.ErrorReportingDSN, version,
		slog.NewTextHandler(stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	if err != nil {
		return err
	}
	defer flushErrors()
	log := slog.New(logHandler)
	db, err := openDatabase(ctx, getenv)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := ensureDefinitionKey(cfg.DataDir, db); err != nil {
		return err
	}
	svc := accounts.New(db, cfg.SessionIdleTimeout)
	coverStore, err := covers.OpenStore(cfg.DataDir)
	if err != nil {
		return err
	}
	pageStore, err := pages.OpenStore(cfg.DataDir)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(cfg.PublicURL, "https://") {
		log.Warn("ABYSS_PUBLIC_URL is http:// on a loopback host: for local testing only; the app accepts only https:// servers and images", "publicUrl", cfg.PublicURL)
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           api.New(api.Options{Config: cfg, DB: db, Accounts: svc, Covers: coverStore, Pages: pageStore, Logger: log}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	log.Info("AbyssReader server started", "version", version, "contract", api.ContractVersion, "listen", ln.Addr().String(),
		"name", cfg.ServerName, "publicUrl", cfg.PublicURL, "signUpOpen", cfg.SignUpOpen, "accessKeyRequired", cfg.AccessKey != "", "features", strings.Join(cfg.Features, ","), "data", config.DataDir(getenv),
		"errorReporting", cfg.ErrorReportingDSN != "")

	watchContractSupport(ctx, log)
	stopBackups := scheduleBackups(ctx, cfg, db, log)
	defer stopBackups()
	go purgeIdleSessions(ctx, svc, log)
	go purgeTombstones(ctx, usersync.New(db), log)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("stopping")
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	log.Info("stopped")
	return nil
}

// purgeIdleSessions deletes expired sessions at start-up and then every hour.
func purgeIdleSessions(ctx context.Context, svc *accounts.Service, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := svc.PurgeIdleSessions(ctx); err != nil && ctx.Err() == nil {
			log.Error("purging idle sessions", "err", err)
		} else if n > 0 {
			log.Info("purged idle sessions", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// purgeTombstones removes expired sync removals and operation receipts at startup
// and every hour. A cursor older than a purged removal requires a full pull.
func purgeTombstones(ctx context.Context, svc *usersync.Service, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := svc.PurgeTombstones(ctx, time.Now().Add(-usersync.TombstoneLifetime)); err != nil && ctx.Err() == nil {
			log.Error("pruning sync history", "err", err)
		} else if n > 0 {
			log.Info("purged sync tombstones", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func admin(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	switch {
	case len(args) > 0 && args[0] == "backup":
		return adminBackup(ctx, args[1:], stdout, stderr, getenv)
	case len(args) > 0 && (args[0] == "reports" || args[0] == "purge" ||
		args[0] == "remove" || args[0] == "suspend" || args[0] == "unsuspend"):
		return featureAdmin(ctx, args, os.Stdin, stdout, stderr, getenv)
	case len(args) == 3 && args[0] == "users":
		switch args[1] {
		case "reset-password", "disable", "enable":
			return withDatabase(ctx, stderr, getenv, func(db *sql.DB) int {
				return adminUsers(ctx, db, args[1], args[2], stdout, stderr)
			})
		}
	case len(args) == 2 && args[0] == "import" && !strings.HasPrefix(args[1], "-"),
		len(args) == 3 && args[0] == "import" && args[1] == "--remove-chapters":
		return withCatalogLock(ctx, stderr, getenv, func(db *sql.DB) int {
			return adminImport(ctx, db, config.DataDir(getenv), args[len(args)-1], len(args) == 3, stdout, stderr)
		})
	case len(args) == 3 && args[0] == "titles" && args[1] == "remove":
		return withCatalogLock(ctx, stderr, getenv, func(db *sql.DB) int {
			return adminRemoveTitle(ctx, db, config.DataDir(getenv), args[2], stdout, stderr)
		})
	case len(args) == 2 && args[0] == "errors" && args[1] == "test":
		return adminErrorsTest(stdout, stderr, getenv)
	case len(args) == 2 && args[0] == "covers" && args[1] == "retry":
		return withCatalogLock(ctx, stderr, getenv, func(db *sql.DB) int {
			return adminRetryCovers(ctx, db, config.DataDir(getenv), stdout, stderr)
		})
	}
	fmt.Fprint(stderr, usage)
	return 2
}

// withDatabase opens the server's existing database for an admin command. It never creates
// one: a wrong ABYSS_DATA_DIR must fail, not start an empty server.
func withDatabase(ctx context.Context, stderr io.Writer, getenv func(string) string, fn func(*sql.DB) int) int {
	dir := config.DataDir(getenv)
	db, err := store.OpenExisting(ctx, filepath.Join(dir, databaseFile))
	if errors.Is(err, store.ErrNoDatabase) {
		fmt.Fprintf(stderr, "%v.\nIs ABYSS_DATA_DIR (%s) the server's data folder, and has the server started at least once?\n", err, dir)
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer db.Close()
	return fn(db)
}

// withCatalogLock opens the database for a command that changes the catalog, holding the data
// folder's admin lock: two such commands never run at once.
func withCatalogLock(ctx context.Context, stderr io.Writer, getenv func(string) string, fn func(*sql.DB) int) int {
	return withDatabase(ctx, stderr, getenv, func(db *sql.DB) int {
		dir := config.DataDir(getenv)
		unlock, err := lockData(dir)
		if errors.Is(err, errLocked) {
			fmt.Fprintf(stderr, "Another admin command is changing the catalog (it holds %s); wait for it to finish, then run this one again.\n", filepath.Join(dir, lockFile))
			return 1
		}
		if err != nil {
			fmt.Fprintln(stderr, "admin lock:", err)
			return 1
		}
		defer unlock()
		return fn(db)
	})
}

func adminUsers(ctx context.Context, db *sql.DB, action, email string, stdout, stderr io.Writer) int {
	// The idle timeout plays no part in these commands.
	svc := accounts.New(db, 24*time.Hour)
	var err error
	switch action {
	case "reset-password":
		var temp string
		var until time.Time
		if temp, until, err = svc.ResetPassword(ctx, email); err == nil {
			fmt.Fprintf(stdout, "Temporary password for %s:\n\n    %s\n\n", email, temp)
			fmt.Fprintf(stdout, "It works for %d hours, until %s, and is shown only this once.\n",
				int(accounts.TemporaryPasswordLifetime.Hours()), until.UTC().Format("2006-01-02 15:04 UTC"))
			fmt.Fprintln(stdout, "Pass it on to the user yourself; they sign in with it and then choose their own password in the app.")
			fmt.Fprintln(stdout, "Every session of the account has been signed out.")
		}
	case "disable":
		if err = svc.Disable(ctx, email); err == nil {
			fmt.Fprintf(stdout, "Disabled %s: its sessions are signed out and signing in answers 403 account_disabled.\n", email)
			fmt.Fprintf(stdout, "Undo with: abyssreader-server admin users enable %s\n", email)
		}
	case "enable":
		if err = svc.Enable(ctx, email); err == nil {
			fmt.Fprintf(stdout, "Enabled %s: the account can sign in again.\n", email)
		}
	}
	if errors.Is(err, accounts.ErrNoAccount) {
		fmt.Fprintf(stderr, "No account with the email %s.\n", email)
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// coverFetchOptions configures cover downloads; tests replace it to reach a local test host.
var coverFetchOptions = covers.FetchOptions{}

func catalogFor(db *sql.DB, dataDir string) (*catalog.Catalog, error) {
	store, err := covers.OpenStore(dataDir)
	if err != nil {
		return nil, err
	}
	pageStore, err := pages.OpenStore(dataDir)
	if err != nil {
		return nil, err
	}
	o := coverFetchOptions
	if o.UserAgent == "" {
		o.UserAgent = "abyssreader-server/" + version + " (cover import)"
	}
	return catalog.New(db, store, covers.NewFetcher(o).Fetch).UsePages(pageStore), nil
}

// adminImport runs `admin import FOLDER`: exit 0 when every title and every hosted chapter was
// imported (covers that failed are listed, and their titles imported without a cover), 1 when
// the manifest is refused or a title or a chapter failed.
func adminImport(ctx context.Context, db *sql.DB, dataDir, folder string, removeChapters bool, stdout, stderr io.Writer) int {
	m, err := manifest.Load(folder)
	if err != nil {
		fmt.Fprintf(stderr, "Nothing was imported from %s:\n%v\n", filepath.Join(folder, manifest.FileName), err)
		return 1
	}
	cat, err := catalogFor(db, dataDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	r, err := cat.RemoveChapters(removeChapters).Import(ctx, m)
	if err != nil {
		fmt.Fprintf(stderr, "The import stopped: %v\nWhat was imported before stays; run the import again.\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Imported %s.\n", filepath.Join(folder, manifest.FileName))
	fmt.Fprintf(stdout, "Sources: %d added, %d updated, %d unchanged.\n", r.Sources.Added, r.Sources.Updated, r.Sources.Unchanged)
	fmt.Fprintf(stdout, "Titles: %d added, %d updated, %d unchanged, %d failed.\n", r.Titles.Added, r.Titles.Updated, r.Titles.Unchanged, len(r.Failed))
	fmt.Fprintf(stdout, "Covers: %d stored, %d unchanged, %d failed.\n", r.Covers.Stored, r.Covers.Unchanged, len(r.CoverFailures))
	if c := r.Chapters; c.Added+c.Updated+c.Unchanged+c.Removed+len(r.ChapterFailures) > 0 {
		fmt.Fprintf(stdout, "Hosted chapters: %d added, %d updated, %d unchanged, %d removed, %d failed.\n", c.Added, c.Updated, c.Unchanged, c.Removed, len(r.ChapterFailures))
		fmt.Fprintf(stdout, "Page files: %d stored, %d already stored.\n", r.Pages.Stored, r.Pages.Reused)
	}
	if len(r.RemovedChapters) > 0 {
		fmt.Fprintln(stdout, "\nRemoved hosted chapters:")
		for _, c := range r.RemovedChapters {
			fmt.Fprintf(stdout, "  %s\n", c)
		}
	}
	if len(r.KeptChapters) > 0 {
		fmt.Fprintln(stdout, "\nHosted chapters kept although the manifest no longer lists them (title/lang/chapter). A removed")
		fmt.Fprintln(stdout, "chapter loses its id, and readers their marks on it; to remove them, import again with --remove-chapters:")
		for _, c := range r.KeptChapters {
			fmt.Fprintf(stdout, "  %s\n", c)
		}
	}
	if len(r.Failed) > 0 {
		fmt.Fprintln(stdout, "\nTitles not imported:")
		for _, f := range r.Failed {
			fmt.Fprintf(stdout, "  %s: %s\n", f.Ref, f.Reason)
		}
	}
	if len(r.ChapterFailures) > 0 {
		fmt.Fprintln(stdout, "\nHosted chapters not imported (title/lang/chapter; a chapter imported before keeps its pages until")
		fmt.Fprintln(stdout, "its files are fixed and the folder imported again):")
		for _, f := range r.ChapterFailures {
			fmt.Fprintf(stdout, "  %s: %s\n", f.Ref, f.Reason)
		}
	}
	if len(r.CoverFailures) > 0 {
		fmt.Fprintln(stdout, "\nTitles imported without a cover (a cover URL: run `abyssreader-server admin covers retry` later;")
		fmt.Fprintln(stdout, "a cover file: fix the file and import the folder again):")
		for _, f := range r.CoverFailures {
			fmt.Fprintf(stdout, "  %s: %s\n", f.Ref, f.Reason)
		}
	}
	if len(r.Failed)+len(r.ChapterFailures)+len(r.KeptChapters) > 0 {
		return 1
	}
	return 0
}

func adminRemoveTitle(ctx context.Context, db *sql.DB, dataDir, ref string, stdout, stderr io.Writer) int {
	cat, err := catalogFor(db, dataDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	removed, err := cat.Remove(ctx, ref)
	if errors.Is(err, catalog.ErrNotFound) {
		fmt.Fprintf(stderr, "No title with the ref %s.\n", ref)
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Removed %q (ref %s, id %d) with its source titles and hosted chapters. Deleted reports=%d.\n", removed.Name, ref, removed.ID, removed.Reports)
	fmt.Fprintln(stdout, "An import of a manifest that still lists this ref adds the title again, under a new id.")
	return 0
}

// adminRetryCovers runs `admin covers retry`: exit 0 when every pending cover was stored, 1
// when some still fail.
func adminRetryCovers(ctx context.Context, db *sql.DB, dataDir string, stdout, stderr io.Writer) int {
	cat, err := catalogFor(db, dataDir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	r, err := cat.RetryCovers(ctx)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if r.Stored == 0 && len(r.Failures) == 0 {
		fmt.Fprintln(stdout, "No cover to retry: every cover URL has its image stored.")
		return 0
	}
	fmt.Fprintf(stdout, "Covers: %d stored, %d still failing.\n", r.Stored, len(r.Failures))
	for _, f := range r.Failures {
		fmt.Fprintf(stdout, "  %s: %s\n", f.Ref, f.Reason)
	}
	if len(r.Failures) > 0 {
		return 1
	}
	return 0
}

// healthcheck asks the local server's /healthz; the container's HEALTHCHECK runs it (the image
// has no shell and no curl).
func healthcheck(ctx context.Context, stderr io.Writer, getenv func(string) string) int {
	base, err := localURL(getenv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/healthz", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(stderr, "unhealthy:", err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(stderr, "unhealthy: /healthz answered", resp.Status)
		return 1
	}
	return 0
}

// localURL is the address the server listens on inside its container, for healthcheck and
// status (the image has no shell and no curl).
func localURL(getenv func(string) string) (string, error) {
	listen := getenv("ABYSS_LISTEN")
	if listen == "" {
		listen = ":8080"
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("ABYSS_LISTEN: %w", err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}
