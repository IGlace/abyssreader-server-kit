// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// net.Pipe gives the HTTP server real blocking reads, writes and deadlines.
// Its lack of a kernel buffer makes client backpressure deterministic, and
// synctest advances the production timeouts without spending minutes in CI.
type backupListener struct {
	conn     net.Conn
	accepted bool
	closed   chan struct{}
	once     sync.Once
}

func (l *backupListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}
func (l *backupListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *backupListener) Addr() net.Addr { return l.conn.LocalAddr() }

func backupHTTP(t *testing.T, h http.HandlerFunc) (net.Conn, <-chan struct{}) {
	t.Helper()
	server, client := net.Pipe()
	listener := &backupListener{conn: server, closed: make(chan struct{})}
	closed := make(chan struct{})
	srv := http.Server{Handler: h, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second,
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				close(closed)
			}
		},
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(listener) }()
	t.Cleanup(func() { client.Close(); srv.Close(); <-done })
	return client, closed
}

func TestBackupUploadDeadlines(t *testing.T) {
	for _, mode := range []string{"progress", "stall", "trickle"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const chunk = 62500 * 10 // ten seconds at 0.5 Mbit/s
				const size = 20 << 20    // the default maximum file size
				result := make(chan error, 1)
				c, _ := backupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
					w = setupBackupDeadlines(&statusWriter{ResponseWriter: w}, r, size)
					n, err := io.Copy(io.Discard, r.Body)
					if err == nil && n != size {
						err = io.ErrUnexpectedEOF
					}
					result <- err
					if err == nil {
						time.Sleep(70 * time.Second) // database work, past WriteTimeout
						_, _ = io.WriteString(w, "restored")
					}
				})
				fmt.Fprintf(c, "POST / HTTP/1.1\r\nHost: kit\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", size)
				start := time.Now()
				switch mode {
				case "progress":
					p := make([]byte, chunk)
					for sent := 0; sent < size; sent += chunk {
						n := min(size-sent, len(p))
						time.Sleep(time.Duration(n) * time.Second / 62_500)
						if _, err := c.Write(p[:n]); err != nil {
							t.Fatal(err)
						}
					}
					if err := <-result; err != nil {
						t.Fatal(err)
					}
					resp, err := http.ReadResponse(bufio.NewReader(c), nil)
					if err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
					body, err := io.ReadAll(resp.Body)
					if err != nil || string(body) != "restored" {
						t.Fatalf("restore response %q: %v", body, err)
					}
				case "stall":
					if err := <-result; !errors.Is(err, os.ErrDeadlineExceeded) || time.Since(start) != 20*time.Second {
						t.Fatalf("stalled upload ended after %v: %v", time.Since(start), err)
					}
				case "trickle":
					// Keep the idle deadline moving through t=350 s. A full
					// 20 MiB upload at the minimum rate gets 336 s + 20 s grace.
					for range 35 {
						time.Sleep(10 * time.Second)
						if _, err := c.Write([]byte{1}); err != nil {
							t.Fatal(err)
						}
					}
					err := <-result
					elapsed := time.Since(start)
					if !errors.Is(err, os.ErrDeadlineExceeded) || elapsed != 356*time.Second {
						t.Fatalf("trickling upload ended after %v: %v", elapsed, err)
					}
				}
			})
		})
	}
}

func TestBackupExportDeadlines(t *testing.T) {
	for _, mode := range []string{"progress", "stall"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				payload := bytes.Repeat([]byte("a"), 160<<10)
				result := make(chan error, 1)
				c, _ := backupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
					w = setupBackupDeadlines(&statusWriter{ResponseWriter: w}, r, 0)
					time.Sleep(75 * time.Second) // prepare the first export page
					_, err := w.Write(payload)
					result <- err
				})
				io.WriteString(c, "GET / HTTP/1.1\r\nHost: kit\r\nConnection: close\r\n\r\n")
				start := time.Now()
				if mode == "stall" {
					if err := <-result; !errors.Is(err, os.ErrDeadlineExceeded) || time.Since(start) != 95*time.Second {
						t.Fatalf("stalled export ended after %s: %v", time.Since(start), err)
					}
					return
				}
				resp, err := http.ReadResponse(bufio.NewReader(c), nil)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				got := make([]byte, len(payload))
				for offset := 0; offset < len(got); offset += 16 << 10 {
					time.Sleep(8 * time.Second)
					if _, err := io.ReadFull(resp.Body, got[offset:offset+(16<<10)]); err != nil {
						t.Fatal(err)
					}
				}
				if extra, err := io.ReadAll(resp.Body); err != nil || len(extra) != 0 {
					t.Fatalf("finishing download: %d extra bytes, %v", len(extra), err)
				}
				if err := <-result; err != nil || !bytes.Equal(got, payload) {
					t.Fatalf("download mismatch: %v", err)
				}
			})
		})
	}
}

func TestBackupDeadlineCoversFinalHTTPFlush(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, closed := backupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
			w = setupBackupDeadlines(w, r, 0)
			defer finishBackupDeadlines(w)
			_, _ = io.WriteString(w, "x")
		})
		io.WriteString(c, "GET / HTTP/1.1\r\nHost: kit\r\nConnection: close\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Fatal(err)
		}
		var payload [1]byte
		if _, err := io.ReadFull(resp.Body, payload[:]); err != nil || payload[0] != 'x' {
			t.Fatalf("payload %q: %v", payload, err)
		}
		// Stop reading before the terminating chunk, written by net/http after
		// the handler returns. The server must release this connection by itself.
		start := time.Now()
		select {
		case <-closed:
			if elapsed := time.Since(start); elapsed != 20*time.Second {
				t.Fatalf("connection closed in %s", elapsed)
			}
		case <-time.After(25 * time.Second):
			t.Fatal("server still blocked on the final HTTP flush")
		}
	})
}
