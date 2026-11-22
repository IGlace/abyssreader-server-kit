-- SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
-- Copyright 2026 Abyss Reader

-- +goose Up
CREATE TABLE sync_applied_operations (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    op_id TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status = 'applied'),
    reason TEXT NOT NULL,
    applied_at INTEGER NOT NULL,
    receipt_seq INTEGER NOT NULL,
    UNIQUE (user_id, receipt_seq),
    PRIMARY KEY (user_id, op_id)
) STRICT;

CREATE INDEX sync_applied_operations_expiry ON sync_applied_operations(applied_at);
