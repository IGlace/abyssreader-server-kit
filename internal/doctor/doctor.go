// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package doctor diagnoses this server's setup. It does not exercise application operations.
package doctor

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"slices"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/backup"
	"github.com/IGlace/abyssreader-server-kit/internal/config"
	"github.com/IGlace/abyssreader-server-kit/internal/covers"
)

const checkTimeout = 5 * time.Second

type Result struct {
	Name    string
	Message string
	Failed  bool
}

type Checks struct {
	Lookup    func(context.Context, string) ([]netip.Addr, error)
	Available func(string) (int64, error)
	Backups   func(context.Context, string) ([]backup.Generation, error)
	Images    func(context.Context, string) ([]string, error)
	Client    *http.Client
}

func LocalChecks() Checks {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return Checks{
		Lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		Available: backup.Available,
		Backups:   backup.List,
		Images:    imagePaths,
		Client:    &http.Client{Transport: transport, Timeout: checkTimeout},
	}
}

// Inspect runs read-only setup checks. expected contains addresses from the
// operator's DNS plan, since a Docker container cannot reliably infer host IPs.
func Inspect(ctx context.Context, cfg config.Config, expected []netip.Addr, now time.Time, checks Checks) []Result {
	publicURL, _ := url.Parse(cfg.PublicURL) // validated by config.Load
	client := *checks.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return []Result{
		checkDNS(ctx, publicURL.Hostname(), expected, checks.Lookup),
		checkStatus(ctx, &client, cfg.PublicURL),
		checkImages(ctx, &client, cfg, checks.Images),
		checkDisk(cfg.DataDir, checks.Available),
		checkBackups(ctx, cfg, now, checks.Backups),
	}
}

func checkDNS(ctx context.Context, host string, expected []netip.Addr, lookup func(context.Context, string) ([]netip.Addr, error)) Result {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	records, err := lookup(ctx, host)
	if err != nil || len(records) == 0 {
		return Result{"DNS", "The server name does not resolve. Check the A and AAAA records.", true}
	}
	if len(expected) == 0 {
		return Result{"DNS", "The server name resolves. Supply expected IP addresses to compare the records.", false}
	}
	for _, target := range expected {
		if !slices.ContainsFunc(records, func(record netip.Addr) bool { return record.Unmap() == target.Unmap() }) {
			return Result{"DNS", "A configured DNS record does not match the expected address. Check both A and AAAA records.", true}
		}
	}
	for _, record := range records {
		if !slices.ContainsFunc(expected, func(addr netip.Addr) bool { return addr.Unmap() == record.Unmap() }) {
			return Result{"DNS", "The name also resolves to an unexpected address. Check both A and AAAA records.", true}
		}
	}
	return Result{"DNS", "The name resolves to the expected addresses.", false}
}

func request(ctx context.Context, client *http.Client, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func checkStatus(ctx context.Context, client *http.Client, origin string) Result {
	const name = "TLS and status"
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	resp, err := request(ctx, client, origin+"/api/status")
	if err != nil {
		var certificate *tls.CertificateVerificationError
		if errors.As(err, &certificate) {
			return Result{name, "The TLS certificate could not be verified. Check its name, issuer and expiry.", true}
		}
		return Result{name, "Could not reach /api/status. Check HTTPS, the reverse proxy and the running server.", true}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{name, fmt.Sprintf("/api/status returned HTTP %d. Check the reverse proxy and server.", resp.StatusCode), true}
	}
	const maxStatusBytes = 64 << 10
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxStatusBytes+1))
	var jsonObject map[string]json.RawMessage
	if err != nil || len(data) > maxStatusBytes || json.Unmarshal(data, &jsonObject) != nil || jsonObject == nil {
		return Result{name, "/api/status did not return a JSON object. Check the proxy's response.", true}
	}
	if resp.TLS == nil {
		return Result{name, "/api/status answers over HTTP. The app requires HTTPS.", true}
	}
	return Result{name, "The public HTTPS address serves /api/status.", false}
}

func checkImages(ctx context.Context, client *http.Client, cfg config.Config, images func(context.Context, string) ([]string, error)) Result {
	const name = "Images"
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	paths, err := images(ctx, cfg.DataDir)
	if err != nil {
		return Result{name, "Could not read image references from the data volume.", true}
	}
	if len(paths) == 0 {
		return Result{name, "No stored covers or pages to check.", false}
	}
	for _, path := range paths {
		resp, err := request(ctx, client, cfg.PublicURL+path)
		if err != nil {
			return Result{name, "Could not load a stored image at the public address.", true}
		}
		contentType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
		_, readErr := io.CopyN(io.Discard, resp.Body, 1)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || readErr != nil ||
			(contentType != "image/jpeg" && contentType != "image/png" && contentType != "image/webp") {
			return Result{name, "A stored image could not be loaded without credentials. Check /covers/ and /pages/ proxy routing.", true}
		}
	}
	return Result{name, "Sampled stored images load without credentials.", false}
}

func imagePaths(ctx context.Context, dataDir string) ([]string, error) {
	u := url.URL{Scheme: "file", Path: filepath.Join(dataDir, backup.Database), RawQuery: "mode=ro&_pragma=busy_timeout(1000)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var paths []string
	for _, sample := range []struct{ prefix, query string }{
		{"/covers/", "SELECT cover_image FROM titles WHERE cover_image IS NOT NULL LIMIT 1"},
		{"/pages/", "SELECT image FROM page_images LIMIT 1"},
	} {
		var filename string
		err := db.QueryRowContext(ctx, sample.query).Scan(&filename)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !covers.ValidName(filename) {
			return nil, errors.New("invalid stored image name")
		}
		paths = append(paths, sample.prefix+filename)
	}
	return paths, nil
}

func checkDisk(dataDir string, available func(string) (int64, error)) Result {
	bytes, err := available(dataDir)
	if err != nil {
		return Result{"Disk", "Could not read free space for the data volume.", true}
	}
	if bytes < backup.SpaceReserve {
		return Result{"Disk", "The data volume has less than the 64 MiB write reserve. Free space before taking a backup.", true}
	}
	return Result{"Disk", fmt.Sprintf("%d MiB free on the data volume.", bytes>>20), false}
}

func checkBackups(ctx context.Context, cfg config.Config, now time.Time, list func(context.Context, string) ([]backup.Generation, error)) Result {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	generations, err := list(ctx, cfg.DataDir)
	if err != nil {
		return Result{"Backups", "Could not read local backup metadata.", true}
	}
	for _, generation := range generations {
		if generation.Raw || generation.Incomplete {
			continue
		}
		maxAge := map[string]time.Duration{"daily": 48 * time.Hour, "weekly": 8 * 24 * time.Hour}[cfg.BackupSchedule]
		if maxAge != 0 && now.Sub(generation.Created) >= maxAge {
			return Result{"Backups", "The latest complete local backup is overdue. Check the backup schedule and server logs.", true}
		}
		return Result{"Backups", fmt.Sprintf("Latest complete local backup metadata is dated %s UTC.", generation.Created.UTC().Format(time.DateOnly)), false}
	}
	return Result{"Backups", "No complete local backup found. Take a backup and keep an off-machine copy.", true}
}
