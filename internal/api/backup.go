// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/backup"
	"github.com/IGlace/abyssreader-server-kit/internal/config"
	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

func (f featureRoutes) BackupRoutes() {
	s := f.s
	route(f.v1, BasePath+"/me/backup", map[string]http.HandlerFunc{"GET": s.feature(f.name, s.signedIn(s.exportBackup))})
	route(f.v1, BasePath+"/me/backup/restore", map[string]http.HandlerFunc{"POST": s.feature(f.name, s.signedIn(s.restoreBackup))})
}

type backupLimits struct {
	restoring, exporting  sync.Map // active accounts only
	processing, downloads chan struct{}
	admitted              chan struct{} // the processing restore and completed uploads waiting for it
	uploads               uploadPool
	restores, exports     *limiter
}

func newBackupLimits() *backupLimits {
	return &backupLimits{
		processing: make(chan struct{}, 1),
		downloads:  make(chan struct{}, 2),
		admitted:   make(chan struct{}, 4), // one processing and up to three waiting
		restores:   newLimiter(6, time.Hour),
		exports:    newLimiter(30, time.Hour),
	}
}
func trySlot(slots chan struct{}) bool {
	select {
	case slots <- struct{}{}:
		return true
	default:
		return false
	}
}

// Only a completed upload takes a place in the processing queue. Slow
// uploads are bounded by the byte budget and never hold a place in line.
func (l *backupLimits) process(ctx context.Context, key string, run func()) (retryAfter int) {
	if !trySlot(l.admitted) {
		return 30
	}
	defer func() { <-l.admitted }()
	wait, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	select {
	case l.processing <- struct{}{}:
		defer func() { <-l.processing }()
	case <-wait.Done():
		return 30
	}
	if wait.Err() != nil {
		return 30
	}
	if retry, ok := l.restores.allow(key); !ok {
		return retry
	}
	run()
	return 0
}

var errFileMissing = errors.New("missing file")

const multipartAllowance = 64 << 10

// NextRawPart never spools to disk. Limit the entire body (including part
// headers, unrelated parts and the epilogue) as well as the file itself.
func readBackup(w http.ResponseWriter, r *http.Request, maxFile int64) (*backup.File, error) {
	maxBody := maxFile + multipartAllowance
	if r.ContentLength > maxBody {
		return nil, backup.ErrTooLarge
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, errFileMissing
	}
	var file *backup.File
	for count := 0; ; count++ {
		part, err := reader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if count >= 10 {
			return nil, backup.ErrCount
		}
		if part.FormName() == "file" {
			if file != nil {
				return nil, backup.ErrInvalid
			}
			file, err = backup.Decode(part, maxFile)
		} else {
			_, err = io.Copy(io.Discard, part)
		}
		if err != nil {
			return nil, err
		}
	}
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		return nil, err
	}
	if file == nil {
		return nil, errFileMissing
	}
	return file, nil
}

type backupDecoder func(http.ResponseWriter, *http.Request, int64) (*backup.File, error)

func (s *server) restoreBackup(w http.ResponseWriter, r *http.Request, c caller) {
	s.restoreUpload(w, r, c, readBackup)
}

func (s *server) restoreUpload(w http.ResponseWriter, r *http.Request, c caller, decode backupDecoder) {
	id := c.account.ID
	key := strconv.FormatInt(id, 10)
	limits := s.backups
	if _, busy := limits.restoring.LoadOrStore(id, true); busy {
		writeRateLimited(w, 30)
		return
	}
	defer limits.restoring.Delete(id)
	if wait, ok := limits.restores.check(key); !ok {
		writeRateLimited(w, wait)
		return
	}
	maxMiB := s.Config.AccountBackupMaxMiB
	if maxMiB == 0 {
		maxMiB = 20
	}
	maxFile := int64(maxMiB) << 20
	maxBody := maxFile + multipartAllowance
	if len(limits.admitted) == cap(limits.admitted) || limits.uploads.used.Load() >= 2*maxBody {
		// HTTP/1 would otherwise drain part of the request before sending
		// the refusal. HTTP/2 can end this request without closing the connection.
		if r.ProtoMajor == 1 {
			w.Header().Set("Connection", "close")
		}
		writeRateLimited(w, 30)
		return
	}
	w = setupBackupDeadlines(w, r, maxBody)
	defer finishBackupDeadlines(w)
	// Charge all multipart bytes (including pre-file fields) before any
	// parsing. Only a complete upload can acquire the decoded-file slot.
	upload, err := limits.uploads.read(w, r, maxBody, 2*maxBody)
	if err != nil {
		writeUploadError(w, err)
		return
	}
	var report backup.Report
	var decodeErr error
	retry := limits.process(r.Context(), key, func() {
		var file *backup.File
		file, decodeErr = upload.decode(w, r, maxFile, decode)
		if decodeErr == nil {
			report, err = backup.Restore(r.Context(), s.DB, s.sync, id, file, s.Config.FeatureOn(config.HostedChapters), s.Config.PublicURL)
		}
	})
	if retry > 0 {
		upload.release()
		if r.Context().Err() == nil {
			writeRateLimited(w, retry)
		}
		return
	}
	if decodeErr != nil {
		writeUploadError(w, decodeErr)
		return
	}
	s.writeRestoreResult(w, report, err)
}

func writeUploadError(w http.ResponseWriter, err error) {
	var over *http.MaxBytesError
	switch {
	case errors.Is(err, errUploadCapacity):
		writeRateLimited(w, 30)
	case errors.Is(err, backup.ErrTooLarge) || errors.As(err, &over) || errors.Is(err, multipart.ErrMessageTooLarge):
		writeError(w, 413, "too_large", "The backup is larger than this server accepts.")
	case errors.Is(err, errFileMissing):
		writeValidation(w, []fieldError{{"file", "required"}})
	case errors.Is(err, backup.ErrCount):
		writeValidation(w, []fieldError{{"file", "too_many"}})
	default:
		writeError(w, 400, "backup_invalid", "The file is not a readable backup.")
	}
}

func (s *server) writeRestoreResult(w http.ResponseWriter, report backup.Report, err error) {
	var action string
	switch {
	case errors.Is(err, backup.ErrRestoreCapacity):
		action = "The account storage limit was reached. Pull sync and retry once capacity is available."
	case errors.Is(err, backup.ErrRestoreRetention):
		action = "The file exceeds the chapter limit. Pull sync and restore smaller files."
	case errors.Is(err, backup.ErrRestoreIncomplete):
		action = "Pull sync and retry the same file."
	}
	if action != "" {
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusServiceUnavailable, "unavailable", fmt.Sprintf("Restore incomplete. %d chapter marks committed. %s", report.ChapterMarks.Restored, action))
		return
	}
	if err != nil {
		s.backupFailed(w, err)
		return
	}
	writeJSON(w, 200, report)
}

// A driver's error can contain a record value. Keep backup contents out of logs.
func (s *server) backupFailed(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	if !store.IsBusy(err) {
		err = errors.New("account backup storage failure")
	}
	s.storeFailed(w, "account backup", err)
}

type backupStream struct {
	w       http.ResponseWriter
	started bool
}

func (s *backupStream) Write(p []byte) (int, error) {
	if !s.started {
		h := s.w.Header()
		h.Set("Content-Type", "application/octet-stream")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Disposition", `attachment; filename="abyssreader_kit_`+time.Now().UTC().Format("2006-01-02_15-04")+`.tachibk"`)
		s.started = true
	}
	return s.w.Write(p)
}
func (s *server) exportBackup(w http.ResponseWriter, r *http.Request, c caller) {
	id := c.account.ID
	if _, busy := s.backups.exporting.LoadOrStore(id, true); busy {
		writeRateLimited(w, 30)
		return
	}
	defer s.backups.exporting.Delete(id)
	if !trySlot(s.backups.downloads) {
		writeRateLimited(w, 30)
		return
	}
	defer func() { <-s.backups.downloads }()
	response := setupBackupDeadlines(w, r, 0)
	defer finishBackupDeadlines(response)
	if wait, ok := s.backups.exports.allow(strconv.FormatInt(id, 10)); !ok {
		writeRateLimited(response, wait)
		return
	}
	ctx, cancel := response.limitExport(r.Context())
	defer cancel()
	stream := &backupStream{w: response}
	err := backup.Export(ctx, s.DB, id, s.Config.FeatureOn(config.HostedChapters), s.Config.PublicURL, s.Config.ServerName, stream)
	if err != nil {
		if stream.started || ctx.Err() != nil {
			panic(http.ErrAbortHandler)
		} // No successful response or gzip trailer after an interrupted export.
		s.backupFailed(response, err)
	}
}
