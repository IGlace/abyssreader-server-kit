// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/accounts"
	"github.com/IGlace/abyssreader-server-kit/internal/backup"
	"github.com/IGlace/abyssreader-server-kit/internal/store"
	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

// Use the real handlers, SQLite, and the HTTP/1 server's 30s ReadTimeout /
// 60s WriteTimeout. Enough varied chapter URLs keep two readers busy past the
// initial export allowance while still fitting in a small test database.
func backupTransportServer(t *testing.T) *server {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "kit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`
	INSERT INTO users(id,email,email_key,display_name,created_at,updated_at)
	 VALUES(1,'a','a','A',0,0),(2,'b','b','B',0,0),(3,'c','c','C',0,0);
	INSERT INTO sources(id,slug,name,website,lang,hosted,created_at,updated_at)
	 VALUES(1,'moon','Paper Moon','https://moon.example','en',0,0,0);
	INSERT INTO titles(id,ref,slug,name,name_key,status,manifest_hash,created_at,updated_at)
	 VALUES(1,'t','t','Tide','tide','unknown','',0,0);
	INSERT INTO source_titles(id,title_id,source_id,url,lang,position) VALUES(1,1,1,'/tide','en',0);
	WITH RECURSIVE marks(i) AS (VALUES (1) UNION ALL SELECT i+1 FROM marks WHERE i<512)
	INSERT INTO sync_records(user_id,kind,key_title,source_slug,chapter_url,url_hash,title_id,data,client_updated_at,deleted,seq)
	SELECT users.id,'chapterMark',0,'moon','/'||hex(randomblob(1000)),randomblob(32),1,'{"read":true}',1,0,i FROM marks,users WHERE users.id<=2;`)
	if err != nil {
		t.Fatal(err)
	}
	return &server{Options: Options{DB: db, Config: testConfig(), Logger: slog.New(slog.DiscardHandler)}, sync: usersync.New(db), backups: newBackupLimits()}
}

func startTransportExport(t *testing.T, s *server, id int64, inheritedReadTimeout time.Duration) net.Conn {
	t.Helper()
	c, _ := backupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if inheritedReadTimeout > 0 {
			// A server middleware may leave a deadline on the active background read.
			if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(inheritedReadTimeout)); err != nil {
				t.Error(err)
			}
		}
		s.exportBackup(&statusWriter{ResponseWriter: w}, r, caller{account: accounts.Account{ID: id}})
	})
	if _, err := io.WriteString(c, "GET "+backupPath+" HTTP/1.1\r\nHost: kit\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	return c
}

func transportResponse(t *testing.T, c net.Conn) *http.Response {
	t.Helper()
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("backup status %d", resp.StatusCode)
	}
	return resp
}

func TestExportQueryAndGzipSurviveServerReadTimeout(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		inheritedReadTimeout time.Duration
	}{{"server_defaults", 0}, {"inherited_read_deadline", 30 * time.Second}} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := backupTransportServer(t)
				hold, err := s.DB.Conn(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer hold.Close()
				start := time.Now()
				c := startTransportExport(t, s, 1, tc.inheritedReadTimeout)
				synctest.Wait()
				if s.DB.Stats().WaitCount == 0 {
					t.Fatal("export did not wait for its first database query")
				}
				time.Sleep(178 * time.Second)
				hold.Close()
				resp := transportResponse(t, c)
				defer resp.Body.Close()
				var compressed bytes.Buffer
				var chunk [6250]byte
				for {
					n, err := resp.Body.Read(chunk[:])
					compressed.Write(chunk[:n])
					time.Sleep(time.Duration(n) * time.Second / 62_500)
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				file, err := backup.Decode(bytes.NewReader(compressed.Bytes()), 2<<20)
				if err != nil || len(file.Entries) != 1 || len(file.Entries[0].Chapters) != 512 {
					t.Fatalf("incomplete gzip backup: %v", err)
				}
				if elapsed := time.Since(start); elapsed <= 3*time.Minute {
					t.Fatalf("export took only %v", elapsed)
				}
				t.Logf("complete gzip with 512 marks in %v", time.Since(start))
			})
		})
	}
}

func TestUploadedRestoreQuerySurvivesServerReadTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := backupTransportServer(t)
		hold, err := s.DB.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer hold.Close()
		c, _ := backupHTTP(t, func(w http.ResponseWriter, r *http.Request) {
			s.restoreBackup(&statusWriter{ResponseWriter: w}, r, caller{account: accounts.Account{ID: 3}})
		})
		var raw bytes.Buffer
		if err := backup.WriteEntry(&raw, backup.Entry{Title: "Tide", URL: "/tide", Favorite: true}); err != nil {
			t.Fatal(err)
		}
		req := upload(t, "", "fixture", raw.Bytes())
		body := req.body.(string)
		start := time.Now()
		fmt.Fprintf(c, "POST "+restorePath+" HTTP/1.1\r\nHost: kit\r\nContent-Type: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", req.header["Content-Type"], len(body))
		time.Sleep(15 * time.Second)
		io.WriteString(c, body)
		synctest.Wait()
		if s.DB.Stats().WaitCount == 0 {
			t.Fatal("restore did not finish its upload and reach its database query")
		}
		time.Sleep(25 * time.Second) // beyond both the upload idle deadline and server ReadTimeout
		hold.Close()
		resp := transportResponse(t, c)
		defer resp.Body.Close()
		var report backup.Report
		if err := json.NewDecoder(resp.Body).Decode(&report); err != nil || report.Titles.MatchedByName+report.Titles.Matched != 1 {
			t.Fatalf("restore failed: %+v, %v", report, err)
		}
		t.Logf("complete restore at %v after 25s database wait", time.Since(start))
	})
}

func TestExportWallDeadlineCoversDatabaseWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := backupTransportServer(t)
		hold, err := s.DB.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer hold.Close()
		c := startTransportExport(t, s, 1, 0)
		synctest.Wait()
		if s.DB.Stats().WaitCount == 0 {
			t.Fatal("export did not reach its database query")
		}
		time.Sleep(3*time.Minute + time.Second)
		synctest.Wait()
		if len(s.backups.downloads) != 0 {
			t.Fatal("database wait retained an export slot beyond the wall deadline")
		}
		if resp, err := http.ReadResponse(bufio.NewReader(c), nil); err == nil && resp.StatusCode == 200 {
			t.Fatal("expired query became a successful empty download")
		}
	})
}

func TestTwoTricklingExportsReleaseBothSlots(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := backupTransportServer(t)
		results := make(chan error, 2)
		stop := make(chan struct{})
		defer close(stop)
		start := time.Now()
		for _, id := range []int64{1, 2} {
			resp := transportResponse(t, startTransportExport(t, s, id, 0))
			go func() {
				defer resp.Body.Close()
				// Record all delivered bytes to check that an abandoned gzip
				// can't be mistaken for a complete backup.
				var compressed bytes.Buffer
				var chunk [16 << 10]byte
				for {
					select {
					case <-stop:
						return
					case <-time.After(19 * time.Second):
					}
					n, err := io.ReadFull(resp.Body, chunk[:])
					compressed.Write(chunk[:n])
					if err != nil {
						break
					}
				}
				z, err := gzip.NewReader(&compressed)
				if err == nil {
					_, err = io.Copy(io.Discard, z)
					z.Close()
				}
				results <- err
			}()
		}
		takeThirdSlot := func() int {
			w := httptest.NewRecorder()
			s.exportBackup(w, httptest.NewRequest("GET", backupPath, nil), caller{account: accounts.Account{ID: 3}})
			return w.Code
		}
		if got := takeThirdSlot(); got != 429 {
			t.Fatalf("third account while busy: %d", got)
		}
		time.Sleep(179 * time.Second)
		synctest.Wait()
		if n := len(s.backups.downloads); n != 2 {
			t.Fatalf("trickling readers hit the idle timeout (only %d active)", n)
		}
		time.Sleep(6 * time.Second)
		synctest.Wait()
		if got := takeThirdSlot(); got != 200 || len(s.backups.downloads) != 0 {
			t.Fatalf("third account after %v: %d; %d slots still busy", time.Since(start), got, len(s.backups.downloads))
		}
		for range 2 {
			if err := <-results; err == nil {
				t.Fatal("timed out stream ended in a valid gzip trailer")
			}
		}
	})
}

func TestFastExportCannotEarnPastFortyMinutes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := httptest.NewRequest("GET", backupPath, nil)
		recorder := httptest.NewRecorder()
		recorder.Body = nil // consume the writes without holding 144 MB of response data
		w := setupBackupDeadlines(recorder, r, 0)
		ctx, cancel := w.limitExport(r.Context())
		defer cancel()
		// Three minutes plus 144 MB at 62,500 B/s would give 41m24s.
		chunk := make([]byte, 16_000)
		for range 9000 {
			if _, err := w.Write(chunk); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(39*time.Minute + 59*time.Second)
		if ctx.Err() != nil {
			t.Fatal("completed writes failed to earn the full transfer budget")
		}
		time.Sleep(time.Second)
		synctest.Wait()
		if ctx.Err() == nil {
			t.Fatal("database work outlived the forty-minute ceiling")
		}
	})
}
