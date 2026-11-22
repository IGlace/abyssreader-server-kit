// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

// errorBody is the contract's Error: every non-2xx response carries it.
type errorBody struct {
	Code    string       `json:"code"`
	Message string       `json:"message"`
	Fields  []fieldError `json:"fields,omitempty"`
}

type fieldError struct {
	Field string `json:"field"`
	Code  string `json:"code"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string, fields ...fieldError) {
	writeJSON(w, status, errorBody{Code: code, Message: message, Fields: fields})
}

func writeValidation(w http.ResponseWriter, fields []fieldError) {
	writeError(w, http.StatusBadRequest, "validation_failed", "Some fields are not valid.", fields...)
}

func writeRateLimited(w http.ResponseWriter, retryAfter int) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many attempts. Try again later.")
}

// busyRetry is the Retry-After of a 503 when every password-hashing slot is taken.
const busyRetry = 5

func writeBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(busyRetry))
	writeError(w, http.StatusServiceUnavailable, "unavailable", "The server is busy. Try again in a few seconds.")
}

// storeFailed answers a request whose read or write of the database failed, the one place that
// does: 503 unavailable with Retry-After when SQLite was busy (a write waited for the one writer
// past the busy timeout), else the 500 after logging what failed.
func (s *server) storeFailed(w http.ResponseWriter, what string, err error) {
	if store.IsBusy(err) {
		writeBusy(w)
		return
	}
	s.Logger.Error(what, "err", err)
	writeServerError(w)
}

func writeServerError(w http.ResponseWriter) {
	writeError(w, http.StatusInternalServerError, "server_error", "Something went wrong on the server.")
}

// maxBody caps JSON request bodies. The account routes declare no 413, so a larger body is a
// 400 validation_failed there.
const maxBody = 64 << 10

// jsonObject is a request body read as a JSON object, so that a missing field, a null and a
// wrong type can each be told apart and reported per field.
type jsonObject struct {
	fields map[string]json.RawMessage
	order  []string          // fields in the order they were read
	errs   map[string]string // field → FieldError code
}

// readObject reads the body as one JSON object. On failure it has written the 400.
func readObject(w http.ResponseWriter, r *http.Request) (*jsonObject, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusBadRequest, "validation_failed", "The request body is too large.")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "validation_failed", "The request body could not be read.")
		return nil, false
	}
	o := &jsonObject{errs: map[string]string{}}
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&o.fields); err != nil || o.fields == nil || dec.More() {
		writeError(w, http.StatusBadRequest, "validation_failed", "The request body must be one JSON object.")
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		writeError(w, http.StatusBadRequest, "validation_failed", "The request body must be one JSON object.")
		return nil, false
	}
	return o, true
}

// str reads a required, non-null string field.
func (o *jsonObject) str(name string) string {
	o.order = append(o.order, name)
	raw, ok := o.fields[name]
	if !ok || string(raw) == "null" {
		o.errs[name] = "required"
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		o.errs[name] = "invalid"
	}
	return s
}

// nullableStr reads a required field that may be null (nil).
func (o *jsonObject) nullableStr(name string) *string {
	o.order = append(o.order, name)
	raw, ok := o.fields[name]
	if !ok {
		o.errs[name] = "required"
		return nil
	}
	if string(raw) == "null" {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		o.errs[name] = "invalid"
		return nil
	}
	return &s
}

// check adds a rule's code ("" = fine) for a field that was read without a shape error, so one
// 400 lists every problem of the request.
func (o *jsonObject) check(name, code string) {
	if _, bad := o.errs[name]; !bad && code != "" {
		o.errs[name] = code
	}
}

// maxLen adds too_long for a field read without a shape error that is longer than max
// characters (the contract's maxLength).
func (o *jsonObject) maxLen(name, value string, max int) {
	if utf8.RuneCountInString(value) > max {
		o.check(name, "too_long")
	}
}

// notEmpty adds required for a field that was read as "".
func (o *jsonObject) notEmpty(name, value string) {
	if value == "" {
		o.check(name, "required")
	}
}

// failed writes the 400 when any field is missing, of the wrong type or breaks a rule.
func (o *jsonObject) failed(w http.ResponseWriter) bool {
	if len(o.errs) == 0 {
		return false
	}
	var fields []fieldError
	for _, name := range o.order {
		if code, bad := o.errs[name]; bad {
			fields = append(fields, fieldError{Field: name, Code: code})
		}
	}
	writeValidation(w, fields)
	return true
}
