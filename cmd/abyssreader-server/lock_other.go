// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

//go:build !unix

package main

import "errors"

// The image and the supported hosts are Linux; elsewhere the admin lock is not taken.
var errLocked = errors.New("locked")

func lockData(string) (func(), error) { return func() {}, nil }
