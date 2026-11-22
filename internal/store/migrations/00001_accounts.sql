-- SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
-- Copyright 2026 Abyss Reader
--
-- Accounts and their sessions. Migrations run forward only, at start-up.
--
-- Rule for every later table that holds something an account created (library, sync records,
-- votes, comments, collections, reports, restored backups): it references users(id) with
-- ON DELETE CASCADE, so deleting the account row deletes everything the account created.
-- internal/store's schema test enforces this for every `user_id` column.

-- +goose Up
CREATE TABLE users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT, -- AUTOINCREMENT: an id is never reused (contract v1)
    email         TEXT    NOT NULL,                  -- as the user typed it
    email_key     TEXT    NOT NULL UNIQUE,           -- lower-cased, for sign-in and uniqueness
    display_name  TEXT    NOT NULL,
    avatar_url    TEXT,
    password_hash TEXT,                              -- NULL for an account without a password (Google sign-in, later)
    disabled_at   INTEGER,                           -- unix ms; set by `admin users disable`
    created_at    INTEGER NOT NULL,                  -- unix ms
    updated_at    INTEGER NOT NULL                   -- unix ms
) STRICT;

CREATE TABLE sessions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash   BLOB    NOT NULL UNIQUE,            -- SHA-256 of the opaque token; the token itself is never stored
    created_at   INTEGER NOT NULL,                   -- unix ms
    last_used_at INTEGER NOT NULL                    -- unix ms; a session unused for the idle timeout expires
) STRICT;

CREATE INDEX sessions_user_id ON sessions (user_id);
CREATE INDEX sessions_last_used_at ON sessions (last_used_at);
