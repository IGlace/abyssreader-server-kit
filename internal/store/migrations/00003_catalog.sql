-- SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
-- Copyright 2026 Abyss Reader
--
-- The catalog: sources, titles and their source titles, filled only by
-- `abyssreader-server admin import <folder>` from the import manifest.
-- The kit never crawls. No row here belongs to an account, so nothing cascades from users.
--
-- Ids use AUTOINCREMENT: an id is never reused for another thing of the same kind (contract v1).
-- Times are unix milliseconds.

-- +goose Up
CREATE TABLE sources (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    slug       TEXT    NOT NULL UNIQUE,           -- the contract's SourceSlug
    name       TEXT    NOT NULL,
    website    TEXT    NOT NULL,                  -- https origin; identifies the source across servers
    lang       TEXT    NOT NULL,
    icon_url   TEXT,                              -- an https URL the manifest gives, served as it is
    hosted     INTEGER NOT NULL DEFAULT 0,        -- 1 only for the server itself
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

-- Mihon/Tachiyomi numeric source ids (decimal text), for restoring their backups.
-- One id names one source.
CREATE TABLE source_tachiyomi_ids (
    tachiyomi_id TEXT    NOT NULL PRIMARY KEY,
    source_id    INTEGER NOT NULL REFERENCES sources (id) ON DELETE CASCADE
) STRICT;

CREATE INDEX source_tachiyomi_ids_source ON source_tachiyomi_ids (source_id);

CREATE TABLE genres (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    slug TEXT    NOT NULL UNIQUE,                 -- made from the name; a genre is found by it
    name TEXT    NOT NULL
) STRICT;

CREATE TABLE authors (
    id   INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT    NOT NULL UNIQUE
) STRICT;

CREATE TABLE titles (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    ref           TEXT    NOT NULL UNIQUE,        -- the operator's stable reference from the manifest
    slug          TEXT    NOT NULL UNIQUE,
    name          TEXT    NOT NULL,
    name_key      TEXT    NOT NULL,               -- lower-cased name, for q and sort=alphabetical
    description   TEXT,
    status        TEXT    NOT NULL,
    rating        REAL,                           -- 0–10, or NULL when unrated
    views         INTEGER NOT NULL DEFAULT 0,
    cover_source  TEXT,                           -- the manifest's cover: an https URL, or "file:<path>"
    cover_image   TEXT,                           -- the stored file under <data>/covers/; NULL = no cover
    cover_error   TEXT,                           -- why the last cover download failed; NULL when it did not
    manifest_hash TEXT    NOT NULL,               -- SHA-256 of the title's manifest entry: unchanged entries are skipped
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL                -- when the title last changed in the catalog (sort=latest)
) STRICT;

CREATE INDEX titles_updated_at ON titles (updated_at);
CREATE INDEX titles_name_key ON titles (name_key);
CREATE INDEX titles_cover_pending ON titles (cover_source) WHERE cover_image IS NULL;

CREATE TABLE title_alt_names (
    title_id INTEGER NOT NULL REFERENCES titles (id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    name     TEXT    NOT NULL,
    name_key TEXT    NOT NULL,                    -- lower-cased, for q
    PRIMARY KEY (title_id, position)
) STRICT;

CREATE TABLE title_genres (
    title_id INTEGER NOT NULL REFERENCES titles (id) ON DELETE CASCADE,
    genre_id INTEGER NOT NULL REFERENCES genres (id),
    position INTEGER NOT NULL,
    PRIMARY KEY (title_id, genre_id)
) STRICT;

CREATE INDEX title_genres_genre ON title_genres (genre_id);

CREATE TABLE title_authors (
    title_id  INTEGER NOT NULL REFERENCES titles (id) ON DELETE CASCADE,
    position  INTEGER NOT NULL,
    author_id INTEGER NOT NULL REFERENCES authors (id),
    role      TEXT    NOT NULL,
    PRIMARY KEY (title_id, position)
) STRICT;

-- One title on one source. A source title belongs to one title: (source, url) is unique, so two
-- refs can never claim the same source title. Its id stays the same across imports.
CREATE TABLE source_titles (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    title_id  INTEGER NOT NULL REFERENCES titles (id) ON DELETE CASCADE,
    source_id INTEGER NOT NULL REFERENCES sources (id),
    url       TEXT    NOT NULL,
    lang      TEXT    NOT NULL,
    position  INTEGER NOT NULL,
    UNIQUE (source_id, url)
) STRICT;

CREATE INDEX source_titles_title ON source_titles (title_id);
