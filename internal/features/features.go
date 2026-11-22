// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package features registers the optional features of this kit version. Configuration,
// route registration and admin purge all enumerate the same definitions.
package features

import (
	"context"
	"database/sql"
	"fmt"
	"io"
)

const (
	HostedChapters = "hosted-chapters"
	Reports        = "reports"
	Backup         = "backup"
)

// Routes is implemented by the HTTP server, with the feature gate already bound.
type Routes interface {
	HostedRoutes()
	ReportRoutes()
	BackupRoutes()
}
type Definition struct {
	Name     string
	Register func(Routes)
	Purge    func(context.Context, *sql.DB, string, io.Writer) (string, error)
}

var Built = []Definition{
	{HostedChapters, Routes.HostedRoutes, purgeHosted},
	{Reports, Routes.ReportRoutes, purgeReports},
	{Backup, Routes.BackupRoutes, purgeBackup},
}

// Account backups are streamed. All restored rows belong to core library/sync.
func purgeBackup(context.Context, *sql.DB, string, io.Writer) (string, error) {
	return "stored_backup_files=0", nil
}

func purgeReports(ctx context.Context, db *sql.DB, _ string, _ io.Writer) (string, error) {
	r, err := db.ExecContext(ctx, "DELETE FROM title_reports")
	if err != nil {
		return "", err
	}
	n, err := r.RowsAffected()
	return fmt.Sprintf("reports=%d", n), err
}
