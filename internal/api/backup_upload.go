// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"sync/atomic"

	"github.com/IGlace/abyssreader-server-kit/internal/backup"
)

const uploadPageSize = 32 << 10

var errUploadCapacity = errors.New("backup upload buffer is full")

// Pages are charged before allocation, including the unused end of the last
// page. Clients that have sent no body bytes hold no pages.
type uploadPool struct{ used atomic.Int64 }

func (p *uploadPool) reserve(limit int64) bool {
	for {
		used := p.used.Load()
		if used+uploadPageSize > limit {
			return false
		}
		if p.used.CompareAndSwap(used, used+uploadPageSize) {
			return true
		}
	}
}

type bufferedUpload struct {
	pages []io.Reader
	pool  *uploadPool
}

func (u *bufferedUpload) release() {
	u.pool.used.Add(-int64(len(u.pages)) * uploadPageSize)
	u.pages = nil
}
func (p *uploadPool) read(w http.ResponseWriter, r *http.Request, maxBody, limit int64) (*bufferedUpload, error) {
	if r.ContentLength > maxBody {
		return nil, backup.ErrTooLarge
	}
	body := http.MaxBytesReader(w, r.Body, maxBody)
	upload := &bufferedUpload{pool: p}
	var first [1]byte
	for {
		// Probe before reserving so an exact number of pages fits the budget.
		_, err := io.ReadFull(body, first[:])
		if err == io.EOF {
			return upload, nil
		}
		if err != nil {
			upload.release()
			return nil, err
		}
		if !p.reserve(limit) {
			upload.release()
			return nil, errUploadCapacity
		}
		page := make([]byte, uploadPageSize)
		page[0] = first[0]
		used := 1
		for used < len(page) {
			var n int
			n, err = body.Read(page[used:])
			used += n
			if err != nil {
				break
			}
		}
		upload.pages = append(upload.pages, bytes.NewReader(page[:used]))
		if err == io.EOF {
			return upload, nil
		}
		if err != nil {
			upload.release()
			return nil, err
		}
	}
}

func (u *bufferedUpload) decode(w http.ResponseWriter, r *http.Request, maxFile int64, decode backupDecoder) (*backup.File, error) {
	defer u.release()
	buffered := r.Clone(r.Context())
	buffered.Body = io.NopCloser(io.MultiReader(u.pages...))
	return decode(w, buffered, maxFile)
}
