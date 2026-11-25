// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package accounts

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/IGlace/abyssreader-server-kit/internal/store/sqlcdb"
)

const MaxSuspensionReasonLength = 500

var ErrSuspensionReason = errors.New("the suspension reason must be at most 500 characters and contain no control characters")

// Suspended is returned only when a sign-in would otherwise succeed.
type Suspended struct{ Reason string }

func (e *Suspended) Error() string { return "accounts: the account is suspended" }

func suspension(ctx context.Context, tx *sql.Tx, userID int64) error {
	var reason string
	err := tx.QueryRowContext(ctx, "SELECT reason FROM account_suspensions WHERE user_id=?", userID).Scan(&reason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return &Suspended{Reason: reason}
}

func accountID(ctx context.Context, tx *sql.Tx, account string) (int64, error) {
	var id int64
	if n, err := strconv.ParseInt(account, 10, 64); err == nil && n > 0 {
		err = tx.QueryRowContext(ctx, "SELECT id FROM users WHERE id=?", n).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNoAccount
		}
		return id, err
	}
	err := tx.QueryRowContext(ctx, "SELECT id FROM users WHERE email_key=?", EmailKey(account)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNoAccount
	}
	return id, err
}

func (s *Service) Suspend(ctx context.Context, account, reason string) error {
	if !utf8.ValidString(reason) || utf8.RuneCountInString(reason) > MaxSuspensionReasonLength || strings.ContainsFunc(reason, unicode.IsControl) {
		return ErrSuspensionReason
	}
	return s.inTx(ctx, func(tx *sql.Tx, q *sqlcdb.Queries) error {
		id, err := accountID(ctx, tx, account)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO account_suspensions (user_id,reason) VALUES (?,?) ON CONFLICT DO UPDATE SET reason=excluded.reason", id, strings.TrimSpace(reason)); err != nil {
			return err
		}
		return q.DeleteUserSessions(ctx, id)
	})
}

func (s *Service) Unsuspend(ctx context.Context, account string) error {
	return s.inTx(ctx, func(tx *sql.Tx, _ *sqlcdb.Queries) error {
		id, err := accountID(ctx, tx, account)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "DELETE FROM account_suspensions WHERE user_id=?", id)
		return err
	})
}
