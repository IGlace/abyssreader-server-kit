-- SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
-- Copyright 2026 Abyss Reader
--
-- Hosted chapters; `sqlc generate` turns them into internal/store/sqlcdb. Keep this
-- file ASCII-only: sqlc's sqlite parser breaks on multi-byte comments.

-- -- The hosted source --

-- name: GetHostedSource :one
SELECT * FROM sources WHERE hosted = 1;

-- name: InsertHostedSource :one
-- Its name, website and lang are filled in when it is served: the server's name and address,
-- and the lang of its source titles.
INSERT INTO sources (slug, name, website, lang, icon_url, hosted, created_at, updated_at)
VALUES (?, ?, '', 'en', NULL, 1, ?, ?)
RETURNING id;

-- name: ListHostedLangs :many
SELECT DISTINCT st.lang FROM source_titles st JOIN sources s ON s.id = st.source_id
WHERE s.hosted = 1 ORDER BY st.lang;

-- -- Hosted source titles: import --

-- name: ListHostedSourceTitlesOfTitle :many
SELECT st.id, st.lang FROM source_titles st JOIN sources s ON s.id = st.source_id
WHERE st.title_id = ? AND s.hosted = 1;

-- name: InsertHostedSourceTitle :one
INSERT INTO source_titles (title_id, source_id, url, lang, position) VALUES (?, ?, ?, ?, ?)
RETURNING id;

-- name: SetSourceTitleURL :exec
UPDATE source_titles SET url = ? WHERE id = ?;

-- name: PutHostedSourceTitle :exec
INSERT INTO hosted_source_titles (source_title_id, reading_mode, rights) VALUES (?, ?, ?)
ON CONFLICT (source_title_id) DO UPDATE SET reading_mode = excluded.reading_mode, rights = excluded.rights;

-- -- Chapters: import --

-- name: GetChapterByRef :one
SELECT * FROM chapters WHERE source_title_id = ? AND ref = ?;

-- name: InsertChapter :one
INSERT INTO chapters (source_title_id, ref, position, number, name, volume, credits, published_at, page_count, manifest_hash)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: UpdateChapter :exec
UPDATE chapters SET position = ?, number = ?, name = ?, volume = ?, credits = ?, published_at = ?,
    page_count = ?, manifest_hash = ?
WHERE id = ?;

-- name: SetChapterPosition :exec
UPDATE chapters SET position = ? WHERE id = ?;

-- name: ListChapterRefs :many
SELECT id, ref FROM chapters WHERE source_title_id = ?;

-- name: DeleteChapter :exec
DELETE FROM chapters WHERE id = ?;

-- name: DeleteChapterPages :exec
DELETE FROM chapter_pages WHERE chapter_id = ?;

-- name: InsertChapterPage :exec
INSERT INTO chapter_pages (chapter_id, position, file_sha256) VALUES (?, ?, ?);

-- -- Page files --

-- name: GetPageFile :one
SELECT * FROM page_files WHERE sha256 = ?;

-- name: ListPageImagesOfFile :many
SELECT * FROM page_images WHERE file_sha256 = ? ORDER BY slice;

-- name: PutPageFile :exec
INSERT INTO page_files (sha256, width, height) VALUES (?, ?, ?)
ON CONFLICT (sha256) DO UPDATE SET width = excluded.width, height = excluded.height;

-- name: DeletePageImagesOfFile :exec
DELETE FROM page_images WHERE file_sha256 = ?;

-- name: InsertPageImage :exec
INSERT INTO page_images (file_sha256, slice, image, width, height) VALUES (?, ?, ?, ?, ?);

-- name: ListUnusedPageFiles :many
SELECT f.sha256 FROM page_files f
WHERE NOT EXISTS (SELECT 1 FROM chapter_pages p WHERE p.file_sha256 = f.sha256);

-- name: ListImagesOfPageFile :many
SELECT image FROM page_images WHERE file_sha256 = ?;

-- name: DeletePageFile :exec
DELETE FROM page_files WHERE sha256 = ?;

-- name: ListPageImageNames :many
SELECT DISTINCT image FROM page_images;

-- name: SetManifestHash :exec
UPDATE titles SET manifest_hash = ? WHERE id = ?;

-- name: CountPageImageUse :one
SELECT count(*) FROM page_images WHERE image = ?;

-- -- Reading --

-- name: GetHostedSourceTitleID :one
SELECT st.id FROM source_titles st JOIN sources s ON s.id = st.source_id
WHERE st.id = ? AND s.hosted = 1;

-- name: CountChapters :one
SELECT count(*) FROM chapters WHERE source_title_id = ?;

-- name: ListChaptersByNumberAsc :many
-- Chapters without a number come last in both orders; ties go by the manifest's order.
SELECT * FROM chapters WHERE source_title_id = ?
ORDER BY number IS NULL, number ASC, position ASC, id ASC
LIMIT ? OFFSET ?;

-- name: ListChaptersByNumberDesc :many
-- Equal numbers keep the manifest's order here too.
SELECT * FROM chapters WHERE source_title_id = ?
ORDER BY number IS NULL, number DESC, position ASC, id ASC
LIMIT ? OFFSET ?;

-- name: GetChapter :one
SELECT * FROM chapters WHERE id = ?;

-- name: ListChapterImages :many
-- The limit is the import's cap on a chapter's images: the answer never grows past it.
SELECT i.image, i.width, i.height FROM chapter_pages p JOIN page_images i ON i.file_sha256 = p.file_sha256
WHERE p.chapter_id = ? ORDER BY p.position, i.slice
LIMIT ?;
