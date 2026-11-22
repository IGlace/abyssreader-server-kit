// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package api

import (
	"errors"
	"net/http"

	"github.com/IGlace/abyssreader-server-kit/internal/accounts"
	"github.com/IGlace/abyssreader-server-kit/internal/password"
)

// The contract's maximum lengths for sign-in fields (SignInRequest, ChangePasswordRequest,
// DeleteAccountRequest).
const (
	maxEmail    = 254
	maxPassword = 1024
)

// The contract's Account and Session.
type accountBody struct {
	ID          int64   `json:"id"`
	DisplayName string  `json:"displayName"`
	Email       string  `json:"email"`
	AvatarURL   *string `json:"avatarUrl"`
	HasPassword bool    `json:"hasPassword"`
}

type sessionBody struct {
	Token string      `json:"token"`
	User  accountBody `json:"user"`
}

func toAccountBody(a accounts.Account) accountBody {
	return accountBody{ID: a.ID, DisplayName: a.DisplayName, Email: a.Email, AvatarURL: a.AvatarURL, HasPassword: a.HasPassword}
}

func toSessionBody(s accounts.Session) sessionBody {
	return sessionBody{Token: s.Token, User: toAccountBody(s.Account)}
}

// signUp: POST /api/v1/auth/sign-up. A working session at once, no email step.
func (s *server) signUp(w http.ResponseWriter, r *http.Request) {
	if !s.Config.SignUpOpen {
		writeError(w, http.StatusForbidden, "sign_up_closed", "Sign-up is closed on this server.")
		return
	}
	if wait, ok := s.signUps.allow(s.clientKey(r)); !ok {
		writeRateLimited(w, wait)
		return
	}
	o, ok := readObject(w, r)
	if !ok {
		return
	}
	name, email, pw := o.str("displayName"), o.str("email"), o.str("password")
	o.check("displayName", accounts.CheckDisplayName(name))
	o.check("email", accounts.CheckEmail(email))
	o.check("password", accounts.CheckPassword(pw))
	if o.failed(w) {
		return
	}
	sess, err := s.Accounts.SignUp(r.Context(), name, email, pw)
	if errors.Is(err, accounts.ErrEmailTaken) {
		writeError(w, http.StatusConflict, "email_taken", "An account with this email already exists.")
		return
	}
	if s.failed(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, toSessionBody(sess))
}

// signIn: POST /api/v1/auth/sign-in.
func (s *server) signIn(w http.ResponseWriter, r *http.Request) {
	if wait, ok := s.signIns.allow(s.clientKey(r)); !ok {
		writeRateLimited(w, wait)
		return
	}
	o, ok := readObject(w, r)
	if !ok {
		return
	}
	email, pw := o.str("email"), o.str("password")
	o.notEmpty("email", email)
	o.notEmpty("password", pw)
	o.maxLen("email", email, maxEmail)
	o.maxLen("password", pw, maxPassword)
	if o.failed(w) {
		return
	}
	account := accounts.EmailKey(email)
	if wait, ok := s.accountFails.check(account); !ok {
		writeRateLimited(w, wait)
		return
	}
	sess, err := s.Accounts.SignIn(r.Context(), email, pw)
	if errors.Is(err, accounts.ErrWrongPassword) {
		s.accountFails.spend(account)
	}
	switch {
	case errors.Is(err, accounts.ErrDisabled):
		writeError(w, http.StatusForbidden, "account_disabled", "This account is disabled. Contact the server operator.")
	case !s.failed(w, err):
		writeJSON(w, http.StatusOK, toSessionBody(sess))
	}
}

// getMe: GET /api/v1/me.
func (s *server) getMe(w http.ResponseWriter, r *http.Request, c caller) {
	writeJSON(w, http.StatusOK, toAccountBody(c.account))
}

// updateMe: PUT /api/v1/me replaces the display name and avatar.
func (s *server) updateMe(w http.ResponseWriter, r *http.Request, c caller) {
	o, ok := readObject(w, r)
	if !ok {
		return
	}
	name, avatar := o.str("displayName"), o.nullableStr("avatarUrl")
	o.check("displayName", accounts.CheckDisplayName(name))
	if avatar != nil {
		o.check("avatarUrl", accounts.CheckHTTPSURL(*avatar))
	}
	if o.failed(w) {
		return
	}
	acc, err := s.Accounts.UpdateProfile(r.Context(), c.account.ID, name, avatar)
	if !s.failed(w, err) {
		writeJSON(w, http.StatusOK, toAccountBody(acc))
	}
}

// changePassword: PUT /api/v1/me/password. Every other session ends; the new token replaces
// the caller's.
func (s *server) changePassword(w http.ResponseWriter, r *http.Request, c caller) {
	if wait, ok := s.signIns.allow(s.clientKey(r)); !ok {
		writeRateLimited(w, wait)
		return
	}
	o, ok := readObject(w, r)
	if !ok {
		return
	}
	current, next := o.str("currentPassword"), o.str("newPassword")
	o.maxLen("currentPassword", current, maxPassword)
	o.maxLen("newPassword", next, maxPassword)
	if o.failed(w) {
		return
	}
	account := accounts.EmailKey(c.account.Email)
	if wait, ok := s.accountFails.check(account); !ok {
		writeRateLimited(w, wait)
		return
	}
	sess, err := s.Accounts.ChangePassword(r.Context(), c.account.ID, current, next)
	if errors.Is(err, accounts.ErrWrongPassword) {
		s.accountFails.spend(account)
	}
	switch {
	case errors.Is(err, accounts.ErrPasswordNotSet):
		writeError(w, http.StatusBadRequest, "password_not_set", "This account has no password.")
	case !s.failed(w, err):
		writeJSON(w, http.StatusOK, toSessionBody(sess))
	}
}

// deleteAccount: POST /api/v1/me/deletion deletes the account and everything it created.
func (s *server) deleteAccount(w http.ResponseWriter, r *http.Request, c caller) {
	if wait, ok := s.signIns.allow(s.clientKey(r)); !ok {
		writeRateLimited(w, wait)
		return
	}
	o, ok := readObject(w, r)
	if !ok {
		return
	}
	pw := o.nullableStr("password")
	if pw != nil {
		o.maxLen("password", *pw, maxPassword)
	}
	if o.failed(w) {
		return
	}
	account := accounts.EmailKey(c.account.Email)
	if wait, ok := s.accountFails.check(account); !ok {
		writeRateLimited(w, wait)
		return
	}
	err := s.Accounts.Delete(r.Context(), c.account.ID, pw)
	if errors.Is(err, accounts.ErrWrongPassword) {
		s.accountFails.spend(account)
	}
	if !s.failed(w, err) {
		s.Logger.Info("account deleted", "account", c.account.ID)
		w.WriteHeader(http.StatusNoContent)
	}
}

// failed answers the errors every account route shares and reports whether there was one.
func (s *server) failed(w http.ResponseWriter, err error) bool {
	var v *accounts.ValidationError
	switch {
	case err == nil:
		return false
	case errors.As(err, &v):
		fields := make([]fieldError, len(v.Fields))
		for i, f := range v.Fields {
			fields[i] = fieldError{Field: f.Field, Code: f.Code}
		}
		writeValidation(w, fields)
	case errors.Is(err, accounts.ErrWrongPassword):
		writeError(w, http.StatusBadRequest, "wrong_password", "The email or password is wrong.")
	case errors.Is(err, accounts.ErrSessionInvalid):
		writeSessionInvalid(w)
	case errors.Is(err, password.ErrBusy):
		s.Logger.Warn("every password-hashing slot stayed taken; answered 503")
		writeBusy(w)
	default:
		s.storeFailed(w, "account request", err)
	}
	return true
}
