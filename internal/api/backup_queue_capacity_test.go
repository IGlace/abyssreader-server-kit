// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"

	"github.com/IGlace/abyssreader-server-kit/internal/backup"
)

type restoreBodyProbe struct {
	read bool
}

func (b *restoreBodyProbe) Read([]byte) (int, error) {
	b.read = true
	return 0, io.EOF
}

func TestFullRestoreQueueRefusesBeforeReading(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := &server{backups: newBackupLimits()}
		release := make(chan struct{})
		defer close(release)
		decode := func(http.ResponseWriter, *http.Request, int64) (*backup.File, error) {
			<-release
			return nil, backup.ErrInvalid
		}
		// One decode is in progress and three completed uploads are waiting.
		for id := int64(1); id <= 4; id++ {
			go tryRestore(s, id, queuedRequest(id), decode)
			synctest.Wait()
		}

		body := &restoreBodyProbe{}
		req := httptest.NewRequest("POST", restorePath, body)
		got := tryRestore(s, 5, req, decode)
		if got.Code != 429 || got.Header().Get("Retry-After") == "" || got.Header().Get("Connection") != "close" || body.read {
			t.Fatalf("got HTTP %d, headers %v, read body %t", got.Code, got.Header(), body.read)
		}
	})
}
