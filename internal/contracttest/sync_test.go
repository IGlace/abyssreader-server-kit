// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package contracttest

import (
	"strings"
	"testing"
)

const okPull = `{"changes":[{"kind":"chapterMark","key":{"mainMangaId":1,"sourceSlug":"paper-moon","chapterUrl":"/c/1"},
"data":{"chapterNumber":1,"chapterName":null,"read":true,"lastPageRead":null,"totalPages":null,"bookmarked":false},
"clientUpdatedAt":"2026-10-10T10:00:00.000Z","deleted":false},
{"kind":"favorite","key":{"mainMangaId":2},"data":null,"clientUpdatedAt":"2026-10-10T10:00:00.000Z","deleted":true}],
"cursor":"d.2","hasMore":false}`

const okPush = `{"results":[{"opId":"00000000-0000-4000-8000-000000000001","status":"applied"},{"opId":"?","status":"rejected","reason":"invalid"}],"cursor":"d.1"}`

func TestValidSyncAnswersPass(t *testing.T) {
	for _, c := range []struct {
		method, path string
		status       int
		body         string
	}{
		{"GET", "/api/v1/sync/pull", 200, okPull},
		{"POST", "/api/v1/sync/push", 200, okPush},
		{"GET", "/api/v1/sync/pull", 409, `{"fullResyncRequired":true}`},
	} {
		if errs := check(t, c.method, c.path, c.status, nil, c.body); len(errs) != 0 {
			t.Errorf("%s %d refused: %v", c.path, c.status, errs)
		}
	}
}

// The sync bodies are held to the sync schema itself: what the OpenAPI strict mode cannot see
// in them is still caught.
func TestEverySyncBreakIsCaught(t *testing.T) {
	for name, c := range map[string]struct {
		method, path string
		status       int
		body         string
	}{
		"an undeclared key field":       {"GET", "/api/v1/sync/pull", 200, strings.Replace(okPull, `"mainMangaId":2}`, `"mainMangaId":2,"device":"x"}`, 1)},
		"an undeclared data field":      {"GET", "/api/v1/sync/pull", 200, strings.Replace(okPull, `"bookmarked":false}`, `"bookmarked":false,"downloaded":true}`, 1)},
		"an undeclared change field":    {"GET", "/api/v1/sync/pull", 200, strings.Replace(okPull, `"deleted":true}`, `"deleted":true,"seq":2}`, 1)},
		"a mark without its url":        {"GET", "/api/v1/sync/pull", 200, strings.Replace(okPull, `,"chapterUrl":"/c/1"`, ``, 1)},
		"a tombstone with data":         {"GET", "/api/v1/sync/pull", 200, strings.Replace(okPull, `"data":null,"clientUpdatedAt":"2026-10-10T10:00:00.000Z","deleted":true`, `"data":{},"clientUpdatedAt":"2026-10-10T10:00:00.000Z","deleted":true`, 1)},
		"a six-digit timestamp":         {"GET", "/api/v1/sync/pull", 200, strings.Replace(okPull, `10:00:00.000Z`, `10:00:00.000000Z`, 1)},
		"no hasMore":                    {"GET", "/api/v1/sync/pull", 200, strings.Replace(okPull, `,"hasMore":false`, ``, 1)},
		"a reason on an applied result": {"POST", "/api/v1/sync/push", 200, strings.Replace(okPush, `"status":"applied"`, `"status":"applied","reason":"cap"`, 1)},
		"an empty cursor":               {"POST", "/api/v1/sync/push", 200, strings.Replace(okPush, `"d.1"`, `""`, 1)},
		"a 409 with the Error body":     {"GET", "/api/v1/sync/pull", 409, `{"code":"conflict","message":"x"}`},
	} {
		if errs := check(t, c.method, c.path, c.status, nil, c.body); len(errs) == 0 {
			t.Errorf("%s: not caught", name)
		}
	}
}
