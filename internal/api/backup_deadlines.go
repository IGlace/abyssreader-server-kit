// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"context"
	"io"
	"net/http"
	"time"
)

const (
	backupIdleTimeout       = 20 * time.Second
	backupMinBytesPerSecond = 62_500 // 0.5 Mbit/s
	backupWriteChunk        = 16 << 10
	backupExportGrace       = 3 * time.Minute
	backupMaxDuration       = 40 * time.Minute
)

// An admitted backup may spend minutes transferring or processing on-device
// data. Apply transfer deadlines at the socket, independently of database work.
func setupBackupDeadlines(w http.ResponseWriter, r *http.Request, maxBody int64) *backupResponse {
	c := http.NewResponseController(w)
	_ = c.SetWriteDeadline(time.Time{})
	if maxBody > 0 {
		seconds := (maxBody + backupMinBytesPerSecond - 1) / backupMinBytesPerSecond
		budget := min(backupMaxDuration, backupIdleTimeout+time.Duration(seconds)*time.Second)
		r.Body = &backupUpload{ReadCloser: r.Body, controller: c, until: time.Now().Add(budget)}
	} else if r.ContentLength == 0 && len(r.TransferEncoding) == 0 {
		// Bodyless exports have nothing to read. Leave any inherited deadline
		// in place for requests whose unexpected body net/http may drain.
		_ = c.SetReadDeadline(time.Time{})
	}
	return &backupResponse{ResponseWriter: w, controller: c}
}

type backupUpload struct {
	io.ReadCloser
	controller *http.ResponseController
	until      time.Time
}

func (b *backupUpload) Read(p []byte) (int, error) {
	deadline := time.Now().Add(backupIdleTimeout)
	if b.until.Before(deadline) {
		deadline = b.until
	}
	_ = b.controller.SetReadDeadline(deadline)
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		// A lingering read deadline could cancel the request during the restore.
		_ = b.controller.SetReadDeadline(time.Time{})
	}
	return n, err
}

// The request goroutine owns until and ceiling. The timer only cancels the
// query context, including while no response writes are in progress.
type exportBudget struct {
	until, ceiling time.Time
	timer          *time.Timer
}

func (w *backupResponse) limitExport(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	now := time.Now()
	w.export = &exportBudget{
		until: now.Add(backupExportGrace), ceiling: now.Add(backupMaxDuration),
		timer: time.AfterFunc(backupExportGrace, cancel),
	}
	return ctx, func() {
		w.export.timer.Stop()
		cancel()
	}
}

func (b *exportBudget) credit(n int) error {
	now := time.Now()
	if !now.Before(b.until) {
		return context.DeadlineExceeded
	}
	if b.until.Before(b.ceiling) {
		// Only flushed compressed bytes earn time, measured from admission.
		b.until = b.until.Add(time.Duration(n) * time.Second / backupMinBytesPerSecond)
		if b.until.After(b.ceiling) {
			b.until = b.ceiling
		}
		b.timer.Reset(time.Until(b.until))
	}
	return nil
}

type backupResponse struct {
	http.ResponseWriter
	controller *http.ResponseController
	export     *exportBudget
}

func (w *backupResponse) Write(p []byte) (int, error) {
	defer w.SetWriteDeadline(time.Time{})
	total := 0
	for len(p) > 0 {
		chunk := p[:min(len(p), backupWriteChunk)]
		_ = w.SetWriteDeadline(time.Now().Add(backupIdleTimeout))
		n, err := w.ResponseWriter.Write(chunk)
		total += n
		if err != nil {
			return total, err
		}
		if n != len(chunk) {
			return total, io.ErrShortWrite
		}
		// Include net/http's buffers in the deadline before declaring progress.
		if err := w.controller.Flush(); err != nil {
			return total, err
		}
		if w.export != nil {
			if err := w.export.credit(n); err != nil {
				return total, err
			}
		}
		p = p[n:]
	}
	return total, nil
}

// The same cap applies to writes, idle-deadline resets and net/http's final flush.
func (w *backupResponse) SetWriteDeadline(t time.Time) error {
	if w.export != nil && (t.IsZero() || w.export.until.Before(t)) {
		t = w.export.until
	}
	return w.controller.SetWriteDeadline(t)
}

func (w *backupResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// net/http may still need to send the terminating chunk after the handler returns.
func finishBackupDeadlines(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(backupIdleTimeout))
}
