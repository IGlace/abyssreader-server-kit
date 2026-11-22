// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package contracttest holds the server's answers to the server contract in contract/openapi.yaml,
// for tests: every response must be declared for its operation, validate strictly against the
// schema (no undeclared field, every required field present, formats asserted), and carry only
// the error codes the contract lists for that status. It also records which operation answered
// which status, so a test can prove it covered every route.
package contracttest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/pb33f/jsonschema/v6"
	"github.com/pb33f/libopenapi"
	validator "github.com/pb33f/libopenapi-validator"
	"github.com/pb33f/libopenapi-validator/config"
	"github.com/pb33f/libopenapi/datamodel"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
)

// Contract is the loaded server contract.
type Contract struct {
	Version string // info.version, the contract version (MAJOR.MINOR)

	v     validator.Validator
	model *v3.Document

	mu   sync.Mutex
	seen map[string]map[int]map[string]bool // operationId → status → error code ("" for 2xx)
}

var (
	loadOnce sync.Once
	loaded   *Contract
	loadErr  error
)

// Dir is the repo's contract/ folder.
func Dir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "contract")
}

// Load reads contract/openapi.yaml once per test binary.
func Load(t testing.TB) *Contract {
	t.Helper()
	loadOnce.Do(func() { loaded, loadErr = load(Dir()) })
	if loadErr != nil {
		t.Fatalf("contract: %v", loadErr)
	}
	return loaded
}

func load(dir string) (*Contract, error) {
	b, err := os.ReadFile(filepath.Join(dir, "openapi.yaml"))
	if err != nil {
		return nil, err
	}
	doc, err := libopenapi.NewDocumentWithConfiguration(b, &datamodel.DocumentConfiguration{
		BasePath: dir, SpecFilePath: "openapi.yaml", AllowFileReferences: true,
	})
	if err != nil {
		return nil, err
	}
	m, err := doc.BuildV3Model()
	if err != nil {
		return nil, err
	}
	v, errs := validator.NewValidator(doc, config.WithStrictMode(), config.WithFormatAssertions())
	if len(errs) > 0 {
		return nil, fmt.Errorf("validator: %v", errs)
	}
	if ok, verrs := v.ValidateDocument(); !ok {
		return nil, fmt.Errorf("the contract itself is invalid: %v", verrs)
	}
	return &Contract{Version: m.Model.Info.Version, v: v, model: &m.Model, seen: map[string]map[int]map[string]bool{}}, nil
}

// Check holds one answer to the contract. body is the response body already read.
func (c *Contract) Check(t testing.TB, req *http.Request, resp *http.Response, body []byte) {
	t.Helper()
	where := req.Method + " " + req.URL.Path + " → " + strconv.Itoa(resp.StatusCode)
	op := c.operation(req.Method, req.URL.Path)
	if op == nil {
		t.Errorf("%s: no operation in the contract", where)
		return
	}
	status := strconv.Itoa(resp.StatusCode)
	declared, ok := op.Responses.Codes.Get(status)
	if !ok {
		if resp.StatusCode != 500 && resp.StatusCode != 503 {
			t.Errorf("%s: %s does not declare status %s", where, op.OperationId, status)
			return
		}
		declared = op.Responses.Default
	}

	// The sync routes' bodies are the sync schema's (sync/sync.schema.json). Its records declare
	// their key and data fields inside if/then branches, which the validator's strict mode does
	// not follow, so for them its strict "not declared" findings are dropped and the body is held
	// to the sync schema itself, which is strict on its own (unevaluatedProperties: false).
	syncDef := syncSchemaDef(declared)
	r := *resp
	r.Body = io.NopCloser(bytes.NewReader(body))
	if ok, errs := c.v.ValidateHttpResponse(req, &r); !ok {
		for _, e := range errs {
			if syncDef != "" && strings.Contains(e.Reason, "Strict mode") {
				continue
			}
			msg := e.Message + ": " + e.Reason
			for _, se := range e.SchemaValidationErrors {
				msg += "\n    " + se.FieldPath + ": " + se.Reason
			}
			t.Errorf("%s: %s", where, msg)
		}
	}

	if syncDef != "" {
		if err := validateSync(syncDef, body); err != nil {
			t.Errorf("%s: against sync.schema.json#/$defs/%s: %v", where, syncDef, err)
		}
	}

	code := ""
	switch {
	case resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified:
		if len(body) != 0 {
			t.Errorf("%s: a %d has a body: %s", where, resp.StatusCode, body)
		}
	case resp.StatusCode >= 400 && declaresNoErrorBody(declared):
		// The one response whose body is not the Error (the sync pull's 409, fixed by the sync
		// schema): its x-error-codes is an empty list, and the schema check above holds it.
	case resp.StatusCode >= 400:
		var e struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(body, &e); err != nil || e.Code == "" {
			t.Errorf("%s: the error body has no code: %s", where, body)
			break
		}
		code = e.Code
		var allowed []string
		if node := declared.Extensions.GetOrZero("x-error-codes"); node != nil {
			if err := node.Decode(&allowed); err != nil {
				t.Fatalf("%s: x-error-codes: %v", where, err)
			}
		}
		if !slices.Contains(allowed, code) {
			t.Errorf("%s: code %q is not one the contract lists for this status %v", where, code, allowed)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			if _, err := strconv.Atoi(resp.Header.Get("Retry-After")); err != nil {
				t.Errorf("%s: a 429 needs Retry-After in seconds, got %q", where, resp.Header.Get("Retry-After"))
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen[op.OperationId] == nil {
		c.seen[op.OperationId] = map[int]map[string]bool{}
	}
	if c.seen[op.OperationId][resp.StatusCode] == nil {
		c.seen[op.OperationId][resp.StatusCode] = map[string]bool{}
	}
	c.seen[op.OperationId][resp.StatusCode][code] = true
}

// syncSchemaDef is the sync schema definition a response's JSON body refers to ("pullResponse"),
// or "" for a response of another schema.
func syncSchemaDef(r *v3.Response) string {
	if r == nil || r.Content == nil {
		return ""
	}
	mt := r.Content.GetOrZero("application/json")
	if mt == nil || mt.Schema == nil {
		return ""
	}
	const prefix = "sync/sync.schema.json#/$defs/"
	if ref := mt.Schema.GetReference(); strings.HasPrefix(ref, prefix) {
		return strings.TrimPrefix(ref, prefix)
	}
	return ""
}

var (
	syncOnce    sync.Once
	syncSchemas map[string]*jsonschema.Schema
	syncErr     error
)

// validateSync holds body to one definition of contract/sync/sync.schema.json.
func validateSync(def string, body []byte) error {
	syncOnce.Do(func() {
		b, err := os.ReadFile(filepath.Join(Dir(), "sync", "sync.schema.json"))
		if err != nil {
			syncErr = err
			return
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			syncErr = err
			return
		}
		comp := jsonschema.NewCompiler()
		const url = "https://abyssreader.com/schemas/sync.schema.json"
		if syncErr = comp.AddResource(url, doc); syncErr != nil {
			return
		}
		syncSchemas = map[string]*jsonschema.Schema{}
		for _, d := range []string{"pushResponse", "pullResponse", "pullConflict"} {
			if syncSchemas[d], syncErr = comp.Compile(url + "#/$defs/" + d); syncErr != nil {
				return
			}
		}
	})
	if syncErr != nil {
		return syncErr
	}
	schema, ok := syncSchemas[def]
	if !ok {
		return fmt.Errorf("no compiled definition %s", def)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
	if err != nil {
		return err
	}
	return schema.Validate(v)
}

// declaresNoErrorBody reports whether a response lists no error codes at all (`x-error-codes:
// []`): its body is not the Error.
func declaresNoErrorBody(r *v3.Response) bool {
	node := r.Extensions.GetOrZero("x-error-codes")
	if node == nil {
		return false
	}
	var codes []string
	return node.Decode(&codes) == nil && len(codes) == 0
}

// Seen reports whether operationId answered status with code ("" for a success) in a Check.
func (c *Contract) Seen(operationID string, status int, code string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen[operationID][status][code]
}

// Summary lists what was seen, for test logs.
func (c *Contract) Summary() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var lines []string
	for op, byStatus := range c.seen {
		for status, codes := range byStatus {
			var cs []string
			for code := range codes {
				cs = append(cs, code)
			}
			sort.Strings(cs)
			lines = append(lines, fmt.Sprintf("%s %d %s", op, status, strings.Join(cs, ",")))
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// OperationIDs lists the contract's operations of one x-feature ("core", "comments", …) whose
// paths start with prefix.
func (c *Contract) OperationIDs(prefix string) []string {
	var ids []string
	for path, item := range c.model.Paths.PathItems.FromOldest() {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		for _, op := range item.GetOperations().FromOldest() {
			ids = append(ids, op.OperationId)
		}
	}
	sort.Strings(ids)
	return ids
}

// operation finds the contract operation for a method and a concrete path.
func (c *Contract) operation(method, path string) *v3.Operation {
	for tmpl, item := range c.model.Paths.PathItems.FromOldest() {
		if !matches(tmpl, path) {
			continue
		}
		for m, op := range item.GetOperations().FromOldest() {
			if strings.EqualFold(m, method) {
				return op
			}
		}
	}
	return nil
}

func matches(tmpl, path string) bool {
	ts, ps := strings.Split(tmpl, "/"), strings.Split(path, "/")
	if len(ts) != len(ps) {
		return false
	}
	for i := range ts {
		if strings.HasPrefix(ts[i], "{") && strings.HasSuffix(ts[i], "}") {
			if ps[i] == "" {
				return false
			}
			continue
		}
		if ts[i] != ps[i] {
			return false
		}
	}
	return true
}
