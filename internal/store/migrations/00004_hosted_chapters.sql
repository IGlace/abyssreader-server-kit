-- SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
-- Copyright 2026 Abyss Reader
--
-- Hosted chapters: the server is one source of its own (sources.hosted = 1, slug
-- "hosted", made by the first import of a hosted title). Each hosted title has one source
-- title on it per language, with its chapters and their pages. Filled only by `admin import`.
-- The data stays when the optional feature hosted-chapters is switched off.
-- No row here belongs to an account, so nothing cascades from users.

-- +goose Up
-- The reader hint and the rights of a hosted source title (its source_titles row has the rest;
-- its url is /source-titles/{id}).
CREATE TABLE hosted_source_titles (
    source_title_id INTEGER PRIMARY KEY REFERENCES source_titles (id) ON DELETE CASCADE,
    reading_mode    TEXT,                 -- paged_ltr, paged_rtl, long_strip; NULL = no hint
    rights          TEXT                  -- JSON {holder, licence, licenceUrl, attribution, changes}; NULL = none
) STRICT;

-- One chapter of a hosted source title. ref is the manifest's key; the id (and so the chapter's
-- url /chapters/{id}, its identity for reading marks and sync) stays the same across imports.
CREATE TABLE chapters (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    source_title_id INTEGER NOT NULL REFERENCES source_titles (id) ON DELETE CASCADE,
    ref             TEXT    NOT NULL,
    position        INTEGER NOT NULL,     -- the manifest's order; breaks ties of number
    number          REAL,
    name            TEXT,
    volume          REAL,
    credits         TEXT,
    published_at    INTEGER NOT NULL,     -- unix ms
    page_count      INTEGER NOT NULL,     -- served images, slices included
    manifest_hash   TEXT    NOT NULL,     -- SHA-256 of the entry and its page files: unchanged chapters are skipped
    UNIQUE (source_title_id, ref)
) STRICT;

CREATE INDEX chapters_by_number ON chapters (source_title_id, number, position, id);

-- One page file of an import folder, known by the SHA-256 of its bytes, with its own size.
CREATE TABLE page_files (
    sha256 TEXT    PRIMARY KEY,
    width  INTEGER NOT NULL,
    height INTEGER NOT NULL
) STRICT;

-- What a page file is served as: the file itself (one row), or its slices from top to bottom.
-- image is the stored file under <data>/pages/ (the SHA-256 of its own bytes + extension).
CREATE TABLE page_images (
    file_sha256 TEXT    NOT NULL REFERENCES page_files (sha256) ON DELETE CASCADE,
    slice       INTEGER NOT NULL,
    image       TEXT    NOT NULL,
    width       INTEGER NOT NULL,
    height      INTEGER NOT NULL,
    PRIMARY KEY (file_sha256, slice)
) STRICT;

CREATE INDEX page_images_image ON page_images (image);

-- A chapter's page files in reading order. A page file no chapter uses is deleted with its
-- images at the end of an import or a removal.
CREATE TABLE chapter_pages (
    chapter_id  INTEGER NOT NULL REFERENCES chapters (id) ON DELETE CASCADE,
    position    INTEGER NOT NULL,
    file_sha256 TEXT    NOT NULL REFERENCES page_files (sha256),
    PRIMARY KEY (chapter_id, position)
) STRICT;

CREATE INDEX chapter_pages_file ON chapter_pages (file_sha256);
