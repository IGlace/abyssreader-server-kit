// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"testing"
)

// Seed the last place in each quota through SQLite to exercise the boundary without
// consuming the HTTP rate budget. The actual submission always uses the contract route.
func TestReportStorageBounds(t *testing.T) {
	for _, mode := range []string{"account", "title", "history"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, reportsOn)
			h.smallCatalog()
			token, id := h.signUp("Reader", "reader@example.com", "correct-horse-7")
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := h.db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "account":
				exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<100)
 INSERT INTO titles(ref,slug,name,name_key,status,manifest_hash,created_at,updated_at)
 SELECT 'ref'||x,'ref'||x,'book','book','unknown','',0,0 FROM n`)
				exec("INSERT INTO title_reports(user_id,title_id,reason,created_at) SELECT ?,id,'other',0 FROM titles WHERE ref LIKE 'ref%'", id)
			case "title":
				exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<250)
 INSERT INTO users(email,email_key,display_name,created_at,updated_at) SELECT x||'@example.org',x||'@example.org','other',0,0 FROM n`)
				exec("INSERT INTO title_reports(user_id,title_id,reason,created_at) SELECT id,1,'other',0 FROM users WHERE id<>?", id)
			case "history":
				exec(`WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<500)
 INSERT INTO title_reports(user_id,title_id,reason,created_at,closed) SELECT ?,1,'other',x,1 FROM n`, id)
			}
			post := req{token: token, body: obj{"reason": "other"}}
			if mode != "history" {
				h.expect(h.do("POST", reportPath, post), 400, "validation_failed")
				exec("UPDATE title_reports SET closed=1 WHERE id=1")
			}
			h.expect(h.do("POST", reportPath, post), 204, "")
			if mode == "history" {
				var n, first int
				if err := h.db.QueryRow("SELECT count(*),min(id) FROM title_reports WHERE user_id=?", id).Scan(&n, &first); err != nil || n != 500 || first != 2 {
					t.Fatalf("retained reports: count=%d first=%d err=%v", n, first, err)
				}
			}
		})
	}
}
