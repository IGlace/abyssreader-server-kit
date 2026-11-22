-- SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
-- Copyright 2026 Abyss Reader

-- +goose Up
CREATE TABLE successful_e2e_migration (value TEXT NOT NULL);
INSERT INTO successful_e2e_migration VALUES ('new release');
