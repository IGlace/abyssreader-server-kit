-- SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
-- Copyright 2026 Abyss Reader
--
-- Account and session queries; `sqlc generate` turns them into internal/store/sqlcdb.

-- name: CreateUser :one
INSERT INTO users (email, email_key, display_name, avatar_url, password_hash, created_at, updated_at)
VALUES (?, ?, ?, NULL, ?, ?, ?)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = ?;

-- name: GetUserByEmailKey :one
SELECT * FROM users WHERE email_key = ?;

-- name: UpdateProfile :one
UPDATE users SET display_name = ?, avatar_url = ?, updated_at = ? WHERE id = ?
RETURNING *;

-- name: SetPasswordHash :execrows
-- password_temporary_until: NULL for the user's own password, a time for a reset password.
UPDATE users SET password_hash = ?, password_temporary_until = ?, updated_at = ? WHERE id = ?;

-- name: SetDisabledAt :execrows
UPDATE users SET disabled_at = ?, updated_at = ? WHERE id = ?;

-- name: DeleteUser :execrows
DELETE FROM users WHERE id = ?;

-- name: CreateSessionIfPasswordUnchanged :execrows
-- Sign-in's session, created only while the password it checked is still the account's and the
-- account is not disabled: a password change, reset or disable that commits between the check
-- and this insert makes it insert nothing.
INSERT INTO sessions (user_id, token_hash, created_at, last_used_at)
SELECT users.id, sqlc.arg(token_hash), sqlc.arg(now), sqlc.arg(now)
FROM users
WHERE users.id = sqlc.arg(user_id) AND users.password_hash = sqlc.arg(password_hash) AND users.disabled_at IS NULL;

-- name: CreateSession :exec
INSERT INTO sessions (user_id, token_hash, created_at, last_used_at) VALUES (?, ?, ?, ?);

-- name: GetSessionByTokenHash :one
SELECT sqlc.embed(sessions), sqlc.embed(users)
FROM sessions JOIN users ON users.id = sessions.user_id
WHERE sessions.token_hash = ?;

-- name: TouchSession :exec
UPDATE sessions SET last_used_at = ? WHERE id = ?;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = ?;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = ?;

-- name: DeleteIdleSessions :execrows
DELETE FROM sessions WHERE last_used_at < ?;
