// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package errorreport sends the server's errors to a Sentry-protocol DSN (GlitchTip or Sentry)
// when the operator sets ABYSS_ERROR_REPORTING_DSN.
//
// What is sent is what the server logs at the error level: the message and the log line's
// attributes (a panic's stack included), nothing else. No request is ever attached, so no
// headers, bodies, query strings, cookies or client addresses; and every event passes Scrub
// before it leaves, which drops the user and request parts, breadcrumbs and the host name, and
// replaces every email address and IP address left in the text with [email] and [ip].
//
// The server contacts the DSN's host only when the operator set one, and only to send events.
package errorreport

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"regexp"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
)

// serverName replaces the host name in every event: the container's or machine's name is the
// operator's business.
const serverName = "abyssreader-server"

// Setup wraps base so that every error-level record is also sent to dsn. With no DSN it
// returns base itself and a flush that does nothing. flush waits (up to 5 s) for the events
// still being sent; call it before the program ends.
func Setup(dsn, version string, base slog.Handler) (h slog.Handler, flush func(), err error) {
	if dsn == "" {
		return base, func() {}, nil
	}
	r, err := newReporter(dsn, version, base)
	if err != nil {
		return nil, nil, err
	}
	return r.Handler, func() { r.Flush(5 * time.Second) }, nil
}

type reporter struct {
	Handler slog.Handler
	client  *sentry.Client
}

func newReporter(dsn, version string, base slog.Handler) (*reporter, error) {
	client, err := sentry.NewClient(sentry.ClientOptions{
		Dsn:            dsn,
		Release:        "abyssreader-server@" + version,
		ServerName:     serverName,
		SendDefaultPII: false,
		MaxBreadcrumbs: -1,
		BeforeSend:     func(ev *sentry.Event, _ *sentry.EventHint) *sentry.Event { return Scrub(ev) },
	})
	if err != nil {
		// The SDK's error may quote the DSN, which holds the project key.
		return nil, fmt.Errorf("ABYSS_ERROR_REPORTING_DSN: not a usable DSN (value not shown)")
	}
	return &reporter{Handler: &handler{next: base, client: client}, client: client}, nil
}

// Flush waits up to timeout for the events still being sent.
func (r *reporter) Flush(timeout time.Duration) bool { return r.client.Flush(timeout) }

// handler passes every record on to next and sends the error-level ones.
type handler struct {
	next   slog.Handler
	client *sentry.Client
	attrs  []slog.Attr // from WithAttrs, keys already prefixed
	prefix string      // from WithGroup: "group."
}

func (h *handler) Enabled(ctx context.Context, l slog.Level) bool { return h.next.Enabled(ctx, l) }

func (h *handler) Handle(ctx context.Context, r slog.Record) error {
	err := h.next.Handle(ctx, r)
	if r.Level >= slog.LevelError {
		fields := map[string]any{}
		for _, a := range h.attrs {
			fields[a.Key] = a.Value.Resolve().String()
		}
		r.Attrs(func(a slog.Attr) bool {
			fields[h.prefix+a.Key] = a.Value.Resolve().String()
			return true
		})
		ev := sentry.NewEvent()
		ev.Level = sentry.LevelError
		ev.Message = r.Message
		ev.Logger = "slog"
		ev.Fingerprint = []string{r.Message}
		if !r.Time.IsZero() {
			ev.Timestamp = r.Time
		}
		ev.Contexts["log"] = fields
		h.client.CaptureEvent(ev, nil, nil)
	}
	return err
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.next = h.next.WithAttrs(attrs)
	c.attrs = append([]slog.Attr(nil), h.attrs...)
	for _, a := range attrs {
		c.attrs = append(c.attrs, slog.Attr{Key: h.prefix + a.Key, Value: a.Value})
	}
	return &c
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.next = h.next.WithGroup(name)
	c.prefix = h.prefix + name + "."
	return &c
}

var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	// Candidates for IPv6 addresses (also with an embedded IPv4 address or a zone); only the
	// ones that parse as an address are replaced, so times such as 12:30:05 stay.
	ipv6Candidate = regexp.MustCompile(`(?i)[0-9a-f.]*:[0-9a-f:.]*(%[0-9a-z]+)?`)
	ipv4Pattern   = regexp.MustCompile(`\b\d{1,3}(\.\d{1,3}){3}\b`)
)

// ScrubString replaces every email address with [email] and every IP address with [ip].
func ScrubString(s string) string {
	s = emailPattern.ReplaceAllString(s, "[email]")
	s = ipv6Candidate.ReplaceAllStringFunc(s, func(m string) string {
		// A sentence may end right after an address ("from 2001:db8::1.").
		trimmed := strings.TrimRight(m, ".:")
		if a, err := netip.ParseAddr(trimmed); err == nil && a.Is6() {
			return "[ip]" + m[len(trimmed):]
		}
		return m
	})
	return ipv4Pattern.ReplaceAllStringFunc(s, func(m string) string {
		if _, err := netip.ParseAddr(m); err == nil {
			return "[ip]"
		}
		return m
	})
}

// Scrub removes from an event everything that could name a person: the user and request
// parts, the breadcrumbs and the host name go, and every string left (message, exception
// values, tags, contexts, fingerprint) passes ScrubString. It runs on every event just before
// it is sent.
func Scrub(ev *sentry.Event) *sentry.Event {
	ev.User = sentry.User{}
	ev.Request = nil
	ev.Breadcrumbs = nil
	ev.ServerName = serverName
	ev.Message = ScrubString(ev.Message)
	ev.Transaction = ScrubString(ev.Transaction)
	for i := range ev.Exception {
		ev.Exception[i].Value = ScrubString(ev.Exception[i].Value)
	}
	for i := range ev.Fingerprint {
		ev.Fingerprint[i] = ScrubString(ev.Fingerprint[i])
	}
	for k, v := range ev.Tags {
		ev.Tags[k] = ScrubString(v)
	}
	for name, c := range ev.Contexts {
		for k, v := range c {
			c[k] = scrubValue(v)
		}
		ev.Contexts[name] = c
	}
	return ev
}

func scrubValue(v any) any {
	switch x := v.(type) {
	case string:
		return ScrubString(x)
	case map[string]any:
		for k, e := range x {
			x[k] = scrubValue(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = scrubValue(e)
		}
		return x
	case []string:
		for i, e := range x {
			x[i] = ScrubString(e)
		}
		return x
	}
	return v
}
