// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	"github.com/IGlace/abyssreader-server-kit/internal/definitions"
	"github.com/IGlace/abyssreader-server-kit/internal/store"
)

const (
	definitionRestoreOverride = "allow-definition-rollback"
	definitionRestoreRecord   = "definition-restore"
)

type definitionState struct {
	localKey string
	registry *definitions.Key
	entries  []definitions.Entry
	damaged  bool
}

type definitionRestorePlan struct {
	InstallKey  bool             `json:"installKey"`
	ClearOffers bool             `json:"clearOffers"`
	LiveKey     *definitions.Key `json:"liveKey"`
}

func readDefinitionState(ctx context.Context, dir string, snapshot bool) (definitionState, error) {
	var state definitionState
	if err := readDefinitionRegistry(ctx, dir, snapshot, &state); err != nil {
		return state, err
	}
	if _, err := keyBackupInfo(dir); os.IsNotExist(err) {
		return state, nil
	} else if err != nil {
		return state, err
	}
	activeID := ""
	if state.registry != nil {
		activeID = state.registry.KeyID
	}
	key, err := definitions.LocalKeyID(definitions.KeyPath(dir), activeID)
	state.localKey = key
	return state, err
}

func readDefinitionRegistry(ctx context.Context, dir string, snapshot bool, state *definitionState) error {
	path := filepath.Join(dir, Database)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && !snapshot {
		return nil
	}
	if err != nil {
		return err
	}
	// Do not open a torn live database with SQLite if it does not even
	// contain a header. The restore path can still verify its local key.
	if !snapshot && info.Mode().IsRegular() && info.Size() < 512 {
		state.damaged = true
		return nil
	}
	db, err := OpenDB(path, snapshot)
	if err != nil {
		return err
	}
	defer db.Close()
	schema, err := store.AppliedVersion(ctx, db)
	if err == nil && schema < 13 {
		return nil
	}
	if err == nil {
		var key definitions.Key
		key, err = definitions.ActiveKey(ctx, db)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err == nil {
			state.registry = &key
			state.entries, err = definitions.Manifest(ctx, db)
		}
	}
	if !snapshot && corruptSQLite(err) {
		state.registry = nil
		state.damaged = true
		return nil
	}
	return err
}

func planDefinitionRestore(ctx context.Context, live, stage string, allow bool) (definitionRestorePlan, error) {
	var plan definitionRestorePlan
	current, err := readDefinitionState(ctx, live, false)
	if err != nil {
		return plan, err
	}
	archived, err := readDefinitionState(ctx, stage, true)
	if err != nil {
		return plan, err
	}
	if current.localKey == "" && current.registry == nil && !current.damaged {
		// There is no existing identity to protect.
		plan.InstallKey = archived.localKey != ""
		return plan, nil
	}
	// A legacy archive with neither publication nor local key can restore
	// ordinary data while retaining the current unpublished signing file.
	keyChanged := archived.localKey != current.localKey &&
		(archived.localKey != "" || archived.registry != nil || current.registry != nil)
	if keyChanged || !reflect.DeepEqual(archived.registry, current.registry) ||
		rollsBackDefinitionEntries(current.entries, archived.entries) {
		if !allow {
			return plan, errors.New("backup signing identity or definition versions differ from the live server; use restore --allow-definition-rollback to keep the live key and clear the archived definitions")
		}
		plan.ClearOffers = true
		plan.LiveKey = current.registry
	}
	return plan, nil
}

func rollsBackDefinitionEntries(live, archived []definitions.Entry) bool {
	offers := make(map[string]definitions.Entry, len(archived))
	for _, entry := range archived {
		offers[entry.SourceSlug] = entry
	}
	for _, prior := range live {
		next, exists := offers[prior.SourceSlug]
		if !exists || next.DefinitionVersion < prior.DefinitionVersion ||
			next.DefinitionVersion == prior.DefinitionVersion && next.Signature != prior.Signature {
			return true
		}
	}
	return false
}

func saveDefinitionRestorePlan(tx string, plan definitionRestorePlan) error {
	data, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(tx, ".definition-plan-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	return moveDurably(file.Name(), filepath.Join(tx, definitionRestoreRecord))
}

func loadDefinitionRestorePlan(tx, next string) (definitionRestorePlan, error) {
	var plan definitionRestorePlan
	raw, err := readRecordFile(filepath.Join(tx, definitionRestoreRecord))
	if os.IsNotExist(err) {
		// Older interrupted restores are safe to replay only when the payload
		// carries no key. Its private key must never be installed by default.
		staged, err := header(next)
		if err != nil {
			return plan, err
		}
		if staged.DefinitionKeySHA != "" {
			return plan, errors.New("interrupted keyed restore has no signing identity record; recover the original files and run restore again")
		}
		return plan, nil
	}
	if err != nil {
		return plan, err
	}
	if err := json.Unmarshal(raw, &plan); err != nil {
		return plan, errors.New("invalid signing identity restore record")
	}
	return plan, nil
}

func clearArchivedDefinitions(ctx context.Context, data string, liveKey *definitions.Key) error {
	db, err := OpenDB(filepath.Join(data, Database), false)
	if err != nil {
		return err
	}
	defer db.Close()
	schema, err := store.AppliedVersion(ctx, db)
	if err != nil || schema < 13 {
		return err
	}
	return definitions.ResetPublication(ctx, db, liveKey)
}
