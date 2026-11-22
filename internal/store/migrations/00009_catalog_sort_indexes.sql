-- SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
-- Copyright 2026 Abyss Reader

-- +goose Up
-- Both sort directions retain the same ascending title-ID tie-break.
CREATE INDEX titles_rating_desc ON titles (rating DESC, id ASC);
CREATE INDEX titles_rating_asc ON titles (rating ASC, id ASC);
CREATE INDEX titles_views_desc ON titles (views DESC, id ASC);
CREATE INDEX titles_views_asc ON titles (views ASC, id ASC);
