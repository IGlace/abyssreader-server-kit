-- SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
-- Copyright 2026 Abyss Reader
--
-- A password set by `admin users reset-password` is temporary ("one-time password"): it
-- signs in until this time (unix ms) and is cleared by the user's own password change.
-- Additive only.

-- +goose Up
ALTER TABLE users ADD COLUMN password_temporary_until INTEGER;
