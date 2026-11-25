// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package setup

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/config"
)

const testImage = "ghcr.io/iglace/abyssreader-server-kit:1.0.0"

const ownProxyAnswers = `# The demo server's answers
ABYSS_PUBLIC_URL=https://demo.example.org
ABYSS_INSTALL_HTTPS=own-proxy
ABYSS_INSTALL_PORT=8091
ABYSS_SERVER_NAME="AbyssReader Demo"
ABYSS_OPERATOR_NAME="Demo team"
ABYSS_OPERATOR_CONTACT=mailto:demo@example.org
ABYSS_OPERATOR_PRIVACY_URL=https://example.org/privacy
ABYSS_SIGN_UP_OPEN=yes
ABYSS_ACCESS_KEY=
ABYSS_FEATURES=hosted-chapters
ABYSS_BACKUP_SCHEDULE=weekly
ABYSS_BACKUP_KEEP=12
`

type run struct {
	dir string
	out bytes.Buffer
	err error
}

// runSetup runs the setup in a fresh work folder with the given files.
func runSetup(t *testing.T, files map[string]string, o Options) *run {
	t.Helper()
	r := &run{dir: t.TempDir()}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(r.dir, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	o.WorkDir = r.dir
	if o.Image == "" {
		o.Image = testImage
	}
	if o.KitVersion == "" {
		o.KitVersion = "1.0.0"
	}
	if o.In == nil {
		o.In = strings.NewReader("")
	}
	o.Out = &r.out
	if o.Lookup == nil {
		o.Lookup = func(context.Context, string) ([]string, error) { return nil, errors.New("no DNS in tests") }
	}
	r.err = Run(context.Background(), o)
	return r
}

func (r *run) file(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.dir, OutDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (r *run) env(t *testing.T) map[string]string {
	t.Helper()
	m, err := ReadEnv(strings.NewReader(r.file(t, ".env")))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (r *run) noOutput(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(r.dir, OutDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused setup wrote %s", OutDir)
	}
}

func TestAnswersFileOwnProxy(t *testing.T) {
	r := runSetup(t, map[string]string{AnswersFile: ownProxyAnswers}, Options{})
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out.String())
	}
	env := r.env(t)
	for k, want := range map[string]string{
		KeyPublicURL: "https://demo.example.org", KeyHTTPS: "own-proxy", KeyPort: "8091", KeyServerName: "AbyssReader Demo",
		KeyOperatorName: "Demo team", KeyContact: "mailto:demo@example.org", KeyPrivacy: "https://example.org/privacy",
		KeyTerms: "", KeyCopyright: "", KeySignUp: "true", KeyAccessKey: "", KeyFeatures: "hosted-chapters", KeyDSN: "",
		KeyBackup: "weekly", KeyBackupKeep: "12",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	if _, ok := env[KeyDNSCheck]; ok {
		t.Error("the answers-file-only key must not reach .env")
	}
	// The server takes what the setup wrote.
	c, err := config.Load(func(k string) string { return env[k] })
	if err != nil || c.ServerName != "AbyssReader Demo" || !c.FeatureOn(config.HostedChapters) || c.BackupKeep != 12 {
		t.Fatalf("%+v %v", c, err)
	}
	compose := r.file(t, "compose.yaml")
	for _, want := range []string{"image: " + testImage + "\n", `"127.0.0.1:8091:8080"`, `max-size: "10m"`, `max-file: "3"`, "read_only: true", "cap_drop: [ALL]", "env_file:\n      - .env\n"} {
		if !strings.Contains(compose, want) {
			t.Errorf("compose.yaml lacks %q:\n%s", want, compose)
		}
	}
	if strings.Contains(compose, "caddy") || strings.Contains(compose, "0.0.0.0") {
		t.Errorf("own-proxy compose.yaml has Caddy or a public port:\n%s", compose)
	}
	if _, err := os.Stat(filepath.Join(r.dir, OutDir, "Caddyfile")); !errors.Is(err, os.ErrNotExist) {
		t.Error("no Caddyfile in own-proxy mode")
	}
	info := r.file(t, "install.info")
	for _, want := range []string{"HTTPS=own-proxy\n", "PORT=8091\n", "PUBLIC_URL=https://demo.example.org\n", "ACCESS_KEY=no\n", "ACCESS_KEY_CHANGED=no\n"} {
		if !strings.Contains(info, want) {
			t.Errorf("install.info lacks %q:\n%s", want, info)
		}
	}
	for name, mode := range map[string]os.FileMode{".env": 0o600, "compose.yaml": 0o644, "install.info": 0o644} {
		st, err := os.Stat(filepath.Join(r.dir, OutDir, name))
		if err != nil || st.Mode().Perm() != mode {
			t.Errorf("%s mode %v, want %v (%v)", name, st.Mode().Perm(), mode, err)
		}
	}
}

func TestAwkwardAnswersSurviveTheEnvFile(t *testing.T) {
	name := `Tom's "Manga" $HOME \ Shelf`
	answers := ownProxyAnswers + "ABYSS_OPERATOR_NAME='" + strings.ReplaceAll(name, "'", `\'`) + "'\n"
	r := runSetup(t, map[string]string{AnswersFile: answers}, Options{})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if got := r.env(t)[KeyOperatorName]; got != name {
		t.Fatalf("operator name %q, want %q\n%s", got, name, r.file(t, ".env"))
	}
	// A line break in a name is refused, not written.
	r = runSetup(t, map[string]string{AnswersFile: ownProxyAnswers + `ABYSS_SERVER_NAME="two\nlines"` + "\n"}, Options{})
	if r.err == nil || !strings.Contains(r.err.Error(), KeyServerName) {
		t.Fatalf("want the server name refused: %v", r.err)
	}
	r.noOutput(t)
}

// current is an installed .env with secrets, a changed rate limit and a setting of a later
// kit version.
const currentEnv = `ABYSS_PUBLIC_URL="https://reader.example.org"
ABYSS_INSTALL_HTTPS="own-proxy"
ABYSS_INSTALL_PORT="8080"
ABYSS_SERVER_NAME="Lantern Library"
ABYSS_OPERATOR_NAME="Lantern team"
ABYSS_OPERATOR_CONTACT="https://reader.example.org/contact"
ABYSS_SIGN_UP_OPEN="false"
ABYSS_ACCESS_KEY="an-existing-access-key-0123456789"
ABYSS_ERROR_REPORTING_DSN="https://0123abcd@glitchtip.example.org/7"
ABYSS_SIGN_UPS_PER_HOUR="3"
ABYSS_SOMETHING_LATER="kept as it is"
`

func TestConfigureKeepsWhatItIsNotAsked(t *testing.T) {
	r := runSetup(t, map[string]string{CurrentFile: currentEnv, AnswersFile: "ABYSS_SERVER_NAME=Lantern Library Two\n"}, Options{})
	if r.err != nil {
		t.Fatal(r.err)
	}
	env := r.env(t)
	for k, want := range map[string]string{
		KeyServerName: "Lantern Library Two", KeyAccessKey: "an-existing-access-key-0123456789",
		KeyDSN: "https://0123abcd@glitchtip.example.org/7", KeySignUp: "false", KeyContact: "https://reader.example.org/contact",
		"ABYSS_SIGN_UPS_PER_HOUR": "3", "ABYSS_SOMETHING_LATER": "kept as it is", KeyBackup: "daily", KeyBackupKeep: "14",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	if !strings.Contains(r.file(t, "install.info"), "ACCESS_KEY=yes\nACCESS_KEY_CHANGED=no\n") {
		t.Errorf("info: %s", r.file(t, "install.info"))
	}
	for _, secret := range []string{"an-existing-access-key", "0123abcd"} {
		if strings.Contains(r.out.String(), secret) {
			t.Errorf("the setup printed a secret:\n%s", r.out.String())
		}
	}
}

func TestAccessKeyAnswers(t *testing.T) {
	keyOf := func(t *testing.T, files map[string]string) (string, string) {
		r := runSetup(t, files, Options{})
		if r.err != nil {
			t.Fatal(r.err)
		}
		return r.env(t)[KeyAccessKey], r.file(t, "install.info")
	}
	// generate on an install with a key keeps it: a scripted configure run twice never changes it.
	k, info := keyOf(t, map[string]string{CurrentFile: currentEnv, AnswersFile: "ABYSS_ACCESS_KEY=generate\n"})
	if k != "an-existing-access-key-0123456789" || !strings.Contains(info, "ACCESS_KEY_CHANGED=no") {
		t.Fatalf("generate replaced the existing key: %q", k)
	}
	// generate on an install without one makes one.
	k, info = keyOf(t, map[string]string{AnswersFile: ownProxyAnswers + "ABYSS_ACCESS_KEY=generate\n"})
	if !regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`).MatchString(k) || config.CheckAccessKey(k) != nil || !strings.Contains(info, "ACCESS_KEY_CHANGED=yes") {
		t.Fatalf("generated key %q / %s", k, info)
	}
	k2, _ := keyOf(t, map[string]string{AnswersFile: ownProxyAnswers + "ABYSS_ACCESS_KEY=generate\n"})
	if k2 == k {
		t.Fatal("two generated keys are the same")
	}
	// new always makes a new one; empty removes it; a key of one's own is taken.
	if k, _ = keyOf(t, map[string]string{CurrentFile: currentEnv, AnswersFile: "ABYSS_ACCESS_KEY=new\n"}); k == "an-existing-access-key-0123456789" || len(k) != 43 {
		t.Fatalf("new: %q", k)
	}
	if k, _ = keyOf(t, map[string]string{CurrentFile: currentEnv, AnswersFile: "ABYSS_ACCESS_KEY=\n"}); k != "" {
		t.Fatalf("empty: %q", k)
	}
	if k, _ = keyOf(t, map[string]string{CurrentFile: currentEnv, AnswersFile: "ABYSS_ACCESS_KEY=my-own-access-key-of-24\n"}); k != "my-own-access-key-of-24" {
		t.Fatalf("own: %q", k)
	}
	r := runSetup(t, map[string]string{CurrentFile: currentEnv, AnswersFile: "ABYSS_ACCESS_KEY=short-secret\n"}, Options{})
	if r.err == nil || strings.Contains(r.err.Error(), "short-secret") {
		t.Fatalf("a short key must be refused without being shown: %v", r.err)
	}
}

func TestBadAnswersAreAllNamedAndNothingIsWritten(t *testing.T) {
	r := runSetup(t, map[string]string{AnswersFile: `ABYSS_PUBLIC_URL=http://reader.example.org
ABYSS_INSTALL_HTTPS=own-proxy
ABYSS_INSTALL_PORT=70000
ABYSS_SERVER_NAME=
ABYSS_OPERATOR_NAME=Team
ABYSS_OPERATOR_CONTACT=team@example.org
ABYSS_OPERATOR_TERMS_URL=http://example.org/terms
ABYSS_SIGN_UP_OPEN=maybe
ABYSS_FEATURES=google-sign-in
ABYSS_ERROR_REPORTING_DSN=http://secretkey@glitchtip.example.org/1
ABYSS_BACKUP_SCHEDULE=hourly
ABYSS_BACKUP_KEEP=0
ABYSS_LISTEN=:9000
ABYSS_SEVRER_NAME=typo
`}, Options{})
	if r.err == nil {
		t.Fatal("want an error")
	}
	r.noOutput(t)
	msg := r.err.Error()
	for _, k := range []string{KeyPublicURL, KeyPort, KeyServerName, KeyContact, KeyTerms, KeySignUp, KeyFeatures, KeyDSN, KeyBackup, KeyBackupKeep, "ABYSS_LISTEN", "ABYSS_SEVRER_NAME"} {
		if !strings.Contains(msg, k) {
			t.Errorf("the error does not name %s:\n%s", k, msg)
		}
	}
	if strings.Contains(msg, "secretkey") {
		t.Errorf("the error shows the DSN:\n%s", msg)
	}
	// Named in the order of the questions.
	if strings.Index(msg, KeyPublicURL) > strings.Index(msg, KeyBackupKeep) {
		t.Errorf("not in question order:\n%s", msg)
	}
}

func TestFreshInstallNeedsTheRequiredAnswers(t *testing.T) {
	r := runSetup(t, map[string]string{AnswersFile: "ABYSS_INSTALL_HTTPS=own-proxy\n"}, Options{})
	if r.err == nil {
		t.Fatal("want an error")
	}
	for _, k := range []string{KeyPublicURL, KeyServerName, KeyOperatorName, KeyContact} {
		if !strings.Contains(r.err.Error(), k) {
			t.Errorf("does not name %s: %v", k, r.err)
		}
	}
}

func TestNoTerminalAndNoAnswersFile(t *testing.T) {
	r := runSetup(t, nil, Options{Interactive: false})
	if r.err == nil || !strings.Contains(r.err.Error(), "--answers") {
		t.Fatalf("%v", r.err)
	}
	r.noOutput(t)
}

func TestHandEditedSettingTheServerRefuses(t *testing.T) {
	r := runSetup(t, map[string]string{CurrentFile: currentEnv + "ABYSS_SIGN_INS_PER_MINUTE=lots\n", AnswersFile: "ABYSS_SERVER_NAME=X\n"}, Options{})
	if r.err == nil || !strings.Contains(r.err.Error(), "ABYSS_SIGN_INS_PER_MINUTE") {
		t.Fatalf("%v", r.err)
	}
	r.noOutput(t)
}

const caddyAnswers = `ABYSS_PUBLIC_URL=https://reader.example.org
ABYSS_SERVER_NAME=Lantern
ABYSS_OPERATOR_NAME=Team
ABYSS_OPERATOR_CONTACT=mailto:team@example.org
`

func lookupTo(addrs ...string) LookupFunc {
	return func(_ context.Context, host string) ([]string, error) {
		if host != "reader.example.org" {
			return nil, errors.New("unexpected host " + host)
		}
		return addrs, nil
	}
}

func TestCaddyMode(t *testing.T) {
	r := runSetup(t, map[string]string{AnswersFile: caddyAnswers, HostAddrsFile: "10.0.0.5 203.0.113.9 2001:db8::9\n"},
		Options{Lookup: lookupTo("203.0.113.9")})
	if r.err != nil {
		t.Fatal(r.err)
	}
	compose := r.file(t, "compose.yaml")
	for _, want := range []string{"image: " + CaddyImage, `"443:443"`, `"80:80"`, "./Caddyfile:/etc/caddy/Caddyfile:ro", "caddy_data:", "cap_add: [NET_BIND_SERVICE]", "logging: *logging"} {
		if !strings.Contains(compose, want) {
			t.Errorf("compose.yaml lacks %q:\n%s", want, compose)
		}
	}
	if strings.Contains(compose, "8080:8080") {
		t.Error("the server's port must not be published in Caddy mode")
	}
	if cf := r.file(t, "Caddyfile"); !strings.Contains(cf, "reader.example.org {\n\treverse_proxy server:8080\n}") || !strings.Contains(cf, "admin off") {
		t.Errorf("Caddyfile:\n%s", cf)
	}
	if r.env(t)[KeyHTTPS] != Caddy {
		t.Error("caddy is the default mode")
	}
}

func TestCaddyModeDNSCheck(t *testing.T) {
	files := map[string]string{AnswersFile: caddyAnswers, HostAddrsFile: "203.0.113.9"}
	r := runSetup(t, files, Options{Lookup: lookupTo("198.51.100.1")})
	if r.err == nil || !strings.Contains(r.err.Error(), "198.51.100.1") || !strings.Contains(r.err.Error(), KeyDNSCheck+"=skip") {
		t.Fatalf("a name pointing elsewhere must stop the install: %v", r.err)
	}
	r.noOutput(t)
	files[AnswersFile] = caddyAnswers + KeyDNSCheck + "=skip\n"
	if r = runSetup(t, files, Options{Lookup: lookupTo("198.51.100.1")}); r.err != nil || !strings.Contains(r.out.String(), "Note: DNS") {
		t.Fatalf("skip: %v\n%s", r.err, r.out.String())
	}
	// Behind NAT the setup cannot tell; it goes on with a note.
	files = map[string]string{AnswersFile: caddyAnswers, HostAddrsFile: "10.0.0.5 172.17.0.1"}
	if r = runSetup(t, files, Options{Lookup: lookupTo("198.51.100.1")}); r.err != nil || !strings.Contains(r.out.String(), "cannot tell") {
		t.Fatalf("NAT: %v\n%s", r.err, r.out.String())
	}
	// No record at all.
	if r = runSetup(t, map[string]string{AnswersFile: caddyAnswers, HostAddrsFile: "203.0.113.9"}, Options{Lookup: lookupTo()}); r.err == nil || !strings.Contains(r.err.Error(), "does not resolve") {
		t.Fatalf("no record: %v", r.err)
	}
}

func TestCaddyModeNeedsADomainWithoutPort(t *testing.T) {
	for _, u := range []string{"https://203.0.113.9", "https://reader.example.org:8443", "http://127.0.0.1:8080", "https://localhost"} {
		r := runSetup(t, map[string]string{AnswersFile: strings.Replace(caddyAnswers, "https://reader.example.org", u, 1)}, Options{Lookup: lookupTo("203.0.113.9")})
		if r.err == nil || !strings.Contains(r.err.Error(), KeyPublicURL) {
			t.Errorf("%s in Caddy mode: %v", u, r.err)
		}
	}
}

func TestImageMustBePinned(t *testing.T) {
	for _, ref := range []string{"ghcr.io/iglace/abyssreader-server-kit", "ghcr.io/iglace/abyssreader-server-kit:latest", "localhost:5000/kit", "bad image", "x\nservices:"} {
		if CheckImage(ref) == nil {
			t.Errorf("%q must be refused", ref)
		}
	}
	for _, ref := range []string{testImage, "ghcr.io/iglace/abyssreader-server-kit:1.0.0-rc.1", "localhost:5000/kit:0.0.0-ci",
		"ghcr.io/iglace/abyssreader-server-kit@sha256:" + strings.Repeat("a", 64)} {
		if err := CheckImage(ref); err != nil {
			t.Errorf("%q: %v", ref, err)
		}
	}
	r := runSetup(t, map[string]string{AnswersFile: ownProxyAnswers}, Options{Image: "ghcr.io/iglace/abyssreader-server-kit:latest"})
	if r.err == nil {
		t.Fatal("latest must be refused")
	}
}

// interactive answers, one a line.
func interactive(lines ...string) Options {
	return Options{Interactive: true, In: strings.NewReader(strings.Join(lines, "\n") + "\n")}
}

func TestInteractiveFreshInstall(t *testing.T) {
	r := runSetup(t, nil, interactive(
		"reader.example.org",         // not a URL: asked again
		"https://Reader.Example.org", // 1
		"2", "",                      // 2: own proxy, port 8080
		"Lantern Library",                                             // 3
		"Lantern team", "team@example.org", "mailto:team@example.org", // 4: a bad contact, then a good one
		"http://example.org/privacy", "https://example.org/privacy", "", "", // links: one bad, then good; none; none
		"n",           // 5: sign-up closed
		"y",           // 6: an access key
		"y",           // 7: hosted-chapters
		"y",           // 7: reports
		"n",           // 7: account backup
		"n", "n", "n", // 7: comments, collections, stats
		"https://key@glitchtip.example.org/3", // 8
		"weekly", "12",                        // 9: backups
		"", // write: yes
	))
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out.String())
	}
	env := r.env(t)
	for k, want := range map[string]string{
		KeyPublicURL: "https://reader.example.org", KeyHTTPS: OwnProxy, KeyPort: "8080", KeyServerName: "Lantern Library",
		KeyContact: "mailto:team@example.org", KeyPrivacy: "https://example.org/privacy", KeySignUp: "false",
		KeyFeatures: "hosted-chapters,reports", KeyDSN: "https://key@glitchtip.example.org/3", KeyBackup: "weekly", KeyBackupKeep: "12",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	if len(env[KeyAccessKey]) != 43 {
		t.Errorf("access key %q", env[KeyAccessKey])
	}
	out := r.out.String()
	if strings.Contains(out, env[KeyAccessKey]) || strings.Contains(out, "key@glitchtip") {
		t.Errorf("the setup printed a secret:\n%s", out)
	}
	// The questions are numbered in setup order.
	last := -1
	for _, q := range []string{"1. Public address", "2. HTTPS", "3. Server name", "4. Operator details", "5. Sign-up", "6. Access key", "7. Optional features", "8. Error reporting"} {
		i := strings.Index(out, q)
		if i < last {
			t.Errorf("%q out of order", q)
		}
		last = i
	}
	if strings.Contains(strings.ToLower(out), "simple or advanced") {
		t.Error("the simple/advanced question is reserved for the advanced setup")
	}
}

// Configure, answering Enter everywhere, writes the same settings back: nothing is lost,
// no secret changes.
func TestInteractiveConfigureKeepsEverything(t *testing.T) {
	first := runSetup(t, map[string]string{CurrentFile: currentEnv, AnswersFile: "ABYSS_FEATURES=hosted-chapters\n"}, Options{})
	if first.err != nil {
		t.Fatal(first.err)
	}
	installed := first.file(t, ".env")
	r := runSetup(t, map[string]string{CurrentFile: installed}, interactive(strings.Split(strings.Repeat("\n", 30), "\n")...))
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out.String())
	}
	if got := r.file(t, ".env"); got != installed {
		t.Fatalf("Enter everywhere changed .env:\n--- before\n%s\n--- after\n%s", installed, got)
	}
	if strings.Contains(r.out.String(), "an-existing-access-key") || strings.Contains(r.out.String(), "0123abcd") {
		t.Errorf("printed a secret:\n%s", r.out.String())
	}
	if !strings.Contains(r.out.String(), "keep, new, remove") || !strings.Contains(r.out.String(), "[set: Enter keeps it") {
		t.Errorf("the secrets' questions:\n%s", r.out.String())
	}
}

func TestInteractiveRemoveAndCancel(t *testing.T) {
	// Answering "no" to writing changes nothing.
	r := runSetup(t, map[string]string{CurrentFile: currentEnv}, interactive("", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "n"))
	if !errors.Is(r.err, ErrCancelled) {
		t.Fatalf("want ErrCancelled: %v\n%s", r.err, r.out.String())
	}
	r.noOutput(t)
	// The end of the input cancels too.
	r = runSetup(t, nil, interactive("https://reader.example.org"))
	if !errors.Is(r.err, ErrCancelled) {
		t.Fatalf("EOF: %v", r.err)
	}
	r.noOutput(t)
}

func TestInteractiveCaddyDNSRetry(t *testing.T) {
	calls := 0
	lookup := func(_ context.Context, host string) ([]string, error) {
		calls++
		if host == "wrong.example.org" {
			return []string{"198.51.100.1"}, nil
		}
		return []string{"203.0.113.9"}, nil
	}
	r := runSetup(t, map[string]string{HostAddrsFile: "203.0.113.9"}, Options{Interactive: true, Lookup: lookup, In: strings.NewReader(strings.Join([]string{
		"https://wrong.example.org", "1", "n", // points elsewhere: not anyway
		"https://reader.example.org", "1", // points here
		"Lantern", "Team", "mailto:t@example.org", "", "", "", "", "", "", "", "", "", "", "", "", "", "", "",
	}, "\n") + "\n")})
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out.String())
	}
	if r.env(t)[KeyPublicURL] != "https://reader.example.org" || calls != 2 || !strings.Contains(r.out.String(), "points at this machine") {
		t.Fatalf("calls=%d\n%s", calls, r.out.String())
	}
}

func TestReadSecretIsUsedForTheDSN(t *testing.T) {
	used := false
	o := interactive("https://reader.example.org", "2", "", "Lantern", "Team", "mailto:t@example.org", "", "", "", "", "", "", "", "", "", "", "", "", "", "")
	o.ReadSecret = func() (string, error) { used = true; return "https://k@glitchtip.example.org/1", nil }
	r := runSetup(t, nil, o)
	if r.err != nil || !used || r.env(t)[KeyDSN] != "https://k@glitchtip.example.org/1" {
		t.Fatalf("%v used=%v\n%s", r.err, used, r.out.String())
	}
}

// Every server setting in .env.example is one the setup knows: asked, kept with a default,
// or fixed by the stack.
func TestEveryExampleSettingIsKnown(t *testing.T) {
	f, err := os.Open("../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	re := regexp.MustCompile(`^#?\s*(ABYSS_[A-Z0-9_]+)=`)
	seen := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := re.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		seen++
		k := m[1]
		if !isOther(k) && !contains(asked, k) && !contains(fixed, k) {
			t.Errorf(".env.example's %s is unknown to the setup", k)
		}
	}
	if seen < 20 {
		t.Fatalf("read only %d settings from .env.example", seen)
	}
	for _, k := range asked {
		if strings.HasPrefix(k, "ABYSS_INSTALL_") {
			continue
		}
		b, _ := os.ReadFile("../../.env.example")
		if !strings.Contains(string(b), k+"=") {
			t.Errorf("%s is not documented in .env.example", k)
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
