-- SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
-- Copyright 2026 Abyss Reader
--
-- Catalog queries; `sqlc generate` turns them into internal/store/sqlcdb. The search
-- itself (GET /api/v1/titles) is built in internal/catalog, because its filters are optional
-- and repeated.

-- -- Sources --

-- name: GetSourceBySlug :one
SELECT * FROM sources WHERE slug = ?;

-- name: InsertSource :one
INSERT INTO sources (slug, name, website, lang, icon_url, hosted, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, 0, ?, ?)
RETURNING id;

-- name: UpdateSource :exec
UPDATE sources SET name = ?, website = ?, lang = ?, icon_url = ?, updated_at = ? WHERE id = ?;

-- name: ListTachiyomiIDsOfSource :many
SELECT tachiyomi_id FROM source_tachiyomi_ids WHERE source_id = ? ORDER BY tachiyomi_id;

-- name: DeleteTachiyomiIDsOfSource :exec
DELETE FROM source_tachiyomi_ids WHERE source_id = ?;

-- name: ListTachiyomiIDClaims :many
SELECT m.tachiyomi_id, s.slug FROM source_tachiyomi_ids m
JOIN sources s ON s.id = m.source_id ORDER BY m.tachiyomi_id;

-- name: InsertTachiyomiID :exec
INSERT INTO source_tachiyomi_ids (tachiyomi_id, source_id) VALUES (?, ?);

-- name: ListAllSources :many
SELECT id, slug, lang, hosted FROM sources;

-- name: ListUsedSources :many
-- GET /api/v1/sources: every source the catalog's source titles point to (the hosted one too).
SELECT s.* FROM sources s
WHERE EXISTS (SELECT 1 FROM source_titles st WHERE st.source_id = s.id)
ORDER BY s.name, s.slug;

-- -- Titles: import --

-- name: GetTitleByRef :one
SELECT * FROM titles WHERE ref = ?;

-- name: GetTitleRefBySlug :one
SELECT ref FROM titles WHERE slug = ?;

-- name: InsertTitle :one
INSERT INTO titles (ref, slug, name, name_key, description, status, rating, cover_source, manifest_hash, created_at, updated_at, publication_year)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: UpdateTitle :exec
UPDATE titles SET slug = ?, name = ?, name_key = ?, description = ?, status = ?, rating = ?,
    cover_source = ?, manifest_hash = ?, updated_at = ?, publication_year = ?
WHERE id = ?;

-- name: DeleteAltNames :exec
DELETE FROM title_alt_names WHERE title_id = ?;

-- name: InsertAltName :exec
INSERT INTO title_alt_names (title_id, position, name, name_key) VALUES (?, ?, ?, ?);

-- name: DeleteTitleGenres :exec
DELETE FROM title_genres WHERE title_id = ?;

-- name: PutGenre :one
-- A genre is found by its slug; the first name it was imported with stays.
INSERT INTO genres (slug, name) VALUES (?, ?)
ON CONFLICT (slug) DO UPDATE SET slug = excluded.slug
RETURNING id;

-- name: InsertTitleGenre :exec
INSERT INTO title_genres (title_id, genre_id, position) VALUES (?, ?, ?);

-- name: DeleteTitleAuthors :exec
DELETE FROM title_authors WHERE title_id = ?;

-- name: PutAuthor :one
INSERT INTO authors (name) VALUES (?)
ON CONFLICT (name) DO UPDATE SET name = excluded.name
RETURNING id;

-- name: InsertTitleAuthor :exec
INSERT INTO title_authors (title_id, position, author_id, role) VALUES (?, ?, ?, ?);

-- name: GetSourceTitleOwner :one
-- Which title a source title belongs to, with that title's ref.
SELECT st.id, st.title_id, t.ref FROM source_titles st JOIN titles t ON t.id = st.title_id
WHERE st.source_id = ? AND st.url = ?;

-- name: ListSourceTitleIDsOfTitle :many
-- The title's source titles on other sources; its hosted ones are imported apart.
SELECT st.id FROM source_titles st JOIN sources s ON s.id = st.source_id
WHERE st.title_id = ? AND s.hosted = 0;

-- name: InsertSourceTitle :exec
INSERT INTO source_titles (title_id, source_id, url, lang, position) VALUES (?, ?, ?, ?, ?);

-- name: UpdateSourceTitle :exec
UPDATE source_titles SET lang = ?, position = ? WHERE id = ?;

-- name: DeleteSourceTitle :exec
DELETE FROM source_titles WHERE id = ?;

-- name: DeleteTitle :execrows
DELETE FROM titles WHERE id = ?;

-- -- Covers --

-- name: SetCover :exec
-- The caller moves updated_at only when the stored image changes.
UPDATE titles SET cover_image = ?, cover_error = ?, updated_at = ? WHERE id = ?;

-- name: ListPendingCovers :many
-- Titles whose cover is an https URL that has no stored image yet.
SELECT id, ref, cover_source, cover_image FROM titles
WHERE cover_image IS NULL AND cover_source LIKE 'https://%'
ORDER BY id;

-- name: ListCoverImageNames :many
SELECT DISTINCT CAST(cover_image AS TEXT) AS name FROM titles WHERE cover_image IS NOT NULL;

-- name: CountCoverUse :one
SELECT count(*) FROM titles WHERE cover_image = ?;

-- -- Titles: reading --

-- name: GetTitle :one
SELECT * FROM titles WHERE id = ?;

-- name: CountView :exec
UPDATE titles SET views = views + 1 WHERE id = ?;

-- name: AltNamesOf :many
SELECT title_id, name FROM title_alt_names WHERE title_id IN (sqlc.slice(ids)) ORDER BY title_id, position;

-- name: GenresOf :many
SELECT tg.title_id, g.id, g.name, g.slug FROM title_genres tg JOIN genres g ON g.id = tg.genre_id
WHERE tg.title_id IN (sqlc.slice(ids)) ORDER BY tg.title_id, tg.position;

-- name: AuthorsOf :many
SELECT ta.title_id, a.id, a.name, ta.role FROM title_authors ta JOIN authors a ON a.id = ta.author_id
WHERE ta.title_id IN (sqlc.slice(ids)) ORDER BY ta.title_id, ta.position;

-- name: SourceTitlesOf :many
SELECT st.title_id, st.id, s.slug, st.url, st.lang, s.hosted, h.reading_mode, h.rights
FROM source_titles st JOIN sources s ON s.id = st.source_id
LEFT JOIN hosted_source_titles h ON h.source_title_id = st.id
WHERE st.title_id IN (sqlc.slice(ids)) ORDER BY st.title_id, s.hosted DESC, st.position;

-- name: ListGenreIDs :many
SELECT id FROM genres;
