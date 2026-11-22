-- SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
-- Copyright 2026 Abyss Reader

-- +goose Up
ALTER TABLE titles ADD COLUMN publication_year INTEGER CHECK (publication_year BETWEEN 1 AND 9999);
