// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

func TestReportUsage(t *testing.T) {
	var out bytes.Buffer
	if run(context.Background(), []string{"--help"}, &out, &out, envOf(baseEnv(t))) != 0 || !strings.Contains(out.String(), "admin reports") || !strings.Contains(out.String(), "admin purge") {
		t.Fatal("top-level help omits moderation or purge")
	}
}

func TestReportQueuePagesFilterAndClose(t *testing.T) {
	env := baseEnv(t)
	db, err := store.Open(context.Background(), filepath.Join(env["ABYSS_DATA_DIR"], databaseFile))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	check := func(want int, args ...string) string {
		t.Helper()
		code, out := admin_(t, env, args...)
		if code != want {
			t.Fatalf("%v exit %d, want %d: %s", args, code, want, out)
		}
		return out
	}
	check(0, "import", example)
	for _, query := range []string{
		`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<150)
 INSERT INTO users(email,email_key,display_name,created_at,updated_at) SELECT x||'@example.org',x||'@example.org','Reader',0,0 FROM n`,
		`INSERT INTO title_reports(title_id,user_id,reason,details,created_at) SELECT 1,id,'other','detail-'||id,0 FROM users ORDER BY id`,
		`INSERT INTO title_reports(title_id,user_id,reason,details,created_at) VALUES (2,1,'other','second title',0)`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	first := check(0, "reports", "--title", "lantern-keeper", "--limit", "100")
	if !strings.Contains(first, "100 of 150 open") || !strings.Contains(first, "--page 2") || !strings.Contains(first, "--title lantern-keeper") || strings.Contains(first, `"detail-1"`) {
		t.Fatal("first page or next command incorrect")
	}
	second := check(0, "reports", "--title", "1", "--page", "2", "--limit", "100")
	if !strings.Contains(second, "50 of 150 open") || strings.Contains(second, "second title") || strings.Contains(second, "--page 3") {
		t.Fatal("unexpected second page")
	}
	for i := 1; i <= 50; i++ {
		if !strings.Contains(second, fmt.Sprintf(`"detail-%d"`, i)) {
			t.Fatalf("missing report %d", i)
		}
	}
	for _, args := range [][]string{{"--page", "0"}, {"--limit", "101"}, {"--title", ""}, {"--page", "9999999999999999999999999"}} {
		check(2, append([]string{"reports"}, args...)...)
	}
	unlock, err := lockData(env["ABYSS_DATA_DIR"])
	if err != nil {
		t.Fatal(err)
	}
	check(1, "reports", "close", "--account", "1")
	unlock()
	if out := check(0, "reports", "close", "--account", "1"); !strings.Contains(out, "Closed reports=2") {
		t.Fatal(out)
	}
	after := check(0, "reports", "--title", "lantern-keeper", "--limit", "1")
	if !strings.Contains(after, "1 of 149 open") {
		t.Fatal(after)
	}
	for _, id := range []string{"1", "9999"} {
		if out := check(1, "reports", "close", id); !strings.Contains(strings.ToLower(out), "no open report") {
			t.Fatal(out)
		}
	}
	if out := check(0, "titles", "remove", "lantern-keeper"); !strings.Contains(out, "reports=150") {
		t.Fatal(out)
	}
}

func TestTerminalTextLimitsCombiningMarks(t *testing.T) {
	marks := strings.Repeat("\u0301", 500)
	want := "a" + strings.Repeat("\u0301", 3) + "e\u0301"
	if got := terminalText(marks + "a" + marks + "e\u0301"); got != want {
		t.Fatalf("want three marks per base, got %d runes", len([]rune(got)))
	}
}
