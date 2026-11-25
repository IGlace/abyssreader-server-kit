// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package setup

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/IGlace/abyssreader-server-kit/internal/config"
)

const backupDiskNote = "Backups are on the same disk as the server. They protect against failed upgrades. For disk loss, copy backups off-machine as described in docs/install.md."

// featureHelp describes each optional feature this kit version has, for its question.
var featureHelp = map[string]string{
	"backup":              "signed-in readers can download and restore their own library",
	"reports":             "readers can report a title for you to review with admin reports",
	"comments":            "readers can comment, reply, vote and report comments on titles",
	"collections":         "readers can share title lists and comment, vote and report on them",
	"stats":               "readers can see their own statistics; anyone can see the rankings with scorer names and avatars",
	config.HostedChapters: "chapters and pages this server stores itself, imported with admin import",
}

// ask prompts in setup order, each with the current answer as
// its default (Enter keeps it). Every answer is checked at once with the server's rules and
// asked again when it does not pass.
func (s *session) ask() error {
	r := bufio.NewReader(s.o.In)
	s.in = r
	if s.fresh {
		s.printf("AbyssReader server setup (kit %s).\nPress Enter to take the answer in [brackets]; Ctrl-C cancels without changing anything.\n", s.o.KitVersion)
	} else {
		s.printf("AbyssReader server setup (kit %s): changing the installed settings.\n"+
			"Press Enter to keep the answer in [brackets]; the settings not asked about are kept.\n"+
			"Ctrl-C cancels without changing anything.\n", s.o.KitVersion)
	}

	// 1–2: the public address and HTTPS, checked together (Caddy needs a domain name).
	for {
		s.printf("\n1. Public address\n   The address users type in the app, such as https://reader.example.org.\n")
		v, err := s.line("Public address", s.a.PublicURL, func(v string) error {
			_, err := config.NormalizePublicURL(v)
			return err
		})
		if err != nil {
			return err
		}
		s.a.PublicURL, _ = config.NormalizePublicURL(v)

		s.printf("\n2. HTTPS\n" +
			"   1) Caddy: a Caddy container gets a Let's Encrypt certificate by itself. The address's name must\n" +
			"      point at this machine, and ports 80 and 443 must be open.\n" +
			"   2) My own proxy: the server listens on 127.0.0.1 only; your nginx, Cloudflare Tunnel or other\n" +
			"      proxy handles HTTPS and forwards to it.\n")
		def := "1"
		if s.a.HTTPS == OwnProxy {
			def = "2"
		}
		v, err = s.line("HTTPS (1 or 2)", def, func(v string) error {
			if v != "1" && v != "2" {
				return errors.New("type 1 or 2")
			}
			return nil
		})
		if err != nil {
			return err
		}
		s.a.HTTPS = map[string]string{"1": Caddy, "2": OwnProxy}[v]
		if s.a.HTTPS == OwnProxy {
			v, err = s.line("Port on 127.0.0.1 for your proxy", strconv.Itoa(s.a.Port), func(v string) error {
				n, err := strconv.Atoi(v)
				if err != nil {
					return errors.New("must be a port number from 1 to 65535")
				}
				return checkPort(n)
			})
			if err != nil {
				return err
			}
			s.a.Port, _ = strconv.Atoi(v)
		}
		if _, err := checkPublicURL(s.a.PublicURL, s.a.HTTPS); err != nil {
			s.printf("   %v. Choose another address or the other HTTPS mode.\n", err)
			continue
		}
		if s.a.HTTPS == OwnProxy && strings.HasPrefix(s.a.PublicURL, "http://") {
			s.printf("   Note: http:// is for local testing only; the app accepts only https:// servers.\n")
		}
		if s.a.HTTPS == Caddy {
			verdict, msg := checkDNS(s.ctx, s.o.Lookup, s.a.Host(), s.hostAddrs)
			if verdict != dnsPointsHere {
				s.printf("\n   %s\n", strings.ReplaceAll(msg, "\n", "\n   "))
				ok, err := s.yesNo("Use this address anyway (Caddy keeps trying until it works)", verdict == dnsCannotTell)
				if err != nil {
					return err
				}
				if !ok {
					continue
				}
				s.notes = append(s.notes, "DNS: "+msg)
			} else {
				s.printf("   %s points at this machine.\n", s.a.Host())
			}
		}
		break
	}

	s.printf("\n3. Server name\n   Shown in the app (1 to %d characters).\n", config.MaxName)
	var err error
	if s.a.ServerName, err = s.line("Server name", s.a.ServerName, config.CheckName); err != nil {
		return err
	}

	s.printf("\n4. Operator details\n   Who runs this server, shown in the app. The links are optional (https:// only).\n")
	if s.a.OperatorName, err = s.line("Your name or your team's", s.a.OperatorName, config.CheckName); err != nil {
		return err
	}
	if s.a.OperatorContact, err = s.line("Contact (mailto:you@example.org or an https:// link)", s.a.OperatorContact, config.CheckContact); err != nil {
		return err
	}
	for _, l := range []struct {
		name string
		v    *string
	}{{"Privacy policy link", &s.a.PrivacyURL}, {"Terms link", &s.a.TermsURL}, {"Copyright (takedown) link", &s.a.CopyrightURL}} {
		if *l.v, err = s.optional(l.name, *l.v, config.CheckLink); err != nil {
			return err
		}
	}

	s.printf("\n5. Sign-up\n")
	if s.a.SignUpOpen, err = s.yesNo("Can anyone create an account (open sign-up)", s.a.SignUpOpen); err != nil {
		return err
	}

	s.printf("\n6. Access key\n   With an access key, users type it when they add your server; without one, anyone can use it.\n")
	if s.a.AccessKey == "" {
		want, err := s.yesNo("Require an access key (the setup makes a random one)", false)
		if err != nil {
			return err
		}
		if want {
			if err := s.newKey(); err != nil {
				return err
			}
		}
	} else {
		v, err := s.line("This server has an access key: keep it, make a new one, or remove it (keep, new, remove)", "keep", func(v string) error {
			if !slices.Contains([]string{"keep", "new", "remove"}, strings.ToLower(v)) {
				return errors.New("type keep, new or remove")
			}
			return nil
		})
		if err != nil {
			return err
		}
		switch strings.ToLower(v) {
		case "new":
			s.printf("   Everyone who uses the old key will have to type the new one.\n")
			if err := s.newKey(); err != nil {
				return err
			}
		case "remove":
			s.a.AccessKey = ""
		}
	}

	s.printf("\n7. Optional features\n   All are off by default. Switching one off later hides it and keeps its data.\n")
	var on []string
	for _, f := range config.Built {
		yes, err := s.yesNo(fmt.Sprintf("Switch on %s (%s)", f, featureHelp[f]), slices.Contains(s.a.Features, f))
		if err != nil {
			return err
		}
		if yes {
			on = append(on, f)
		}
	}
	s.a.Features = on

	s.printf("\n8. Error reporting\n" +
		"   A GlitchTip or Sentry DSN (https://KEY@host/PROJECT) the server sends its errors to, without\n" +
		"   emails, IP addresses or request bodies. Optional; the typing is not shown.\n")
	if err := s.dsn(); err != nil {
		return err
	}

	s.printf("\n9. Backups\n   %s\n", backupDiskNote)
	schedule, err := s.line("Snapshot schedule (daily, weekly or off)", s.a.BackupSchedule, config.CheckBackupSchedule)
	if err != nil {
		return err
	}
	s.a.BackupSchedule = schedule
	if schedule != "off" {
		keep, err := s.line("Complete backups to keep", strconv.Itoa(s.a.BackupKeep), func(v string) error {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > config.MaxBackupKeep {
				return fmt.Errorf("enter a number from 1 to %d", config.MaxBackupKeep)
			}
			return nil
		})
		if err != nil {
			return err
		}
		s.a.BackupKeep, _ = strconv.Atoi(keep)
	}
	s.summary()
	ok, err := s.yesNo("\nWrite these settings and start the server", true)
	if err != nil {
		return err
	}
	if !ok {
		return ErrCancelled
	}
	return nil
}

// readLine reads one answer; the end of the input cancels the setup.
func (s *session) readLine() (string, error) {
	v, err := s.wait(func() (string, error) { return s.in.ReadString('\n') })
	if errors.Is(err, ErrInterrupted) {
		return "", err
	}
	if err != nil && (v == "" || !errors.Is(err, io.EOF)) {
		if errors.Is(err, io.EOF) {
			return "", ErrCancelled
		}
		return "", err
	}
	return strings.TrimSpace(v), nil
}

// line asks for a required answer; Enter takes def when there is one.
func (s *session) line(prompt, def string, check func(string) error) (string, error) {
	for {
		if def != "" {
			s.printf("   %s [%s]: ", prompt, def)
		} else {
			s.printf("   %s: ", prompt)
		}
		v, err := s.readLine()
		if err != nil {
			return "", err
		}
		if v == "" {
			v = def
		}
		if err := check(v); err != nil {
			s.printf("   %v\n", err)
			continue
		}
		return v, nil
	}
}

// optional asks for an optional answer: Enter keeps the current one, "-" removes it.
func (s *session) optional(prompt, cur string, check func(string) error) (string, error) {
	for {
		if cur != "" {
			s.printf("   %s [%s] (- for none): ", prompt, cur)
		} else {
			s.printf("   %s (Enter for none): ", prompt)
		}
		v, err := s.readLine()
		if err != nil {
			return "", err
		}
		switch v {
		case "":
			return cur, nil
		case "-":
			return "", nil
		}
		if err := check(v); err != nil {
			s.printf("   %v\n", err)
			continue
		}
		return v, nil
	}
}

func (s *session) yesNo(prompt string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		s.printf("   %s? [%s]: ", prompt, hint)
		v, err := s.readLine()
		if err != nil {
			return false, err
		}
		if v == "" {
			return def, nil
		}
		if b, ok := parseYesNo(v); ok {
			return b, nil
		}
		s.printf("   Type y or n.\n")
	}
}

// dsn asks for the error-reporting DSN without echoing it, and never shows the current one.
func (s *session) dsn() error {
	for {
		if s.a.DSN != "" {
			s.printf("   Error-reporting DSN [set: Enter keeps it, - removes it]: ")
		} else {
			s.printf("   Error-reporting DSN (Enter for none): ")
		}
		var v string
		var err error
		if s.o.ReadSecret != nil {
			v, err = s.wait(s.o.ReadSecret)
			s.printf("\n")
			v = strings.TrimSpace(v)
		} else {
			v, err = s.readLine()
		}
		if errors.Is(err, io.EOF) {
			return ErrCancelled
		}
		if err != nil {
			return err
		}
		switch v {
		case "":
			return nil
		case "-":
			s.a.DSN = ""
			return nil
		}
		if err := config.CheckErrorReportingDSN(v); err != nil {
			s.printf("   %v\n", err)
			continue
		}
		s.a.DSN = v
		return nil
	}
}

// wait runs a blocking read and gives up when the setup is interrupted (Ctrl-C, a closed SSH
// session, docker stop): the read itself cannot be stopped, but the setup returns at once
// and the process ends.
func (s *session) wait(read func() (string, error)) (string, error) {
	if s.ctx.Err() != nil {
		return "", ErrInterrupted
	}
	type answer struct {
		v   string
		err error
	}
	got := make(chan answer, 1)
	go func() {
		v, err := read()
		got <- answer{v, err}
	}()
	select {
	case <-s.ctx.Done():
		return "", ErrInterrupted
	case a := <-got:
		return a.v, a.err
	}
}
