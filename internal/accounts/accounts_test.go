// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IGlace/abyssreader-server-kit/internal/contracttest"
	"github.com/IGlace/abyssreader-server-kit/internal/password"
	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newService(t *testing.T) (*Service, *sql.DB, *clock) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "abyssreader.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	c := &clock{t: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)}
	s := New(db, 90*24*time.Hour)
	s.now = c.now
	return s, db, c
}

var ctx = context.Background()

func TestSignUpGivesAWorkingSession(t *testing.T) {
	s, _, _ := newService(t)
	sess, err := s.SignUp(ctx, "quietreader", "Reader@Example.com", "correct-horse-7")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Token == "" || sess.Account.ID < 1 || sess.Account.Email != "Reader@Example.com" || !sess.Account.HasPassword || sess.Account.AvatarURL != nil {
		t.Fatalf("%+v", sess)
	}
	acc, _, err := s.Authenticate(ctx, sess.Token)
	if err != nil || acc.ID != sess.Account.ID {
		t.Fatalf("Authenticate = %+v, %v", acc, err)
	}
	if _, err := s.SignUp(ctx, "other", "reader@example.COM", "correct-horse-7"); !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("same email in other case: %v, want ErrEmailTaken", err)
	}
}

func TestSignUpValidation(t *testing.T) {
	s, _, _ := newService(t)
	cases := []struct {
		name, email, password string
		field, code           string
	}{
		{"ab", "a@example.com", "correct-horse-7", "displayName", "too_short"},
		{strings.Repeat("a", 33), "a@example.com", "correct-horse-7", "displayName", "too_long"},
		{"bad<name>", "a@example.com", "correct-horse-7", "displayName", "invalid"},
		{" lead", "a@example.com", "correct-horse-7", "displayName", "invalid"},
		{"reader", "not-an-email", "correct-horse-7", "email", "invalid"},
		{"reader", "Reader <a@example.com>", "correct-horse-7", "email", "invalid"},
		{"reader", "ré@example.com", "correct-horse-7", "email", "invalid"},
		{"reader", "a@exa_mple.com", "correct-horse-7", "email", "invalid"},
		{"reader", "a@-example.com", "correct-horse-7", "email", "invalid"},
		{"reader", `"a\"b"@example.com`, "correct-horse-7", "email", "invalid"},
		{"reader", "a@example.com", "short", "password", "too_short"},
		{"reader", "a@example.com", strings.Repeat("p", 129), "password", "too_long"},
		{"reader", "a@example.com", "tab\tinside", "password", "invalid"},
	}
	for _, c := range cases {
		_, err := s.SignUp(ctx, c.name, c.email, c.password)
		var v *ValidationError
		if !errors.As(err, &v) || len(v.Fields) != 1 || v.Fields[0].Field != c.field || v.Fields[0].Code != c.code {
			t.Errorf("SignUp(%q, %q, %q) = %v; want %s %s", c.name, c.email, c.password, err, c.field, c.code)
		}
	}
}

// The contract's floor (owner decision 2026-11-19): every server accepts a display name of 3–32
// ASCII letters and digits and a password of 8–64 printable ASCII characters.
func TestSignUpAcceptsTheContractFloor(t *testing.T) {
	s, _, _ := newService(t)
	printable := make([]byte, 0, 95)
	for b := byte(0x20); b <= 0x7e; b++ {
		printable = append(printable, b)
	}
	inputs := []struct{ name, password string }{
		{"abc", "12345678"},
		{strings.Repeat("Z9", 16), string(printable[:64])},
		{"Reader2026", string(printable[31:95])},
		{"x1y", " leading and trailing spaces "},
	}
	for i, in := range inputs {
		email := "floor" + string(rune('a'+i)) + "@example.com"
		if _, err := s.SignUp(ctx, in.name, email, in.password); err != nil {
			t.Errorf("SignUp(%q, %q) = %v; the contract floor must be accepted", in.name, in.password, err)
		}
	}
	for _, e := range acceptedEmails {
		if _, err := s.SignUp(ctx, "anyemail", e, "correct-horse-7"); err != nil {
			t.Errorf("SignUp with email %q = %v; any syntactically valid email is accepted", e, err)
		}
	}
}

func TestSignIn(t *testing.T) {
	s, _, _ := newService(t)
	up, _ := s.SignUp(ctx, "quietreader", "reader@example.com", "correct-horse-7")
	in, err := s.SignIn(ctx, "READER@example.com", "correct-horse-7")
	if err != nil || in.Account.ID != up.Account.ID || in.Token == up.Token {
		t.Fatalf("SignIn = %+v, %v", in, err)
	}
	if _, err := s.SignIn(ctx, "reader@example.com", "wrong-password"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := s.SignIn(ctx, "nobody@example.com", "correct-horse-7"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("unknown email: %v", err)
	}
	// Both sessions work side by side.
	for _, tok := range []string{up.Token, in.Token} {
		if _, _, err := s.Authenticate(ctx, tok); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuthenticateRejectsUnknownAndIdleTokens(t *testing.T) {
	s, _, c := newService(t)
	if _, _, err := s.Authenticate(ctx, "not-a-token"); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("unknown token: %v", err)
	}
	up, _ := s.SignUp(ctx, "quietreader", "reader@example.com", "correct-horse-7")
	c.t = c.t.Add(89 * 24 * time.Hour)
	if _, _, err := s.Authenticate(ctx, up.Token); err != nil {
		t.Fatalf("used within the idle timeout: %v", err)
	}
	// Use moved the idle window: 89 more days still work.
	c.t = c.t.Add(89 * 24 * time.Hour)
	if _, _, err := s.Authenticate(ctx, up.Token); err != nil {
		t.Fatalf("used again within the idle timeout: %v", err)
	}
	c.t = c.t.Add(91 * 24 * time.Hour)
	if _, _, err := s.Authenticate(ctx, up.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("idle past the timeout: %v", err)
	}
}

func TestUpdateProfile(t *testing.T) {
	s, _, _ := newService(t)
	up, _ := s.SignUp(ctx, "quietreader", "reader@example.com", "correct-horse-7")
	avatar := "https://images.example.org/a.png"
	acc, err := s.UpdateProfile(ctx, up.Account.ID, "Night Owl", &avatar)
	if err != nil || acc.DisplayName != "Night Owl" || acc.AvatarURL == nil || *acc.AvatarURL != avatar {
		t.Fatalf("%+v, %v", acc, err)
	}
	acc, err = s.UpdateProfile(ctx, up.Account.ID, "Night Owl", nil)
	if err != nil || acc.AvatarURL != nil {
		t.Fatalf("clearing the avatar: %+v, %v", acc, err)
	}
	userinfo := "https://user:secret@images.example.org/a.png"
	var vu *ValidationError
	if _, err := s.UpdateProfile(ctx, up.Account.ID, "Night Owl", &userinfo); !errors.As(err, &vu) || vu.Fields[0].Code != "invalid" {
		t.Fatalf("avatar with userinfo (N3): %v", err)
	}
	bad := "http://images.example.org/a.png"
	var v *ValidationError
	if _, err := s.UpdateProfile(ctx, up.Account.ID, "x", &bad); !errors.As(err, &v) || len(v.Fields) != 2 {
		t.Fatalf("two bad fields: %v", err)
	}
}

func TestChangePasswordRevokesEveryOtherSession(t *testing.T) {
	s, _, _ := newService(t)
	up, _ := s.SignUp(ctx, "quietreader", "reader@example.com", "correct-horse-7")
	other, _ := s.SignIn(ctx, "reader@example.com", "correct-horse-7")
	if _, err := s.ChangePassword(ctx, up.Account.ID, "wrong-password", "new-password-8"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong current password: %v", err)
	}
	var v *ValidationError
	if _, err := s.ChangePassword(ctx, up.Account.ID, "correct-horse-7", "short"); !errors.As(err, &v) || v.Fields[0].Field != "newPassword" {
		t.Fatalf("short new password: %v", err)
	}
	sess, err := s.ChangePassword(ctx, up.Account.ID, "correct-horse-7", "new-password-8")
	if err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{up.Token, other.Token} {
		if _, _, err := s.Authenticate(ctx, old); !errors.Is(err, ErrSessionInvalid) {
			t.Fatalf("old token after a password change: %v", err)
		}
	}
	if _, _, err := s.Authenticate(ctx, sess.Token); err != nil {
		t.Fatalf("new token: %v", err)
	}
	if _, err := s.SignIn(ctx, "reader@example.com", "correct-horse-7"); !errors.Is(err, ErrWrongPassword) {
		t.Fatal("the old password still signs in")
	}
	if _, err := s.SignIn(ctx, "reader@example.com", "new-password-8"); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteRemovesEverythingTheAccountCreated(t *testing.T) {
	s, db, _ := newService(t)
	keep, _ := s.SignUp(ctx, "someoneelse", "keep@example.com", "correct-horse-7")
	up, _ := s.SignUp(ctx, "quietreader", "reader@example.com", "correct-horse-7")
	s.SignIn(ctx, "reader@example.com", "correct-horse-7")
	wrong := "wrong-password"
	if err := s.Delete(ctx, up.Account.ID, &wrong); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("wrong password: %v", err)
	}
	if err := s.Delete(ctx, up.Account.ID, nil); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("null password on an account with one: %v", err)
	}
	right := "correct-horse-7"
	if err := s.Delete(ctx, up.Account.ID, &right); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, up.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("token after deletion: %v", err)
	}
	if _, err := s.SignIn(ctx, "reader@example.com", "correct-horse-7"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("sign-in after deletion: %v", err)
	}
	if n := rowsOf(t, db, up.Account.ID); n != 0 {
		t.Fatalf("%d rows of the deleted account are left", n)
	}
	if n := rowsOf(t, db, keep.Account.ID); n == 0 {
		t.Fatal("the other account's rows went too")
	}
	// The id is never reused.
	again, _ := s.SignUp(ctx, "quietreader", "reader@example.com", "correct-horse-7")
	if again.Account.ID <= up.Account.ID {
		t.Fatalf("id %d reused or lower than %d", again.Account.ID, up.Account.ID)
	}
}

func TestAdminResetPasswordAndDisable(t *testing.T) {
	s, _, _ := newService(t)
	up, _ := s.SignUp(ctx, "quietreader", "reader@example.com", "correct-horse-7")
	temp, until, err := s.ResetPassword(ctx, "READER@example.com")
	if err != nil || len(temp) < 16 || !until.Equal(s.now().Add(TemporaryPasswordLifetime)) {
		t.Fatalf("ResetPassword = %q, %v, %v", temp, until, err)
	}
	if _, _, err := s.Authenticate(ctx, up.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("a reset password must sign every session out")
	}
	if _, err := s.SignIn(ctx, "reader@example.com", "correct-horse-7"); !errors.Is(err, ErrWrongPassword) {
		t.Fatal("the old password still works")
	}
	in, err := s.SignIn(ctx, "reader@example.com", temp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangePassword(ctx, in.Account.ID, temp, "chosen-by-user-9"); err != nil {
		t.Fatalf("changing the temporary password: %v", err)
	}

	in, _ = s.SignIn(ctx, "reader@example.com", "chosen-by-user-9")
	if err := s.Disable(ctx, "reader@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(ctx, in.Token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("disabling must sign every session out")
	}
	if _, err := s.SignIn(ctx, "reader@example.com", "chosen-by-user-9"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("sign-in of a disabled account: %v", err)
	}
	if _, err := s.SignIn(ctx, "reader@example.com", "wrong-password"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("a wrong password must not reveal that the account is disabled: %v", err)
	}
	if err := s.Enable(ctx, "reader@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SignIn(ctx, "reader@example.com", "chosen-by-user-9"); err != nil {
		t.Fatalf("after enable: %v", err)
	}
	for _, f := range []func(context.Context, string) error{s.Disable, s.Enable, func(c context.Context, e string) error { _, _, err := s.ResetPassword(c, e); return err }} {
		if err := f(ctx, "nobody@example.com"); !errors.Is(err, ErrNoAccount) {
			t.Fatalf("unknown email: %v", err)
		}
	}
}

func TestPurgeIdleSessions(t *testing.T) {
	s, db, c := newService(t)
	s.SignUp(ctx, "quietreader", "reader@example.com", "correct-horse-7")
	c.t = c.t.Add(91 * 24 * time.Hour)
	fresh, _ := s.SignUp(ctx, "nightowl", "owl@example.com", "correct-horse-7")
	n, err := s.PurgeIdleSessions(ctx)
	if err != nil || n != 1 {
		t.Fatalf("PurgeIdleSessions = %d, %v; want 1", n, err)
	}
	var left int
	db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&left)
	if left != 1 {
		t.Fatalf("%d sessions left, want 1", left)
	}
	if _, _, err := s.Authenticate(ctx, fresh.Token); err != nil {
		t.Fatal(err)
	}
}

func rowsOf(t *testing.T, db *sql.DB, userID int64) int {
	t.Helper()
	n, err := store.AccountRowCount(ctx, db, userID)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A reset password is temporary (S4): it works for TemporaryPasswordLifetime, then answers like
// a wrong password everywhere; the user's own password change clears the expiry.
func TestTemporaryPasswordExpires(t *testing.T) {
	s, _, c := newService(t)
	s.SignUp(ctx, "quietreader", "reader@example.com", "correct-horse-7")
	temp, _, err := s.ResetPassword(ctx, "reader@example.com")
	if err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(TemporaryPasswordLifetime - time.Minute)
	in, err := s.SignIn(ctx, "reader@example.com", temp)
	if err != nil {
		t.Fatalf("temporary password before it expires: %v", err)
	}
	c.t = c.t.Add(2 * time.Minute)
	if _, err := s.SignIn(ctx, "reader@example.com", temp); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("expired temporary password: %v, want ErrWrongPassword", err)
	}
	if _, err := s.ChangePassword(ctx, in.Account.ID, temp, "chosen-by-user-9"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("password change with an expired temporary password: %v", err)
	}
	if err := s.Delete(ctx, in.Account.ID, &temp); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("deletion with an expired temporary password: %v", err)
	}

	// A fresh reset, then the user's own password: no expiry any more.
	temp, _, _ = s.ResetPassword(ctx, "reader@example.com")
	in, _ = s.SignIn(ctx, "reader@example.com", temp)
	if _, err := s.ChangePassword(ctx, in.Account.ID, temp, "chosen-by-user-9"); err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(10 * TemporaryPasswordLifetime)
	if _, err := s.SignIn(ctx, "reader@example.com", "chosen-by-user-9"); err != nil {
		t.Fatalf("own password long after the reset: %v", err)
	}
}

// N1: a sign-in that checked the old password while a password change committed creates no
// session.
func TestSignInCreatesNoSessionAfterAConcurrentPasswordChange(t *testing.T) {
	s, db, _ := newService(t)
	up, _ := s.SignUp(ctx, "quietreader", "reader@example.com", "correct-horse-7")
	s.beforeSession = func() {
		if _, err := s.ChangePassword(ctx, up.Account.ID, "correct-horse-7", "battery-staple-8"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SignIn(ctx, "reader@example.com", "correct-horse-7"); !errors.Is(err, ErrWrongPassword) {
		t.Fatalf("sign-in racing a password change: %v, want ErrWrongPassword", err)
	}
	s.beforeSession = nil
	var n int
	db.QueryRow(`SELECT count(*) FROM sessions`).Scan(&n)
	if n != 1 {
		t.Fatalf("%d sessions; want only the password change's", n)
	}
	// The same for a disable that commits in between.
	s.beforeSession = func() { s.Disable(ctx, "reader@example.com") }
	if _, err := s.SignIn(ctx, "reader@example.com", "battery-staple-8"); err == nil {
		t.Fatal("sign-in racing a disable created a session")
	}
}

// S2: with every hashing slot taken, the account rules answer ErrBusy (503), not a wrong password.
func TestBusyHashingIsNotAWrongPassword(t *testing.T) {
	s, _, _ := newService(t)
	s.SignUp(ctx, "quietreader", "reader@example.com", "correct-horse-7")
	release := password.HoldAllSlotsForTest()
	defer release()
	if _, err := s.SignIn(ctx, "reader@example.com", "correct-horse-7"); !errors.Is(err, password.ErrBusy) {
		t.Fatalf("sign-in with no hashing slot: %v", err)
	}
	if _, err := s.SignIn(ctx, "nobody@example.com", "correct-horse-7"); !errors.Is(err, password.ErrBusy) {
		t.Fatalf("unknown email with no hashing slot: %v", err)
	}
	if _, err := s.SignUp(ctx, "nightowl", "owl@example.com", "correct-horse-7"); !errors.Is(err, password.ErrBusy) {
		t.Fatalf("sign-up with no hashing slot: %v", err)
	}
}

var acceptedEmails = []string{
	"a@b", "first.last+tag@sub.example.co.uk", "o'brien@example.ie", "x_y-z@example-mail.org",
	"UPPER@EXAMPLE.COM", "a!#$%&*/=?^`{|}~@example.com", "1@2.example", "a@" + strings.Repeat("x", 63) + ".example",
}

var refusedEmails = []string{
	"", "plain", "@example.com", "a@", "a@@example.com", "a@exa_mple.com", "a@-example.com", "a@example-.com",
	"a@example..com", "a@.example.com", "a@example.com.", ".a@example.com", "a.@example.com", "a..b@example.com",
	`"a\"b"@example.com`, `"quoted local"@example.com`, `a\b@example.com`, "x@[192.0.2.1]", "a b@example.com",
	"Reader <a@example.com>", "a@example.com (comment)", "ré@example.com", "a@exämple.com",
	strings.Repeat("a", 65) + "@example.com", "a@" + strings.Repeat("x", 64) + ".example",
}

// S3: every email the kit accepts must also pass the contract's `format: email`, or the
// account's own /me answer would break the contract.
func TestEveryAcceptedEmailPassesTheContractFormat(t *testing.T) {
	c := contracttest.Load(t)
	check := func(email string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"id": 1, "displayName": "reader", "email": email, "avatarUrl": nil, "hasPassword": true})
		req := httptest.NewRequest("GET", "https://server.example.org/api/v1/me", nil)
		resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Request: req}
		c.Check(t, req, resp, body)
	}
	for _, e := range acceptedEmails {
		if code := CheckEmail(e); code != "" {
			t.Errorf("CheckEmail(%q) = %s; want accepted", e, code)
		}
		check(e)
	}
	for _, e := range refusedEmails {
		if code := CheckEmail(e); code == "" {
			t.Errorf("CheckEmail(%q) accepted it", e)
		}
	}
	// Random addresses from the characters an email may hold: whatever the kit accepts passes.
	r := rand.New(rand.NewPCG(457, 1))
	const chars = "abcXYZ019.-_+!#$%&'*/=?^`{|}~@\"\\ []()<>,;:é"
	accepted := 0
	for i := 0; i < 3000; i++ {
		b := make([]rune, 3+r.IntN(20))
		for j := range b {
			b[j] = []rune(chars)[r.IntN(len([]rune(chars)))]
		}
		e := string(b)
		if r.IntN(2) == 0 {
			e += "@example.com"
		}
		if CheckEmail(e) == "" {
			accepted++
			check(e)
		}
	}
	if accepted < 50 {
		t.Fatalf("only %d random addresses accepted; the sample says little", accepted)
	}
}
