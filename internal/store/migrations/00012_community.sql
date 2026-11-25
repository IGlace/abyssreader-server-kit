-- SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
-- Copyright 2026 Abyss Reader

-- +goose Up
CREATE TABLE account_suspensions (
    user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    reason TEXT NOT NULL
) STRICT;

CREATE TABLE collections (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    slug TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    name_key TEXT NOT NULL,
    description TEXT,
    views INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX collections_account ON collections(user_id, updated_at, id);
CREATE INDEX collections_latest ON collections(updated_at, id);

CREATE TABLE collection_titles (
    collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
    title_id INTEGER NOT NULL REFERENCES titles(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    PRIMARY KEY (collection_id, title_id),
    UNIQUE (collection_id, position)
) STRICT;
CREATE INDEX collection_titles_title ON collection_titles(title_id);

CREATE TABLE comments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title_id INTEGER REFERENCES titles(id) ON DELETE CASCADE,
    collection_id INTEGER REFERENCES collections(id) ON DELETE CASCADE,
    parent_id INTEGER REFERENCES comments(id) ON DELETE SET NULL,
    text TEXT NOT NULL,
    image_url TEXT,
    spoiler INTEGER NOT NULL CHECK (spoiler IN (0,1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    CHECK ((title_id IS NULL) != (collection_id IS NULL))
) STRICT;
CREATE INDEX comments_account ON comments(user_id, id);
CREATE INDEX comments_titles ON comments(title_id, parent_id, created_at, id);
CREATE INDEX comments_collections ON comments(collection_id, parent_id, created_at, id);
CREATE INDEX comments_replies ON comments(parent_id, created_at, id);

CREATE TABLE comment_votes (
    comment_id INTEGER NOT NULL REFERENCES comments(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    vote TEXT NOT NULL CHECK (vote IN ('up','down')),
    PRIMARY KEY (comment_id, user_id)
) STRICT;
CREATE INDEX comment_votes_account ON comment_votes(user_id, comment_id);

CREATE TABLE collection_votes (
    collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    vote TEXT NOT NULL CHECK (vote IN ('up','down')),
    PRIMARY KEY (collection_id, user_id)
) STRICT;
CREATE INDEX collection_votes_account ON collection_votes(user_id, collection_id);

CREATE TABLE content_reports (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    comment_id INTEGER REFERENCES comments(id) ON DELETE CASCADE,
    collection_id INTEGER REFERENCES collections(id) ON DELETE CASCADE,
    reason TEXT NOT NULL,
    details TEXT,
    created_at INTEGER NOT NULL,
    closed INTEGER NOT NULL DEFAULT 0 CHECK (closed IN (0,1)),
    CHECK ((comment_id IS NULL) != (collection_id IS NULL))
) STRICT;
CREATE UNIQUE INDEX content_reports_open_comment ON content_reports(user_id, comment_id) WHERE comment_id IS NOT NULL AND closed=0;
CREATE UNIQUE INDEX content_reports_open_collection ON content_reports(user_id, collection_id) WHERE collection_id IS NOT NULL AND closed=0;
CREATE INDEX content_reports_account ON content_reports(user_id, closed, id);
CREATE INDEX content_reports_comment ON content_reports(comment_id, closed);
CREATE INDEX content_reports_collection ON content_reports(collection_id, closed);
CREATE INDEX content_reports_queue ON content_reports(closed, id DESC);
