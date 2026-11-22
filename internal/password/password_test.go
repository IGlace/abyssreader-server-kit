// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package password

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHashAndVerify(t *testing.T) {
	h, err := Hash("correct-horse-7")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$") {
		t.Fatalf("hash %q is not an argon2id PHC string", h)
	}
	if strings.Contains(h, "correct-horse-7") {
		t.Fatal("hash contains the password")
	}
	if ok, err := Verify("correct-horse-7", h); !ok || err != nil {
		t.Fatalf("Verify(right) = %v, %v", ok, err)
	}
	if err := VerifyDummy("x"); err != nil {
		t.Fatalf("VerifyDummy = %v", err)
	}
	if ok, _ := Verify("correct-horse-8", h); ok {
		t.Fatal("Verify(wrong) = true")
	}
	h2, _ := Hash("correct-horse-7")
	if h == h2 {
		t.Fatal("two hashes of one password are equal: the salt is not random")
	}
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	for _, h := range []string{"", "plain", "$argon2id$v=19$m=1,t=1,p=1$xx", "$bcrypt$x$y$z$w"} {
		if ok, err := Verify("x", h); ok || err == nil {
			t.Errorf("Verify(%q) = %v, %v; want false and an error", h, ok, err)
		}
	}
}

func TestTemporaryPassword(t *testing.T) {
	p, err := Temporary()
	if err != nil {
		t.Fatal(err)
	}
	if len(p) < 16 {
		t.Fatalf("temporary password %q is too short", p)
	}
	for _, r := range p {
		if r < 0x21 || r > 0x7e {
			t.Fatalf("temporary password %q has a character outside printable ASCII", p)
		}
	}
	q, _ := Temporary()
	if p == q {
		t.Fatal("two temporary passwords are equal")
	}
}

// At most Slots hashes run at once; a caller that finds no free slot within the wait gets
// ErrBusy instead of queueing without bound (S2: each hash holds 19 MiB).
func TestHashingIsCappedWithBusyAnswer(t *testing.T) {
	old := slotWait
	slotWait = 50 * time.Millisecond
	defer func() { slotWait = old }()
	if Slots() < 2 || Slots() > 4 {
		t.Fatalf("Slots() = %d, want 2–4", Slots())
	}
	h, _ := Hash("correct-horse-7")
	for i := 0; i < Slots(); i++ {
		slots <- struct{}{} // every slot taken
	}
	if _, err := Hash("x-password"); !errors.Is(err, ErrBusy) {
		t.Fatalf("Hash with no free slot: %v, want ErrBusy", err)
	}
	if _, err := Verify("correct-horse-7", h); !errors.Is(err, ErrBusy) {
		t.Fatalf("Verify with no free slot: %v, want ErrBusy", err)
	}
	if err := VerifyDummy("x"); !errors.Is(err, ErrBusy) {
		t.Fatalf("VerifyDummy with no free slot: %v, want ErrBusy", err)
	}
	<-slots // one frees up
	if ok, err := Verify("correct-horse-7", h); !ok || err != nil {
		t.Fatalf("Verify with a free slot: %v, %v", ok, err)
	}
	for i := 1; i < Slots(); i++ {
		<-slots
	}
}

func TestParallelHashesNeverExceedTheSlots(t *testing.T) {
	var running, peak atomic.Int32
	inside = func(delta int32) {
		n := running.Add(delta)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
	}
	defer func() { inside = func(int32) {} }()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); Hash("correct-horse-7") }()
	}
	wg.Wait()
	if p := peak.Load(); p > int32(Slots()) || p < 1 {
		t.Fatalf("peak of %d hashes at once, cap %d", p, Slots())
	}
}
