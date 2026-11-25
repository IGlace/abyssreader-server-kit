// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package accounts holds the core accounts of server contract v1: sign-up with a
// working session at once, sign-in, the profile, password change and account deletion, plus the
// operator's admin commands. There are no admin accounts and no email.
package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/password"
	"github.com/IGlace/abyssreader-server-kit/internal/store/sqlcdb"
)

var (
	ErrEmailTaken     = errors.New("accounts: an account with this email already exists")
	ErrWrongPassword  = errors.New("accounts: the email or password is wrong")
	ErrPasswordNotSet = errors.New("accounts: the account has no password")
	ErrDisabled       = errors.New("accounts: the account is disabled")
	ErrSessionInvalid = errors.New("accounts: the session token is not accepted")
	ErrNoAccount      = errors.New("accounts: no account with this email")
)

// Account is an account as its owner sees it (the contract's Account).
type Account struct {
	ID          int64
	DisplayName string
	Email       string
	AvatarURL   *string
	HasPassword bool
}

// Session is a new session token and its account.
type Session struct {
	Token   string
	Account Account
}

// Service runs the account rules on the database.
type Service struct {
	db   *sql.DB
	q    *sqlcdb.Queries
	idle time.Duration
	now  func() time.Time

	beforeSession func() // test hook: runs between sign-in's password check and its session
}

// New returns the account service. A session that is not used for idle stops working.
func New(db *sql.DB, idle time.Duration) *Service {
	return &Service{db: db, q: sqlcdb.New(db), idle: idle, now: time.Now}
}

// touchEvery limits how often a session's last use is written.
const touchEvery = time.Hour

func (s *Service) ms() int64 { return s.now().UnixMilli() }

// SignUp creates an account and its first session. Any syntactically valid email is accepted
// and never checked for deliverability.
func (s *Service) SignUp(ctx context.Context, displayName, email, pw string) (Session, error) {
	var v ValidationError
	v.check("displayName", CheckDisplayName(displayName))
	v.check("email", CheckEmail(email))
	v.check("password", CheckPassword(pw))
	if v.failed() {
		return Session{}, &v
	}
	hash, err := password.Hash(pw)
	if err != nil {
		return Session{}, err
	}
	var sess Session
	err = s.inTx(ctx, func(_ *sql.Tx, q *sqlcdb.Queries) error {
		now := s.ms()
		u, err := q.CreateUser(ctx, sqlcdb.CreateUserParams{
			Email: email, EmailKey: EmailKey(email), DisplayName: displayName,
			PasswordHash: sql.NullString{String: hash, Valid: true}, CreatedAt: now, UpdatedAt: now,
		})
		if isUniqueViolation(err) {
			return ErrEmailTaken
		}
		if err != nil {
			return err
		}
		token, err := s.newSession(ctx, q, u.ID)
		sess = Session{Token: token, Account: toAccount(u)}
		return err
	})
	return sess, err
}

// SignIn checks an email and password and starts a new session. An unknown email, a wrong
// password and an expired temporary password are the same ErrWrongPassword; a disabled account
// is ErrDisabled only once its password was right, so the status is never told to someone
// without the password. With every hashing slot taken it is password.ErrBusy.
func (s *Service) SignIn(ctx context.Context, email, pw string) (Session, error) {
	u, err := s.q.GetUserByEmailKey(ctx, EmailKey(email))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !u.PasswordHash.Valid) {
		if err := password.VerifyDummy(pw); err != nil {
			return Session{}, err
		}
		return Session{}, ErrWrongPassword
	}
	if err != nil {
		return Session{}, err
	}
	if err := s.checkPassword(u, pw); err != nil {
		return Session{}, err
	}
	if u.DisabledAt.Valid {
		return Session{}, ErrDisabled
	}
	if s.beforeSession != nil {
		s.beforeSession()
	}
	token, b := newToken()
	err = s.inTx(ctx, func(tx *sql.Tx, q *sqlcdb.Queries) error {
		n, err := q.CreateSessionIfPasswordUnchanged(ctx, sqlcdb.CreateSessionIfPasswordUnchangedParams{
			TokenHash: b, Now: s.ms(), UserID: u.ID, PasswordHash: u.PasswordHash,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			// The password changed or the account was disabled since the check.
			return ErrWrongPassword
		}
		// An error rolls back the new session. Suspension and session creation
		// take the same SQLite write lock so no token can outlive suspension.
		return suspension(ctx, tx, u.ID)
	})
	if err != nil {
		return Session{}, err
	}
	return Session{Token: token, Account: toAccount(u)}, nil
}

// checkPassword is nil when pw is the account's password and, for a temporary one, has not
// expired.
func (s *Service) checkPassword(u sqlcdb.User, pw string) error {
	ok, err := password.Verify(pw, u.PasswordHash.String)
	if errors.Is(err, password.ErrBusy) {
		return err
	}
	if err != nil {
		return fmt.Errorf("account %d: %w", u.ID, err)
	}
	if !ok || (u.PasswordTemporaryUntil.Valid && s.ms() > u.PasswordTemporaryUntil.Int64) {
		return ErrWrongPassword
	}
	return nil
}

// Authenticate returns the account and session id of a session token, or ErrSessionInvalid
// when the token is unknown, idle for too long, or its account is disabled or deleted.
func (s *Service) Authenticate(ctx context.Context, token string) (Account, int64, error) {
	row, err := s.q.GetSessionByTokenHash(ctx, hashToken(token))
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, 0, ErrSessionInvalid
	}
	if err != nil {
		return Account{}, 0, err
	}
	now := s.now()
	last := time.UnixMilli(row.Session.LastUsedAt)
	if now.Sub(last) > s.idle {
		if err := s.q.DeleteSession(ctx, row.Session.ID); err != nil {
			return Account{}, 0, err
		}
		return Account{}, 0, ErrSessionInvalid
	}
	if row.User.DisabledAt.Valid {
		return Account{}, 0, ErrSessionInvalid
	}
	if now.Sub(last) >= touchEvery {
		if err := s.q.TouchSession(ctx, sqlcdb.TouchSessionParams{LastUsedAt: now.UnixMilli(), ID: row.Session.ID}); err != nil {
			return Account{}, 0, err
		}
	}
	return toAccount(row.User), row.Session.ID, nil
}

// UpdateProfile replaces the display name and avatar (nil clears it).
func (s *Service) UpdateProfile(ctx context.Context, userID int64, displayName string, avatarURL *string) (Account, error) {
	var v ValidationError
	v.check("displayName", CheckDisplayName(displayName))
	if avatarURL != nil {
		v.check("avatarUrl", CheckHTTPSURL(*avatarURL))
	}
	if v.failed() {
		return Account{}, &v
	}
	avatar := sql.NullString{}
	if avatarURL != nil {
		avatar = sql.NullString{String: *avatarURL, Valid: true}
	}
	u, err := s.q.UpdateProfile(ctx, sqlcdb.UpdateProfileParams{DisplayName: displayName, AvatarUrl: avatar, UpdatedAt: s.ms(), ID: userID})
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrSessionInvalid
	}
	if err != nil {
		return Account{}, err
	}
	return toAccount(u), nil
}

// ChangePassword checks the current password, sets the new one, ends every session of the
// account and returns a new one, which replaces the caller's.
func (s *Service) ChangePassword(ctx context.Context, userID int64, current, next string) (Session, error) {
	u, err := s.q.GetUserByID(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrSessionInvalid
	}
	if err != nil {
		return Session{}, err
	}
	if !u.PasswordHash.Valid {
		return Session{}, ErrPasswordNotSet
	}
	if err := s.checkPassword(u, current); err != nil {
		return Session{}, err
	}
	var v ValidationError
	v.check("newPassword", CheckPassword(next))
	if v.failed() {
		return Session{}, &v
	}
	hash, err := password.Hash(next)
	if err != nil {
		return Session{}, err
	}
	var sess Session
	err = s.inTx(ctx, func(tx *sql.Tx, q *sqlcdb.Queries) error {
		var suspended *Suspended
		if err := suspension(ctx, tx, u.ID); err != nil {
			if errors.As(err, &suspended) {
				return ErrSessionInvalid
			}
			return err
		}
		// The user's own password: a temporary password's expiry is cleared.
		if _, err := q.SetPasswordHash(ctx, sqlcdb.SetPasswordHashParams{PasswordHash: sql.NullString{String: hash, Valid: true}, UpdatedAt: s.ms(), ID: u.ID}); err != nil {
			return err
		}
		if err := q.DeleteUserSessions(ctx, u.ID); err != nil {
			return err
		}
		token, err := s.newSession(ctx, q, u.ID)
		u.PasswordHash = sql.NullString{String: hash, Valid: true}
		u.PasswordTemporaryUntil = sql.NullInt64{}
		sess = Session{Token: token, Account: toAccount(u)}
		return err
	})
	return sess, err
}

// Delete deletes the account and, through the schema's ON DELETE CASCADE, everything it created.
// An account with a password confirms with it; one without (Google sign-in, later)
// is deleted with the session alone.
func (s *Service) Delete(ctx context.Context, userID int64, pw *string) error {
	u, err := s.q.GetUserByID(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSessionInvalid
	}
	if err != nil {
		return err
	}
	if u.PasswordHash.Valid {
		if pw == nil {
			return ErrWrongPassword
		}
		if err := s.checkPassword(u, *pw); err != nil {
			return err
		}
	}
	_, err = s.q.DeleteUser(ctx, u.ID)
	return err
}

// TemporaryPasswordLifetime is how long a password from `admin users reset-password` signs in.
const TemporaryPasswordLifetime = 72 * time.Hour

// ResetPassword gives the account with this email a new random temporary password, which works
// until the returned time, and ends its sessions. It returns the password, which the operator
// passes on because the kit has no email; the user then chooses their own.
func (s *Service) ResetPassword(ctx context.Context, email string) (string, time.Time, error) {
	u, err := s.byEmail(ctx, email)
	if err != nil {
		return "", time.Time{}, err
	}
	temp, err := password.Temporary()
	if err != nil {
		return "", time.Time{}, err
	}
	hash, err := password.Hash(temp)
	if err != nil {
		return "", time.Time{}, err
	}
	until := s.now().Add(TemporaryPasswordLifetime)
	err = s.inTx(ctx, func(_ *sql.Tx, q *sqlcdb.Queries) error {
		if _, err := q.SetPasswordHash(ctx, sqlcdb.SetPasswordHashParams{
			PasswordHash:           sql.NullString{String: hash, Valid: true},
			PasswordTemporaryUntil: sql.NullInt64{Int64: until.UnixMilli(), Valid: true},
			UpdatedAt:              s.ms(), ID: u.ID,
		}); err != nil {
			return err
		}
		return q.DeleteUserSessions(ctx, u.ID)
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return temp, until, nil
}

// Disable switches the account off: its sessions end and sign-in answers account_disabled.
func (s *Service) Disable(ctx context.Context, email string) error {
	u, err := s.byEmail(ctx, email)
	if err != nil {
		return err
	}
	return s.inTx(ctx, func(_ *sql.Tx, q *sqlcdb.Queries) error {
		now := s.ms()
		if _, err := q.SetDisabledAt(ctx, sqlcdb.SetDisabledAtParams{DisabledAt: sql.NullInt64{Int64: now, Valid: true}, UpdatedAt: now, ID: u.ID}); err != nil {
			return err
		}
		return q.DeleteUserSessions(ctx, u.ID)
	})
}

// Enable switches a disabled account on again.
func (s *Service) Enable(ctx context.Context, email string) error {
	u, err := s.byEmail(ctx, email)
	if err != nil {
		return err
	}
	_, err = s.q.SetDisabledAt(ctx, sqlcdb.SetDisabledAtParams{DisabledAt: sql.NullInt64{}, UpdatedAt: s.ms(), ID: u.ID})
	return err
}

// PurgeIdleSessions deletes the sessions that are past the idle timeout.
func (s *Service) PurgeIdleSessions(ctx context.Context) (int64, error) {
	return s.q.DeleteIdleSessions(ctx, s.now().Add(-s.idle).UnixMilli())
}

func (s *Service) byEmail(ctx context.Context, email string) (sqlcdb.User, error) {
	u, err := s.q.GetUserByEmailKey(ctx, EmailKey(email))
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNoAccount
	}
	return u, err
}

// newSession stores a new random session token (only its SHA-256) and returns the token.
func (s *Service) newSession(ctx context.Context, q *sqlcdb.Queries, userID int64) (string, error) {
	token, h := newToken()
	now := s.ms()
	err := q.CreateSession(ctx, sqlcdb.CreateSessionParams{UserID: userID, TokenHash: h, CreatedAt: now, LastUsedAt: now})
	return token, err
}

// newToken returns a random 256-bit session token and the SHA-256 the server stores.
func newToken() (string, []byte) {
	b := make([]byte, 32)
	rand.Read(b) // crypto/rand.Read never fails (Go 1.24+)
	token := base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token)
}

func (s *Service) inTx(ctx context.Context, f func(*sql.Tx, *sqlcdb.Queries) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := f(tx, s.q.WithTx(tx)); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// EmailKey is the form an email is looked up by: sign-in and uniqueness ignore case.
func EmailKey(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func toAccount(u sqlcdb.User) Account {
	a := Account{ID: u.ID, DisplayName: u.DisplayName, Email: u.Email, HasPassword: u.PasswordHash.Valid}
	if u.AvatarUrl.Valid {
		v := u.AvatarUrl.String
		a.AvatarURL = &v
	}
	return a
}
