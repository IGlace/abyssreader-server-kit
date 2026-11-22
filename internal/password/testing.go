// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package password

import "time"

// HoldAllSlotsForTest takes every hashing slot and shortens the wait, so that the next hash
// answers ErrBusy at once. For tests only; the returned function gives everything back.
func HoldAllSlotsForTest() func() {
	old := slotWait
	slotWait = 10 * time.Millisecond
	for i := 0; i < Slots(); i++ {
		slots <- struct{}{}
	}
	return func() {
		for i := 0; i < Slots(); i++ {
			<-slots
		}
		slotWait = old
	}
}
