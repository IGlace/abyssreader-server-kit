// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/IGlace/abyssreader-server-kit/internal/usersync"
)

func TestReuploadAfterCancellationInsideRestoreTransaction(t *testing.T) {
	fixture := matchCatalog(t)
	var path string
	if err := fixture.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.Exec("INSERT INTO users(id,email,email_key,display_name,created_at,updated_at) VALUES(1,'a@x.test','a@x.test','reader',0,0)"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reached := make(chan struct{})
	sqliteDriver := &sqlite.Driver{}
	if err := sqliteDriver.RegisterScalarFunction("cancel_restore", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) {
		close(reached)
		cancel()
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	address := url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw&_txlock=immediate&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"}
	db := sql.OpenDB(restoreCutConnector{sqliteDriver, address.String()})
	t.Cleanup(func() { db.Close() })
	// database/sql can still be rolling back on cancellation when Restore
	// returns. The next operation must wait for that connection to be released.
	db.SetMaxOpenConns(1)
	const batch = usersync.MaxBatch
	const marks = 2*batch + 1
	// Updating the sequence is the last statement in the batch transaction.
	// The second batch has already inserted its marks, and has yet to commit.
	if _, err := db.Exec(fmt.Sprintf(`
		CREATE TRIGGER cut_restore AFTER UPDATE OF seq ON sync_accounts
		WHEN OLD.seq = %d AND NEW.seq = %d
		BEGIN SELECT cancel_restore(); END
	`, batch, 2*batch)); err != nil {
		t.Fatal(err)
	}

	// Match with the ordinary connection pool and write through the instrumented
	// pool for the same database file.
	svc := usersync.New(db)
	report, err := Restore(ctx, fixture, svc, 1, markedFile(marks), false, "")
	select {
	case <-reached:
	default:
		t.Fatalf("the restore never reached the second batch's sequence update: %+v, %v", report.ChapterMarks, err)
	}
	if !errors.Is(err, context.Canceled) || report.ChapterMarks.Restored != batch || report.ChapterMarks.Skipped != marks-batch {
		t.Fatalf("interrupted restore: %+v, %v", report.ChapterMarks, err)
	}
	cleanupCtx, stopCleanup := context.WithTimeout(t.Context(), 10*time.Second)
	defer stopCleanup()
	checkCommittedRestorePrefix(t, cleanupCtx, db, batch)
	if _, err := db.ExecContext(cleanupCtx, "DROP TRIGGER cut_restore"); err != nil {
		t.Fatal(err)
	}

	report, err = Restore(t.Context(), fixture, svc, 1, markedFile(marks), false, "")
	if err != nil || report.ChapterMarks.Restored != marks-batch || report.ChapterMarks.Skipped != batch {
		t.Fatalf("resubmitted restore: %+v, %v", report.ChapterMarks, err)
	}
	checkCommittedRestorePrefix(t, t.Context(), db, marks)
}

func checkCommittedRestorePrefix(t *testing.T, ctx context.Context, db *sql.DB, want int) {
	t.Helper()
	var count, last, account int
	err := db.QueryRowContext(ctx, `
		SELECT COUNT(*), MAX(seq), (SELECT seq FROM sync_accounts WHERE user_id=1)
		FROM sync_records WHERE user_id=1
	`).Scan(&count, &last, &account)
	if err != nil || count != want || last != want || account != want {
		t.Fatalf("commit has %d rows, last record %d, account sequence %d, error %v; want %d", count, last, account, err, want)
	}
}

// Give the test its own SQLite function without changing the default driver.
type restoreCutConnector struct {
	sqlite *sqlite.Driver
	dsn    string
}

func (c restoreCutConnector) Connect(context.Context) (driver.Conn, error) {
	return c.sqlite.Open(c.dsn)
}

func (c restoreCutConnector) Driver() driver.Driver { return c.sqlite }
