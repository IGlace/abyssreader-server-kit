// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/accounts"
)

type ctxKey int

const callerKey ctxKey = 0

// caller is the signed-in account of a request, when its token was accepted.
type caller struct {
	account   accounts.Account
	sessionID int64
}

func callerOf(r *http.Request) (caller, bool) {
	c, ok := r.Context().Value(callerKey).(caller)
	return c, ok
}

// accessKey requires X-Access-Key on every route it wraps when the operator set one: 403 access_key_required when it is missing, 403 access_key_invalid when it is wrong.
// Neither ever means "signed out". A server without a key ignores the header.
func (s *server) accessKey(next http.Handler) http.Handler {
	if s.Config.AccessKey == "" {
		return next
	}
	want := sha256.Sum256([]byte(s.Config.AccessKey))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("X-Access-Key")
		if got == "" {
			writeError(w, http.StatusForbidden, "access_key_required", "This server needs an access key.")
			return
		}
		// Hashing both sides first keeps the comparison constant-time whatever the lengths.
		have := sha256.Sum256([]byte(got))
		if subtle.ConstantTimeCompare(have[:], want[:]) != 1 {
			writeError(w, http.StatusForbidden, "access_key_invalid", "The access key is not right.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// session reads `Authorization: Bearer <token>`. A request without the header goes on
// anonymously; a header that is not an accepted token gets 401 session_invalid on every route,
// public ones included: the server never quietly serves it anonymously.
func (s *server) session(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		if len(values) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		// The scheme is case-insensitive (RFC 9110 §11.1).
		scheme, token, _ := strings.Cut(strings.TrimSpace(values[0]), " ")
		token = strings.TrimSpace(token)
		if len(values) != 1 || !strings.EqualFold(scheme, "Bearer") || token == "" {
			writeSessionInvalid(w)
			return
		}
		account, sessionID, err := s.Accounts.Authenticate(r.Context(), token)
		if errors.Is(err, accounts.ErrSessionInvalid) {
			writeSessionInvalid(w)
			return
		}
		if err != nil {
			s.storeFailed(w, "session lookup", err)
			return
		}
		ctx := context.WithValue(r.Context(), callerKey, caller{account: account, sessionID: sessionID})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeSessionInvalid(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="abyssreader"`)
	writeError(w, http.StatusUnauthorized, "session_invalid", "Your session has ended. Sign in again.")
}

// signedIn lets only requests with an accepted session token through.
func (s *server) signedIn(h func(http.ResponseWriter, *http.Request, caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, ok := callerOf(r)
		if !ok {
			writeSessionInvalid(w)
			return
		}
		h(w, r, c)
	}
}

// statusWriter remembers the status for the request log.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// logRequests writes one readable line per request to stdout: method, path, status and time.
// It never logs IP addresses, query strings, headers or bodies.
func (s *server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		s.Logger.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
	})
}

// recoverPanics turns a panic into a 500 server_error and logs its stack.
func (s *server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				s.Logger.Error("panic", "value", v, "stack", string(debug.Stack()))
				writeServerError(w)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
