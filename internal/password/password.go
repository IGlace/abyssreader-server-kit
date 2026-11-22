// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package password hashes account passwords with Argon2id and makes the temporary passwords
// that `admin users reset-password` prints.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters: the OWASP minimum (19 MiB, 2 passes, 1 lane), which fits the simple
// setup's 512 MB–1 GB machines. They are stored in each hash, so they can be raised later.
const (
	memoryKiB = 19 * 1024
	passes    = 2
	lanes     = 1
	saltLen   = 16
	keyLen    = 32
)

var errMalformed = errors.New("password: malformed hash")

// ErrBusy means every hashing slot stayed taken for the whole wait: the server answers
// 503 unavailable instead of queueing more hashes than its memory holds.
var ErrBusy = errors.New("password: too many password checks at once")

// slots caps the Argon2id hashes running at once (each holds 19 MiB): with 4 slots, at most
// 76 MiB, whatever the number of clients.
var slots = make(chan struct{}, Slots())

// slotWait is how long a password check waits for a free slot.
var slotWait = 3 * time.Second

// inside is a test hook, told +1 / -1 around every hash.
var inside = func(int32) {}

// Slots is the number of hashes that may run at once: the CPUs Go uses, between 2 and 4.
func Slots() int { return min(4, max(2, runtime.GOMAXPROCS(0))) }

// argon2id runs one hash in a slot.
func argon2id(pw, salt []byte, t, m uint32, p uint8, n uint32) ([]byte, error) {
	timer := time.NewTimer(slotWait)
	defer timer.Stop()
	select {
	case slots <- struct{}{}:
	case <-timer.C:
		return nil, ErrBusy
	}
	defer func() { <-slots }()
	inside(1)
	defer inside(-1)
	return argon2.IDKey(pw, salt, t, m, p, n), nil
}

// Hash returns the Argon2id hash of pw as a PHC string.
func Hash(pw string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := argon2id([]byte(pw), salt, passes, memoryKiB, lanes, keyLen)
	if err != nil {
		return "", err
	}
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memoryKiB, passes, lanes, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Verify reports whether pw matches hash. An error means the hash itself is unusable.
func Verify(pw, hash string) (bool, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errMalformed
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errMalformed
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m == 0 || t == 0 || p == 0 {
		return false, errMalformed
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false, errMalformed
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil || len(want) < 16 {
		return false, errMalformed
	}
	got, err := argon2id([]byte(pw), salt, t, m, p, uint32(len(want)))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash is verified against when an email is unknown, so that a sign-in for an unknown email
// takes as long as one with a wrong password.
var dummyHash = func() string {
	salt := make([]byte, saltLen)
	rand.Read(salt)
	key := argon2.IDKey([]byte("abyssreader-dummy-password"), salt, passes, memoryKiB, lanes, keyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memoryKiB, passes, lanes, b64.EncodeToString(salt), b64.EncodeToString(key))
}()

// VerifyDummy spends the time of one Verify; it fails only with ErrBusy.
func VerifyDummy(pw string) error {
	_, err := Verify(pw, dummyHash)
	return err
}

// temporaryAlphabet has no look-alike characters (no 0/O, 1/l/I), so an operator can read the
// password out to a user.
const temporaryAlphabet = "abcdefghijkmnpqrstuvwxyzACDEFGHJKLMNPQRTUVWXYZ2345679"

// Temporary returns a random 20-character password in four groups of five, joined by '-'
// (about 113 bits).
func Temporary() (string, error) {
	var b strings.Builder
	buf := make([]byte, 1)
	for n := 0; n < 20; {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		// Rejection sampling keeps every character equally likely.
		if int(buf[0]) >= 256-256%len(temporaryAlphabet) {
			continue
		}
		if n > 0 && n%5 == 0 {
			b.WriteByte('-')
		}
		b.WriteByte(temporaryAlphabet[int(buf[0])%len(temporaryAlphabet)])
		n++
	}
	return b.String(), nil
}
