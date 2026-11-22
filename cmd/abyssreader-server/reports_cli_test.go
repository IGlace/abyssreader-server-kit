// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/contracttest"
	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

// Drive the installed command's actual process entry, including stdin confirmation and exit codes.
func TestReportsAndFeaturePurgeCLI(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "abyssreader-server")
	if b, e := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); e != nil {
		t.Fatalf("build: %v %s", e, b)
	}
	env := baseEnv(t)
	db, err := store.Open(context.Background(), filepath.Join(env["ABYSS_DATA_DIR"], databaseFile))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cli := func(input string, want int, args ...string) string {
		t.Helper()
		cmd := exec.Command(binary, append([]string{"admin"}, args...)...)
		cmd.Env = []string{}
		for key, value := range env {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
		cmd.Stdin = strings.NewReader(input)
		b, e := cmd.CombinedOutput()
		code := 0
		if e != nil {
			ee, ok := e.(*exec.ExitError)
			if !ok {
				t.Fatal(e)
			}
			code = ee.ExitCode()
		}
		if code != want {
			t.Fatalf("%v exit %d, want %d: %s", args, code, want, b)
		}
		return string(b)
	}
	hosted := "../../examples/manifests/sample-hosted"
	cli("", 0, "import", hosted)
	cli("", 0, "import", example)
	execSQL := func(s string, args ...any) {
		t.Helper()
		if _, e := db.Exec(s, args...); e != nil {
			t.Fatal(e)
		}
	}
	env["ABYSS_FEATURES"] = "reports,hosted-chapters"
	base, stop := serveForTest(t, env)
	stop = sync.OnceFunc(stop)
	defer stop()
	checkedPost := func(path, token string, body map[string]any, want int) map[string]any {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest("POST", base+path, bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		contracttest.Load(t).Check(t, req, resp, raw)
		if resp.StatusCode != want {
			t.Fatalf("%s returned %d, want %d", path, resp.StatusCode, want)
		}
		var result map[string]any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	signUp := func(name, email string) string {
		return checkedPost("/api/v1/auth/sign-up", "", map[string]any{"displayName": name, "email": email, "password": "correct-horse-7"}, 201)["token"].(string)
	}
	report := func(token, details string, status int) {
		checkedPost("/api/v1/titles/1/reports", token, map[string]any{"reason": "wrong_source", "details": details}, status)
	}
	reader := signUp("Reader", "a@example.org")
	second := signUp("Other", "b@example.org")
	report(reader, "hello\x1b[2J\x1b]0;fake title\a\r\nline\t\u2028\u202e", 204)
	report(reader, "duplicate", 409)
	report(second, "second", 204)
	out := cli("", 0, "reports")
	for _, s := range []string{"Night Ferry", "wrong_source", "a@example.org", "hello", "line", "second"} {
		if !strings.Contains(out, s) {
			t.Fatalf("missing %q: %q", s, out)
		}
	}
	if strings.ContainsAny(out, "\r\x1b\a\u2028\u202e") || strings.Contains(out, "fake title") || strings.Contains(out, "\nline") {
		t.Fatalf("unsafe report: %q", out)
	}
	cli("", 0, "reports", "close", "2")
	if limited := cli("", 0, "reports", "--limit", "1"); !strings.Contains(limited, "hello") || strings.Contains(limited, "second") {
		t.Fatalf("open first: %q", limited)
	}
	cli("", 2, "reports", "--limit", "101")
	cli("", 2, "reports", "--limit", "0")
	cli("", 0, "reports", "close-title", "1")
	report(reader, "new after closing", 204)
	changes := []map[string]any{
		{"opId": "00000000-0000-4000-8000-000000000005", "kind": "position", "key": map[string]any{"mainMangaId": 1}, "data": map[string]any{"sourceSlug": "hosted", "chapterUrl": "/chapters/1", "chapterNumber": 1, "chapterName": "One", "pageIndex": 0}, "clientUpdatedAt": "2026-10-10T14:00:04.000Z", "deleted": false},
		{"opId": "00000000-0000-4000-8000-000000000006", "kind": "chapterMark", "key": map[string]any{"mainMangaId": 1, "sourceSlug": "hosted", "chapterUrl": "/chapters/1"}, "data": map[string]any{"chapterNumber": 1, "chapterName": "One", "read": true, "lastPageRead": 1, "totalPages": 2, "bookmarked": false}, "clientUpdatedAt": "2026-10-10T14:00:05.000Z", "deleted": false},
	}
	pushed := checkedPost("/api/v1/sync/push", reader, map[string]any{"items": changes}, 200)
	for _, result := range pushed["results"].([]any) {
		if result.(map[string]any)["status"] != "applied" {
			t.Fatalf("seed sync: %v", pushed)
		}
	}
	stop()
	snapshot := func(tables ...string) string { t.Helper(); return tableSnapshot(t, db, tables) }
	hostedTables := []string{"hosted_source_titles", "chapters", "chapter_pages", "page_files", "page_images", "source_titles", "sources"}
	directory := func(name string) map[string]string {
		t.Helper()
		out := map[string]string{}
		dir := filepath.Join(env["ABYSS_DATA_DIR"], name)
		err := fs.WalkDir(os.DirFS(dir), ".", func(path string, d fs.DirEntry, e error) error {
			if e != nil || d.IsDir() {
				return e
			}
			b, e := os.ReadFile(filepath.Join(dir, path))
			out[path] = string(b)
			return e
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := snapshot(hostedTables...)
	images := directory("pages")
	covers := directory("covers")
	records := snapshot("sync_records", "sync_accounts", "users")
	reports := snapshot("title_reports")
	env["ABYSS_FEATURES"] = "reports"
	cli("", 1, "purge", "reports", "--yes")
	delete(env, "ABYSS_FEATURES")
	cli("no\n", 1, "purge", "reports")
	cli("", 1, "purge", "reports")
	unlock, err := lockData(env["ABYSS_DATA_DIR"])
	if err != nil {
		t.Fatal(err)
	}
	cli("", 1, "purge", "reports", "--yes")
	unlock()
	if reports != snapshot("title_reports") {
		t.Fatal("refused purge changed reports")
	}
	if out := cli("reports\n", 0, "purge", "reports"); !strings.Contains(out, "reports=3") {
		t.Fatal(out)
	}
	if before != snapshot(hostedTables...) || !reflect.DeepEqual(images, directory("pages")) {
		t.Fatal("purging reports changed hosted content")
	}
	execSQL("INSERT INTO title_reports(title_id,user_id,reason,details,created_at) VALUES (1,1,'other','retained',1700000000000)")
	reports = snapshot("title_reports")
	env["ABYSS_FEATURES"] = "hosted-chapters"
	cli("", 1, "purge", "hosted-chapters", "--yes")
	delete(env, "ABYSS_FEATURES")
	marker := filepath.Join(env["ABYSS_DATA_DIR"], "pages", "operator-note")
	if err := os.WriteFile(marker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	// Capture all core title columns except the import shortcut, plus every non-hosted source row.
	catalogSnapshot := func() string {
		return querySnapshot(t, db, "SELECT * FROM source_titles WHERE source_id IN (SELECT id FROM sources WHERE hosted=0) ORDER BY id") +
			querySnapshot(t, db, "SELECT * FROM sources WHERE hosted=0 ORDER BY id") +
			querySnapshot(t, db, `SELECT id,ref,slug,name,name_key,description,status,rating,views,cover_source,cover_image,cover_error,created_at,updated_at FROM titles ORDER BY id`)
	}
	coreCatalog := catalogSnapshot()
	out = cli("", 0, "purge", "hosted-chapters", "--yes")
	if !strings.Contains(out, "chapters=3") || !strings.Contains(out, fmt.Sprintf("image_files=%d", len(images))) {
		t.Fatal(out)
	}
	if coreCatalog != catalogSnapshot() {
		t.Error("hosted purge changed core catalog rows")
	}
	for _, text := range []string{"night-ferry", "admin titles remove <ref>", "restart"} {
		if !strings.Contains(out, text) {
			t.Errorf("missing %q in purge output: %s", text, out)
		}
	}
	for _, tab := range hostedTables[:5] {
		var n int
		if err := db.QueryRow("SELECT count(*) FROM " + tab).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s count %d: %v", tab, n, err)
		}
	}
	var remainingSources int
	if err := db.QueryRow("SELECT count(*) FROM sources WHERE hosted=1").Scan(&remainingSources); err != nil || remainingSources != 0 {
		t.Fatalf("hosted source rows: %d, %v", remainingSources, err)
	}
	if got := directory("pages"); !reflect.DeepEqual(got, map[string]string{"operator-note": "keep"}) {
		t.Fatal("page files after purge:", len(got))
	}
	if reports != snapshot("title_reports") || records != snapshot("sync_records", "sync_accounts", "users") || !reflect.DeepEqual(covers, directory("covers")) {
		t.Fatal("hosted purge changed unrelated data")
	}
	cli("", 0, "purge", "hosted-chapters", "--yes")
	cli("", 0, "import", hosted)
	var chapterID int
	if err := db.QueryRow("SELECT min(id) FROM chapters").Scan(&chapterID); err != nil || chapterID <= 3 {
		t.Fatalf("reimported chapter id %d: %v", chapterID, err)
	}
	cli("", 0, "titles", "remove", "night-ferry")
	env["ABYSS_FEATURES"] = "reports"
	base, stopAgain := serveForTest(t, env)
	defer stopAgain()
	if body := checkedPost("/api/v1/titles/1/reports", reader, map[string]any{"reason": "other"}, 404); body["code"] != "not_found" {
		t.Fatal(body)
	}
	if got := snapshot("title_reports"); got != "" {
		t.Fatalf("removed title kept reports: %s", got)
	}
}

func tableSnapshot(t *testing.T, db *sql.DB, tables []string) string {
	t.Helper()
	var out bytes.Buffer
	for _, table := range tables {
		if data := querySnapshot(t, db, "SELECT * FROM "+table+" ORDER BY rowid"); data != "" {
			out.WriteString(table)
			out.WriteString(data)
		}
	}
	return out.String()
}

func querySnapshot(t *testing.T, db *sql.DB, query string) string {
	t.Helper()
	var out bytes.Buffer
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		values := make([]any, len(cols))
		fields := make([]any, len(cols))
		for i := range fields {
			fields[i] = &values[i]
		}
		if err := rows.Scan(fields...); err != nil {
			t.Fatal(err)
		}
		if err := json.NewEncoder(&out).Encode(values); err != nil {
			t.Fatal(err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}
