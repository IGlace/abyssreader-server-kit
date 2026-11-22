// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"errors"
	"net/http"

	"github.com/IGlace/abyssreader-server-kit/internal/config"
	"github.com/IGlace/abyssreader-server-kit/internal/reports"
)

func (s *server) reportTitle(w http.ResponseWriter, r *http.Request, c caller) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	o, ok := readObject(w, r)
	if !ok {
		return
	}
	reason := o.str("reason")
	switch reason {
	case "wrong_information", "wrong_source", "chapter_not_working", "other":
	default:
		o.check("reason", "unknown_value")
	}
	var details *string
	if _, given := o.fields["details"]; given {
		details = o.nullableStr("details")
		if details != nil {
			o.notEmpty("details", *details)
			o.maxLen("details", *details, 500)
		}
	}
	if o.failed(w) {
		return
	}
	err := reports.Create(r.Context(), s.DB, c.account.ID, id, s.Config.FeatureOn(config.HostedChapters), reason, details)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, reports.ErrTitle):
		notFound(w, r)
	case errors.Is(err, reports.ErrDuplicate):
		writeError(w, http.StatusConflict, "already_exists", "You already have an open report on this title.")
	case errors.Is(err, reports.ErrFull):
		writeError(w, http.StatusBadRequest, "validation_failed", "Too many reports are open for this account or title.")
	default:
		s.storeFailed(w, "submit title report", err)
	}
}
