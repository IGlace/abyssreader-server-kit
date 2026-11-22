// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import "testing"

func TestOperationRotatesPermitsAndCanBeClearedTwice(t *testing.T) {
	data := t.TempDir()
	if _, err := PermitBoot(data); err == nil {
		t.Fatal("issued token for idle volume")
	}
	if err := BeginOperation(data); err != nil {
		t.Fatal(err)
	}
	if err := BeginOperation(data); err != nil {
		t.Fatal(err)
	}
	first, err := PermitBoot(data)
	if err != nil {
		t.Fatal(err)
	}
	next, err := PermitBoot(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckBoot(data, first); err == nil {
		t.Fatal("accepted superseded permit")
	}
	if err := CheckBoot(data, next); err != nil {
		t.Fatal(err)
	}
	if err := CheckBoot(data, next); err == nil {
		t.Fatal("replayed permit")
	}
	for i := 0; i < 2; i++ {
		if err := EndOperation(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := CheckBoot(data, ""); err != nil {
		t.Fatal(err)
	}
}

func TestReadyRefusesOrphanedVolumeOperation(t *testing.T) {
	data := t.TempDir()
	if err := BeginOperation(data); err != nil {
		t.Fatal(err)
	}
	if err := Ready(data); err == nil {
		t.Fatal("new operation can override an orphaned operation marker")
	}
}
