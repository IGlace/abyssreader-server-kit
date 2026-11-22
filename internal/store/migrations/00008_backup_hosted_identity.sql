-- SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
-- Copyright 2026 Abyss Reader

-- +goose Up
-- The reserved hosted source uses database-local source-title and chapter URLs.
-- Its backup identity belongs to the whole catalog, across address and row-ID changes.
CREATE TABLE backup_hosted_identity (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    source_id INTEGER NOT NULL CHECK (source_id < 0)
) STRICT;

INSERT INTO backup_hosted_identity (singleton, source_id) VALUES (1, random() | (-9223372036854775807 - 1));
