// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRecoveryAuditAppearsInLocalStatusWithoutPendingSwap(t *testing.T) {
	ctx := context.Background()
	env := envOf(baseEnv(t))
	db, err := openDatabase(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	var out, stderr bytes.Buffer
	for _, args := range [][]string{{"begin", "upgrade", "14", "operation.abcdefgh"}, {"abandon"}, {"ready"}} {
		if code := maintenance(ctx, args, nil, &out, &stderr, env); code != 0 {
			t.Fatalf("%v: %d (%s)", args, code, stderr.String())
		}
	}
	out.Reset()
	// The status command still reports a transport failure when the service is stopped.
	status(ctx, &out, &stderr, env)
	if !strings.Contains(out.String(), "Last abandonment") || !strings.Contains(out.String(), "upgrade") || !strings.Contains(out.String(), "operation.abcdefgh") {
		t.Fatal(out.String())
	}
}
