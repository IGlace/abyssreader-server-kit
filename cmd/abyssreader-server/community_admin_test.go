// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/accounts"
	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

func TestCommunityModerationKeepsSuspendedAccountData(t *testing.T) {
	env := baseEnv(t)
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(env["ABYSS_DATA_DIR"], databaseFile))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	code, out := admin_(t, env, "import", example)
	if code != 0 {
		t.Fatal(out)
	}
	svc := accounts.New(db, 0)
	sess, err := svc.SignUp(ctx, "Reader", "test@example.org", "correct-horse-7")
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"INSERT INTO comments (id,user_id,title_id,text,spoiler,created_at,updated_at) VALUES (1,1,1,'parent',0,0,0)",
		"INSERT INTO comments (id,user_id,title_id,parent_id,text,spoiler,created_at,updated_at) VALUES (2,1,1,1,'reply',0,0,0)",
		"INSERT INTO collections (id,user_id,slug,name,name_key,created_at,updated_at) VALUES (1,1,'tests','Test collection','test collection',0,0)",
		"INSERT INTO comments (id,user_id,collection_id,text,spoiler,created_at,updated_at) VALUES (3,1,1,'collection comment',0,0,0)",
		"INSERT INTO content_reports (user_id,comment_id,reason,created_at) VALUES (1,2,'spam',0),(1,3,'spam',0)",
		"INSERT INTO content_reports (user_id,collection_id,reason,created_at) VALUES (1,1,'other',0)",
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want int, args ...string) string {
		t.Helper()
		code, out := admin_(t, env, args...)
		if code != want {
			t.Fatalf("%v exit %d: %s", args, code, out)
		}
		return out
	}
	list := check(0, "reports")
	for _, phrase := range []string{"content:1", "reply", "collection comment", "Test collection"} {
		if !strings.Contains(list, phrase) {
			t.Fatalf("report list missing %q: %s", phrase, list)
		}
	}
	check(0, "reports", "close", "content:1")
	check(1, "reports", "close", "content:1")
	check(0, "suspend", "test@example.org", "--reason", "Repeated spam")
	var n int
	if err := db.QueryRow("SELECT count(*) FROM comments").Scan(&n); err != nil || n != 3 {
		t.Fatalf("suspension removed data: %d %v", n, err)
	}
	base, stop := serveForTest(t, env)
	defer stop()
	req, err := http.NewRequest("GET", base+"/api/v1/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+sess.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("suspended session got %d", resp.StatusCode)
	}
	resp, err = http.Post(base+"/api/v1/auth/sign-in", "application/json", strings.NewReader(`{"email":"test@example.org","password":"correct-horse-7"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var e struct {
		Code    string
		Message string
	}
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden || e.Code != "account_suspended" || !strings.Contains(e.Message, "Repeated spam") {
		t.Fatalf("suspended sign-in got %d %+v", resp.StatusCode, e)
	}
	check(0, "unsuspend", "1")
	if _, err := svc.SignIn(ctx, "test@example.org", "correct-horse-7"); err != nil {
		t.Fatal(err)
	}
	check(0, "remove", "comment", "1")
	if err := db.QueryRow("SELECT count(*) FROM comments WHERE title_id=1").Scan(&n); err != nil || n != 0 {
		t.Fatalf("title replies survived moderation: %d %v", n, err)
	}
	check(0, "remove", "collection", "1")
	if err := db.QueryRow("SELECT count(*) FROM comments").Scan(&n); err != nil || n != 0 {
		t.Fatalf("collection comments survived moderation: %d %v", n, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM content_reports").Scan(&n); err != nil || n != 0 {
		t.Fatalf("reports on removed content survived moderation: %d %v", n, err)
	}
}
