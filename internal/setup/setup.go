// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package setup is the installer's question-and-answer setup. It
// runs inside the kit image (`abyssreader-server setup`), started by install.sh, so the
// questions and their checks are the same on every host and are the server's own checks.
//
// install.sh mounts a work folder and the setup reads from it:
//
//	current.env      the installed .env, when there is one (install.sh configure)
//	answers.env      an answers file: the setup then asks nothing (scripted installs)
//	host-addresses   this machine's IP addresses, for the Caddy mode's DNS check
//
// and writes into out/ (the files install.sh then puts in /opt/abyssreader-server/):
//
//	.env             every setting, secrets included (mode 0600)
//	compose.yaml     the stack, the image pinned to an exact tag
//	Caddyfile        Caddy mode only
//	install.info     what install.sh needs to know, nothing secret
//
// The setup never prints a secret, and never makes a new access key unless asked to.
package setup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/IGlace/abyssreader-server-kit/internal/config"
)

// File names in the work folder.
const (
	CurrentFile   = "current.env"
	AnswersFile   = "answers.env"
	HostAddrsFile = "host-addresses"
	OutDir        = "out"
)

// ErrCancelled is returned when the operator stops the setup (no, or end of input).
var ErrCancelled = errors.New("setup cancelled: nothing was changed")

// ErrInterrupted is returned when the setup is interrupted (Ctrl-C, SIGHUP, SIGTERM).
var ErrInterrupted = errors.New("setup interrupted: nothing was changed")

// Options configure one run.
type Options struct {
	WorkDir    string
	Image      string // the server image compose.yaml pins
	KitVersion string
	// Interactive is true when the setup may ask questions (a terminal). Without an answers
	// file and without a terminal the setup refuses to run.
	Interactive bool
	In          io.Reader
	Out         io.Writer
	// ReadSecret reads one line without echoing it (a terminal); nil reads it from In.
	ReadSecret func() (string, error)
	// Lookup resolves names for the DNS check; nil uses the system resolver.
	Lookup LookupFunc
}

// Run runs the setup.
func Run(ctx context.Context, o Options) error {
	if err := CheckImage(o.Image); err != nil {
		return fmt.Errorf("--image: %v", err)
	}
	if o.Lookup == nil {
		o.Lookup = net.DefaultResolver.LookupHost
	}
	if ctx.Err() != nil {
		return ErrInterrupted
	}
	current, err := readOptional(filepath.Join(o.WorkDir, CurrentFile))
	if err != nil {
		return fmt.Errorf("the installed .env cannot be read: %v", err)
	}
	answersFile, err := readOptional(filepath.Join(o.WorkDir, AnswersFile))
	if err != nil {
		return fmt.Errorf("the answers file cannot be read: %v", err)
	}
	hostAddrs, err := os.ReadFile(filepath.Join(o.WorkDir, HostAddrsFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	s := &session{o: o, ctx: ctx, fresh: current == nil, hostAddrs: parseHostAddresses(string(hostAddrs))}
	s.a = defaults()
	if current != nil {
		s.a = fromEnv(current)
	}
	oldKey := s.a.AccessKey

	// rest: explicit settings from the installed file that the setup does not ask about.
	rest := map[string]string{}
	for k, v := range current {
		if !slices.Contains(asked, k) {
			rest[k] = v
		}
	}

	switch {
	case answersFile != nil:
		if err := s.apply(answersFile, rest); err != nil {
			return err
		}
	case !o.Interactive:
		return errors.New("no answers file and no terminal: run install.sh in a terminal to answer the questions,\n" +
			"or give it an answers file (install.sh --answers FILE)")
	default:
		if err := s.ask(); err != nil {
			return err
		}
	}

	if ctx.Err() != nil {
		return ErrInterrupted
	}
	if p := s.a.problems(); len(p) > 0 {
		return problemsError(p)
	}
	env := s.a.env()
	for k, v := range rest {
		env[k] = v
	}
	// The server's own check of the whole file: the setup never writes a .env the server
	// would refuse (a hand-edited setting it does not ask about included).
	if _, err := config.Load(func(k string) string { return env[k] }); err != nil {
		return fmt.Errorf("the server would refuse these settings (fix the ones it names in the installed .env\n"+
			"or the answers file, then run the setup again):\n%v", err)
	}

	envText, err := renderEnv(s.a, rest)
	if err != nil {
		return err
	}
	compose, err := renderCompose(s.a, o.Image, o.KitVersion)
	if err != nil {
		return err
	}
	out := filepath.Join(o.WorkDir, OutDir)
	if err := os.MkdirAll(out, 0o700); err != nil {
		return err
	}
	files := []struct {
		name, text string
		mode       os.FileMode
	}{
		{".env", envText, 0o600},
		{"compose.yaml", compose, 0o644},
		{"install.info", renderInfo(s.a, s.a.AccessKey != oldKey && s.a.AccessKey != ""), 0o644},
	}
	if s.a.HTTPS == Caddy {
		files = append(files, struct {
			name, text string
			mode       os.FileMode
		}{"Caddyfile", renderCaddyfile(s.a), 0o644})
	}
	for _, f := range files {
		if err := writeFile(filepath.Join(out, f.name), f.text, f.mode); err != nil {
			return err
		}
	}
	s.summary()
	return nil
}

// writeFile writes a new file with exactly mode (whatever the umask), replacing an old one.
func writeFile(path, text string, mode os.FileMode) error {
	_ = os.Remove(path)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func readOptional(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadEnv(f)
}

func problemsError(p []string) error {
	return fmt.Errorf("these answers are not valid (nothing was changed):\n  %s", strings.Join(p, "\n  "))
}

type session struct {
	o         Options
	ctx       context.Context
	fresh     bool
	a         Answers
	hostAddrs []netip.Addr
	notes     []string // printed in the summary
	in        *bufio.Reader
}

func (s *session) printf(format string, args ...any) { fmt.Fprintf(s.o.Out, format, args...) }

// apply applies an answers file (non-interactive). A setting the file lists replaces the
// current one (empty clears an optional one); a setting it does not list keeps its current
// value, or its default on a fresh install. ABYSS_ACCESS_KEY takes "generate" (make one when
// there is none, keep the current one otherwise), "new" (always make a new one), empty (none)
// or the key itself.
func (s *session) apply(file map[string]string, rest map[string]string) error {
	var problems []string
	bad := func(key, msg string) { problems = append(problems, key+": "+msg) }
	skipDNS := false
	for k, raw := range file {
		v := strings.TrimSpace(raw)
		switch k {
		case KeyPublicURL:
			s.a.PublicURL = v
		case KeyHTTPS:
			s.a.HTTPS = strings.ToLower(v)
		case KeyPort:
			n, err := strconv.Atoi(v)
			if err != nil {
				bad(k, "must be a port number from 1 to 65535")
			}
			s.a.Port = n
		case KeyServerName:
			s.a.ServerName = v
		case KeyOperatorName:
			s.a.OperatorName = v
		case KeyContact:
			s.a.OperatorContact = v
		case KeyPrivacy:
			s.a.PrivacyURL = v
		case KeyTerms:
			s.a.TermsURL = v
		case KeyCopyright:
			s.a.CopyrightURL = v
		case KeySignUp:
			b, ok := parseYesNo(v)
			if !ok {
				bad(k, "must be true or false (yes or no)")
			}
			s.a.SignUpOpen = b
		case KeyAccessKey:
			switch v {
			case "generate":
				if s.a.AccessKey == "" {
					if err := s.newKey(); err != nil {
						return err
					}
				}
			case "new":
				if err := s.newKey(); err != nil {
					return err
				}
			default:
				s.a.AccessKey = v
			}
		case KeyFeatures:
			s.a.Features = splitList(v)
		case KeyDSN:
			s.a.DSN = v
		case KeyBackup:
			s.a.BackupSchedule = strings.ToLower(v)
		case KeyBackupKeep:
			n, err := strconv.Atoi(v)
			if err != nil {
				bad(k, fmt.Sprintf("must be a whole number from 1 to %d", config.MaxBackupKeep))
			}
			s.a.BackupKeep = n
		case KeyDNSCheck:
			switch strings.ToLower(v) {
			case "skip":
				skipDNS = true
			case "", "check":
			default:
				bad(k, "must be check or skip")
			}
		default:
			switch {
			case isOther(k):
				rest[k] = raw
			case slices.Contains(fixed, k):
				bad(k, "set by the installed stack (the /data volume, port 8080 in the container); an answers file cannot change it")
			default:
				bad(k, "not a setting of this kit version (a typo?)")
			}
		}
	}
	// The checks of the answers themselves, for every setting not already named above.
	for _, p := range s.a.problems() {
		key, _, _ := strings.Cut(p, ":")
		if !slices.ContainsFunc(problems, func(q string) bool { return strings.HasPrefix(q, key+":") }) {
			problems = append(problems, p)
		}
	}
	if len(problems) > 0 {
		slices.SortStableFunc(problems, func(x, y string) int { return keyOrder(x) - keyOrder(y) })
		return problemsError(problems)
	}
	if s.a.HTTPS == Caddy {
		verdict, msg := checkDNS(s.ctx, s.o.Lookup, s.a.Host(), s.hostAddrs)
		switch {
		case verdict == dnsPointsHere:
		case verdict == dnsCannotTell || skipDNS:
			s.notes = append(s.notes, "DNS: "+msg)
		default:
			return fmt.Errorf("%s\nSet %s=skip in the answers file to install anyway.", msg, KeyDNSCheck)
		}
	}
	return nil
}

func (s *session) newKey() error {
	k, err := newAccessKey()
	if err != nil {
		return fmt.Errorf("making an access key: %v", err)
	}
	s.a.AccessKey = k
	return nil
}

func parseYesNo(v string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "y", "yes", "true", "1", "on":
		return true, true
	case "n", "no", "false", "0", "off":
		return false, true
	}
	return false, false
}

// summary prints the answers without the secrets.
func (s *session) summary() {
	a := s.a
	s.printf("\nThe settings:\n")
	s.printf("  Public address:   %s\n", a.PublicURL)
	if a.HTTPS == Caddy {
		s.printf("  HTTPS:            Caddy (Let's Encrypt certificate for %s)\n", a.Host())
	} else {
		s.printf("  HTTPS:            your own proxy, forwarding to http://127.0.0.1:%d\n", a.Port)
	}
	s.printf("  Server name:      %s\n", a.ServerName)
	s.printf("  Operator:         %s, %s\n", a.OperatorName, a.OperatorContact)
	for _, l := range []struct{ name, v string }{{"Privacy page", a.PrivacyURL}, {"Terms page", a.TermsURL}, {"Copyright page", a.CopyrightURL}} {
		if l.v != "" {
			s.printf("  %-17s %s\n", l.name+":", l.v)
		}
	}
	s.printf("  Sign-up:          %s\n", map[bool]string{true: "open", false: "closed"}[a.SignUpOpen])
	if a.AccessKey != "" {
		s.printf("  Access key:       yes (in .env, not shown)\n")
	} else {
		s.printf("  Access key:       none\n")
	}
	if len(a.Features) > 0 {
		s.printf("  Optional features: %s\n", strings.Join(a.Features, ", "))
	} else {
		s.printf("  Optional features: none\n")
	}
	s.printf("  Error reporting:  %s\n", map[bool]string{true: "on (DSN in .env, not shown)", false: "off"}[a.DSN != ""])
	if s.a.BackupSchedule == "off" {
		s.printf("  Backups:          manual only, keep %d.\n", s.a.BackupKeep)
	} else {
		s.printf("  Backups:          %s, keep %d (manual backups also count).\n", s.a.BackupSchedule, s.a.BackupKeep)
	}
	s.printf("  %s\n", backupDiskNote)
	for _, n := range s.notes {
		s.printf("\nNote: %s\n", n)
	}
}
