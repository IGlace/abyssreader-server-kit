-- SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
-- Copyright 2026 Abyss Reader

-- +goose Up
CREATE TABLE title_reports (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    title_id   INTEGER NOT NULL REFERENCES titles(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reason     TEXT NOT NULL,
    details    TEXT,
    created_at INTEGER NOT NULL,
    closed     INTEGER NOT NULL DEFAULT 0 CHECK (closed IN (0,1))
) STRICT;

CREATE UNIQUE INDEX title_reports_open ON title_reports(user_id, title_id) WHERE closed = 0;
CREATE INDEX title_reports_account ON title_reports(user_id, closed, id);
CREATE INDEX title_reports_title ON title_reports(title_id, closed);
CREATE INDEX title_reports_queue ON title_reports(closed, id DESC);
