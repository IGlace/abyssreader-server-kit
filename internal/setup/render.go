// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package setup

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// CaddyImage is the Caddy release the Caddy mode runs (Caddy v2.11.9), pinned like the
// server's own image.
const CaddyImage = "caddy:2.11.9@sha256:4851e54a46b206517c574821bb5cc67f170d2156041e9fbc4c9193b45e84a75b"

// ProjectName is the Compose project of an installed server: its containers, network and
// volumes are named after it (the data volume is abyssreader-server_data).
const ProjectName = "abyssreader-server"

var imageRef = regexp.MustCompile(`^[a-z0-9][A-Za-z0-9._:/-]{0,254}(@sha256:[a-f0-9]{64})?$`)

// CheckImage checks the server image reference the installer pins: an exact tag (never
// "latest") or a digest.
func CheckImage(ref string) error {
	if !imageRef.MatchString(ref) {
		return errors.New("not an image reference")
	}
	name := ref
	if strings.Contains(name, "@") {
		return nil // a digest pins it exactly
	}
	slash := strings.LastIndex(name, "/")
	colon := strings.LastIndex(name, ":")
	if colon <= slash {
		return errors.New("the image must be pinned to an exact tag")
	}
	if tag := name[colon+1:]; tag == "latest" {
		return errors.New(`the image must be pinned to an exact tag, never "latest"`)
	}
	return nil
}

const envHeader = `# Settings of this AbyssReader server, written by install.sh (abyssreader-server setup).
# Change them with "sudo sh install.sh configure", which asks again with these answers filled in
# and keeps every setting it does not ask about. Or edit this file, then restart:
#   cd /opt/abyssreader-server && sudo docker compose up -d --force-recreate
# Every server setting is described in the kit's .env.example.
# This file holds secrets (the access key, the error-reporting DSN): keep it root-owned, chmod 600.
`

// renderEnv writes the .env file: the answers first, in the order of the questions, then the
// settings the setup does not ask about, then every other setting the previous file had.
// rest holds the settings that are not answers.
func renderEnv(a Answers, rest map[string]string) (string, error) {
	env := a.env()
	var b strings.Builder
	b.WriteString(envHeader)
	var err error
	line := func(key string, value string) {
		if err != nil {
			return
		}
		q, e := Quote(value)
		if e != nil {
			err = fmt.Errorf("%s: %v", key, e)
			return
		}
		b.WriteString(key + "=" + q + "\n")
	}
	section := func(title string, comment ...string) {
		b.WriteString("\n# ── " + title + " ──\n")
		for _, c := range comment {
			b.WriteString("# " + c + "\n")
		}
	}
	section("Address and HTTPS", "The address users type in the app. ABYSS_INSTALL_HTTPS (caddy or own-proxy) and",
		"ABYSS_INSTALL_PORT (own-proxy: the port on 127.0.0.1) are install.sh's; the server ignores them.")
	line(KeyPublicURL, env[KeyPublicURL])
	line(KeyHTTPS, env[KeyHTTPS])
	line(KeyPort, env[KeyPort])
	section("Server and operator details", "Shown in the app (/api/status).")
	for _, k := range []string{KeyServerName, KeyOperatorName, KeyContact, KeyPrivacy, KeyTerms, KeyCopyright} {
		line(k, env[k])
	}
	section("Accounts", "An empty ABYSS_ACCESS_KEY means no access key. The access key is a secret.")
	line(KeySignUp, env[KeySignUp])
	line(KeyAccessKey, env[KeyAccessKey])
	section("Optional features", "Comma-separated; all are off by default.")
	line(KeyFeatures, env[KeyFeatures])
	section("Error reporting", "A GlitchTip or Sentry DSN; empty: none. The DSN is a secret.")
	line(KeyDSN, env[KeyDSN])
	section("Backups", "SQLite and images in /data/backups: daily, weekly or off; keep 1–1000.")
	line(KeyBackup, env[KeyBackup])
	line(KeyBackupKeep, env[KeyBackupKeep])

	section("Other settings", "Uncomment a setting to override the server's default.")
	for _, o := range others {
		if value, set := rest[o.Key]; set {
			line(o.Key, value)
		} else {
			fmt.Fprintf(&b, "# %s=%s\n", o.Key, o.Default)
		}
	}
	var kept []string
	for k := range rest {
		if !isOther(k) && !slices.Contains(asked, k) && k != KeyDNSCheck {
			kept = append(kept, k)
		}
	}
	if len(kept) > 0 {
		sort.Strings(kept)
		section("Kept from the previous file")
		for _, k := range kept {
			line(k, rest[k])
		}
	}
	return b.String(), err
}

func isOther(k string) bool {
	return slices.ContainsFunc(others, func(o struct{ Key, Default string }) bool { return o.Key == k })
}

// renderCompose writes the operator's compose.yaml: the server image pinned to an exact tag,
// the hardening of the kit's own compose.yaml, Docker log rotation (10 MB × 3), and either Caddy in front (ports 80 and 443) or the server on 127.0.0.1:<port> only.
func renderCompose(a Answers, image, kitVersion string) (string, error) {
	if err := CheckImage(image); err != nil {
		return "", fmt.Errorf("image %q: %v", image, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `# The AbyssReader server, written by install.sh (abyssreader-server setup, kit %s).
# install.sh configure writes it again; it asks first when you have changed this file.
# HTTPS: %s.
name: %s
services:
  server:
    image: %s
    env_file:
      - .env
`, kitVersion, httpsComment(a), ProjectName, image)
	if a.HTTPS == OwnProxy {
		fmt.Fprintf(&b, `    # Your proxy forwards to this address; Docker publishes it on this machine only.
    ports:
      - "127.0.0.1:%d:8080"
`, a.Port)
	}
	b.WriteString(`    volumes:
      - data:/data
      # Import folders for "admin import /import/<folder>".
      - ./import:/import:ro
    restart: unless-stopped
    read_only: true
    tmpfs:
      - /tmp
    cap_drop: [ALL]
    security_opt:
      - no-new-privileges:true
    logging: &logging
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"
`)
	if a.HTTPS == Caddy {
		fmt.Fprintf(&b, `  caddy:
    image: %s
    depends_on: [server]
    ports:
      - "80:80"
      - "443:443"
      - "443:443/udp"
    volumes:
      - ./Caddyfile:/etc/caddy/Caddyfile:ro
      - caddy_data:/data
      - caddy_config:/config
    restart: unless-stopped
    read_only: true
    tmpfs:
      - /tmp
    cap_drop: [ALL]
    cap_add: [NET_BIND_SERVICE]
    security_opt:
      - no-new-privileges:true
    logging: *logging
volumes:
  data:
  caddy_data:
  caddy_config:
`, CaddyImage)
	} else {
		b.WriteString("volumes:\n  data:\n")
	}
	return b.String(), nil
}

func httpsComment(a Answers) string {
	if a.HTTPS == Caddy {
		return "Caddy gets a Let's Encrypt certificate for " + a.Host() + " (ports 80 and 443)"
	}
	return "your own proxy, in front of http://127.0.0.1:" + strconv.Itoa(a.Port)
}

// renderCaddyfile writes Caddy's configuration: the public address, proxied to the server.
// The host passed checkPublicURL (letters, digits, dots and hyphens only).
func renderCaddyfile(a Answers) string {
	return `# Written by install.sh (abyssreader-server setup). Caddy gets and renews the certificate itself.
{
	admin off
}

` + a.Host() + ` {
	reverse_proxy server:8080
}
`
}

// renderInfo writes what install.sh needs to know, without anything secret, one KEY=value a
// line (values are checked: no spaces, no quotes).
func renderInfo(a Answers, accessKeyChanged bool) string {
	yes := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	return fmt.Sprintf("HTTPS=%s\nPORT=%d\nPUBLIC_URL=%s\nHOST=%s\nACCESS_KEY=%s\nACCESS_KEY_CHANGED=%s\n",
		a.HTTPS, a.Port, a.PublicURL, a.Host(), yes(a.AccessKey != ""), yes(accessKeyChanged))
}
