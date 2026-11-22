// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package schemas holds the kit's own JSON schemas, embedded in the binary so that the server
// checks a file against exactly the schema published beside it.
package schemas

import (
	"bytes"
	_ "embed"
	"sync"

	"github.com/pb33f/jsonschema/v6"
)

//go:embed import-manifest.schema.json
var importManifest []byte

// ImportManifestJSON is the text of import-manifest.schema.json.
func ImportManifestJSON() []byte { return bytes.Clone(importManifest) }

var (
	compileOnce sync.Once
	compiled    *jsonschema.Schema
	compileErr  error
)

// ImportManifest is the compiled import manifest schema (draft 2020-12, formats not asserted:
// the schema uses patterns only).
func ImportManifest() (*jsonschema.Schema, error) {
	compileOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(importManifest))
		if err != nil {
			compileErr = err
			return
		}
		c := jsonschema.NewCompiler()
		const url = "urn:abyssreader:server-kit:import-manifest"
		if err := c.AddResource(url, doc); err != nil {
			compileErr = err
			return
		}
		compiled, compileErr = c.Compile(url)
	})
	return compiled, compileErr
}
