// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package config

import "testing"

func TestAccountBackupConfig(t *testing.T) {
	m := minimal()
	m["ABYSS_FEATURES"] = "backup"
	c, err := Load(env(m))
	if err != nil || !c.FeatureOn("backup") {
		t.Fatal("backup switch", err)
	}
	m["ABYSS_ACCOUNT_BACKUP_MAX_MIB"] = "9"
	if _, err = Load(env(m)); err == nil {
		t.Fatal("accepted an upload limit below contract floor")
	}
}
