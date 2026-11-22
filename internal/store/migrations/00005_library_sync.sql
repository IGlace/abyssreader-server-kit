-- SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
-- Copyright 2026 Abyss Reader
--
-- Library and sync: the sync contract (contract/sync/sync.schema.json) on SQLite. Every
-- record an account syncs (the library lists favorite / planning / blacklist, the vote, the
-- reading position, chapter marks, the update inbox) is one row per key: a live record, or a
-- tombstone once it was removed. Only internal/usersync writes these tables, inside one write
-- transaction (SQLite has one writer), so an account's sequence numbers commit in order.

-- +goose Up
-- One row per account that synced: its last sequence number, and the floor below which a delta
-- cursor can no longer be served (tombstones older than that were purged: 409).
CREATE TABLE sync_accounts (
    user_id   INTEGER PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    seq       INTEGER NOT NULL DEFAULT 0,
    floor_seq INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE sync_records (
    id                INTEGER PRIMARY KEY,
    user_id           INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind              TEXT    NOT NULL,  -- favorite, planning, blacklist, rating, position, chapterMark, inbox
    -- The key. A chapter mark is keyed by its source and chapter url (key_title 0); every other
    -- kind by its title (source_slug and chapter_url '', url_hash empty). The unique key holds
    -- the SHA-256 of the url (32 bytes), not the url itself (up to 2048 characters).
    key_title         INTEGER NOT NULL,
    source_slug       TEXT    NOT NULL,
    chapter_url       TEXT    NOT NULL,
    url_hash          BLOB    NOT NULL,
    -- The title (mainMangaId). No foreign key: a tombstone outlives a removed title, whose id is
    -- never used again. Removing a title tombstones its records (usersync.RemoveTitle).
    title_id          INTEGER NOT NULL,
    data              TEXT,              -- the record's data as JSON, every field present; NULL for the lists and tombstones
    client_updated_at INTEGER NOT NULL,  -- unix ms
    deleted           INTEGER NOT NULL,  -- 1 = a tombstone
    deleted_at        INTEGER,           -- unix ms, server time, when it became a tombstone
    seq               INTEGER NOT NULL,  -- the account's sequence number of the last write
    UNIQUE (user_id, kind, key_title, source_slug, url_hash),
    UNIQUE (user_id, seq)                -- a delta pull reads by it; a number is never given twice
) STRICT;

-- An account's live records by kind and title: the cap counts, the library lists, the search's
-- blacklist and the caller's state on titles.
CREATE INDEX sync_records_live ON sync_records (user_id, kind, key_title) WHERE deleted = 0;
-- The snapshot pull pages an account's live records by id.
CREATE INDEX sync_records_snapshot ON sync_records (user_id, id) WHERE deleted = 0;
-- Removing a title finds its live records.
CREATE INDEX sync_records_title ON sync_records (title_id) WHERE deleted = 0;
-- The purge finds old tombstones.
CREATE INDEX sync_records_tombstones ON sync_records (deleted_at) WHERE deleted = 1;
