-- SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
-- Copyright 2026 Abyss Reader

-- +goose Up
-- Export pages one account's live title IDs, then its marks on each title.
-- SQLite stores the rowid (sync_records.id) after these keys in the index.
CREATE INDEX sync_records_user_title ON sync_records (user_id, title_id) WHERE deleted = 0;
