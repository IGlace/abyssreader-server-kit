// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package kit embeds the parts of the server contract (contract/) the server itself checks
// requests against, so that it uses exactly the published files and keeps no copy of them.
package kit

import (
	"bytes"
	_ "embed"
)

//go:embed contract/sync/sync.schema.json
var syncSchema []byte

// SyncSchemaJSON is the text of contract/sync/sync.schema.json.
func SyncSchemaJSON() []byte { return bytes.Clone(syncSchema) }

//go:embed contract/definition.schema.json
var definitionSchema []byte

// DefinitionSchemaJSON is the published schema for a resolved source definition.
func DefinitionSchemaJSON() []byte { return bytes.Clone(definitionSchema) }
