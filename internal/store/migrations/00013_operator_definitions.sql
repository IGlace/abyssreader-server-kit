-- SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
-- Copyright 2026 Abyss Reader
-- +goose Up

CREATE TABLE definition_key (
    slot INTEGER PRIMARY KEY CHECK (slot = 1),
    key_id TEXT NOT NULL,
    public_key TEXT NOT NULL,
    endorsed_by TEXT,
    endorsement TEXT,
    CHECK ((endorsed_by IS NULL) = (endorsement IS NULL))
);

CREATE TABLE operator_definitions (
    source_slug TEXT PRIMARY KEY NOT NULL,
    version INTEGER NOT NULL CHECK (version BETWEEN 1 AND 2147483647),
    content TEXT,
    content_hash TEXT,
    signature TEXT NOT NULL,
    min_app_version TEXT,
    CHECK ((content IS NULL) = (content_hash IS NULL))
);

CREATE TABLE definition_health (
    source_slug TEXT NOT NULL REFERENCES operator_definitions(source_slug) ON DELETE CASCADE,
    error_kind TEXT NOT NULL,
    count INTEGER NOT NULL CHECK (count > 0),
    PRIMARY KEY (source_slug, error_kind)
);
