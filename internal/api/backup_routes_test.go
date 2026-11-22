// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"bytes"
	"github.com/IGlace/abyssreader-server-kit/internal/config"
	"mime/multipart"
	"testing"
)

const backupPath = BasePath + "/me/backup"
const restorePath = backupPath + "/restore"

func backupOn(c *config.Config) { c.Features = []string{"backup"} }
func upload(t *testing.T, token, filename string, data []byte) req {
	t.Helper()
	var b bytes.Buffer
	m := multipart.NewWriter(&b)
	p, err := m.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = m.Close(); err != nil {
		t.Fatal(err)
	}
	return req{token: token, body: b.String(), header: map[string]string{"Content-Type": m.FormDataContentType()}}
}
func TestBackupFeatureGateAndInvalidUpload(t *testing.T) {
	h := newHarness(t, nil)
	h.expect(h.do("GET", backupPath, req{}), 404, "feature_disabled")
	h.expect(h.do("POST", restorePath, req{}), 404, "feature_disabled")
	h = h.reopen(backupOn)
	h.expect(h.do("GET", backupPath, req{}), 401, "session_invalid")
	h.expect(h.do("POST", restorePath, req{}), 401, "session_invalid")
	tok, _ := h.signUp("Reader", "reader@example.com", "correct-horse-7")
	h.expect(h.do("POST", restorePath, req{token: tok}), 400, "validation_failed")
	for _, data := range [][]byte{nil, []byte("not protobuf")} {
		h.expect(h.do("POST", restorePath, upload(t, tok, "anything", data)), 400, "backup_invalid")
	}
}
