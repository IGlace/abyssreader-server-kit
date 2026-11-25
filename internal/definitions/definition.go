// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

// Package definitions validates, signs, and stores the definitions served by the kit.
package definitions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	kit "github.com/IGlace/abyssreader-server-kit"
	"github.com/pb33f/jsonschema/v6"
	"golang.org/x/net/publicsuffix"
)

const MaxContent = 2 << 20

var (
	compileOnce    sync.Once
	schema         *jsonschema.Schema
	schemaErr      error
	slugPattern    = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	paramReference = regexp.MustCompile(`\{\{params\.([a-zA-Z0-9_]+)}}`)
	urlScheme      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*:`)
	dnsName        = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

type Metadata struct {
	Slug          string
	MinAppVersion *string
}

func object(raw []byte) (map[string]any, error) {
	if len(raw) > MaxContent || !utf8.Valid(raw) || !json.Valid(raw) {
		return nil, errors.New("definition must be valid UTF-8 JSON of at most 2 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, errors.New("definition must be a JSON object")
	}
	return value, nil
}

func compiledSchema() (*jsonschema.Schema, error) {
	compileOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(kit.DefinitionSchemaJSON()))
		if err != nil {
			schemaErr = err
			return
		}
		compiler := jsonschema.NewCompiler()
		const id = "urn:abyssreader:server-kit:definition"
		if err := compiler.AddResource(id, doc); err != nil {
			schemaErr = err
			return
		}
		schema, schemaErr = compiler.Compile(id)
	})
	return schema, schemaErr
}

func validHost(host string) bool {
	if len(host) > 253 || !dnsName.MatchString(host) {
		return false
	}
	tld := host[strings.LastIndex(host, ".")+1:]
	switch tld {
	case "localhost", "local", "internal", "onion", "test", "invalid", "home", "lan", "arpa":
		return false
	}
	return true
}

func validHostEntry(entry string) bool {
	host, wildcard := strings.CutPrefix(entry, "*.")
	if !validHost(host) {
		return false
	}
	if !wildcard {
		return true
	}
	suffix, _ := publicsuffix.PublicSuffix(host)
	return host != suffix
}

func httpsURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || !validHost(strings.ToLower(u.Hostname())) {
		return nil, errors.New("definition URLs must use HTTPS with a public DNS host and no credentials")
	}
	if port := u.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return nil, errors.New("definition URL has an invalid port")
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("definition URL has an invalid port")
	}
	return u, nil
}

func allowedHost(host, base string, extras []string) bool {
	if strings.EqualFold(host, base) || slices.Contains(extras, strings.ToLower(host)) {
		return true
	}
	host = strings.ToLower(host)
	for _, extra := range extras {
		if suffix, wildcard := strings.CutPrefix(extra, "*"); wildcard {
			if label, matches := strings.CutSuffix(host, suffix); matches && label != "" && !strings.Contains(label, ".") {
				return true
			}
		}
	}
	return false
}

// Lint verifies the resolved JSON and the parts of the app's network policy
// that can be checked statically. The app checks the resolved address and redirects.
func Lint(raw []byte) (Metadata, error) {
	value, err := object(raw)
	if err != nil {
		return Metadata{}, err
	}
	compiled, err := compiledSchema()
	if err != nil {
		return Metadata{}, err
	}
	if err := compiled.Validate(value); err != nil {
		return Metadata{}, errors.New("definition does not match contract/definition.schema.json")
	}
	slug := value["id"].(string)
	if !validSlug(slug) {
		return Metadata{}, errors.New("definition id must be an unreserved source slug of at most 64 characters")
	}
	base, err := httpsURL(value["baseUrl"].(string))
	if err != nil {
		return Metadata{}, err
	}
	if base.Path != "" || base.RawQuery != "" || base.ForceQuery || base.Fragment != "" || strings.Contains(base.String(), "#") {
		return Metadata{}, errors.New("baseUrl must be an HTTPS origin")
	}
	if execution := value["execution"]; execution == "server" {
		return Metadata{}, errors.New("a kit definition must run in the app")
	}
	if value["capabilities"].(map[string]any)["requiresJs"] == true {
		return Metadata{}, errors.New("definitions cannot use JavaScript")
	}

	rawHosts, _ := value["hosts"].([]any)
	hosts := make([]string, 0, len(rawHosts))
	for _, rawHost := range rawHosts {
		host := rawHost.(string)
		if !validHostEntry(host) {
			return Metadata{}, errors.New("hosts must be public DNS names or single-label wildcards")
		}
		hosts = append(hosts, host)
	}
	if err := lintOperations(value["operations"], base.Hostname(), hosts); err != nil {
		return Metadata{}, err
	}
	meta := Metadata{Slug: slug}
	if minApp, ok := value["minAppVersion"].(string); ok {
		meta.MinAppVersion = &minApp
	}
	return meta, nil
}

func lintOperations(value any, base string, hosts []string) error {
	switch value := value.(type) {
	case map[string]any:
		if _, hasJS := value["js"].(map[string]any); hasJS {
			return errors.New("definitions cannot use JavaScript")
		}
		if request, ok := value["request"].(map[string]any); ok {
			if raw, ok := request["url"].(string); ok {
				if err := lintRequestURL(raw, base, hosts); err != nil {
					return err
				}
			}
		}
		for _, item := range value {
			if err := lintOperations(item, base, hosts); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range value {
			if err := lintOperations(item, base, hosts); err != nil {
				return err
			}
		}
	}
	return nil
}

func lintRequestURL(raw, base string, hosts []string) error {
	if strings.HasPrefix(raw, "//") {
		return errors.New("request URL must specify HTTPS")
	}
	if !urlScheme.MatchString(raw) {
		return nil // runtime placeholders and relative paths are checked by the app
	}
	u, err := httpsURL(raw)
	if err != nil {
		return err
	}
	if !allowedHost(u.Hostname(), base, hosts) {
		return errors.New("request URL names a host outside baseUrl and hosts")
	}
	return nil
}

// Resolve merges objects, replaces other values and joins hosts. References to
// params are expanded before duplicate hosts are removed.
func Resolve(family, source []byte) ([]byte, error) {
	base, err := object(family)
	if err != nil {
		return nil, fmt.Errorf("family: %w", err)
	}
	over, err := object(source)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	hosts := []any{}
	hasHosts := false
	for _, obj := range []map[string]any{base, over} {
		if value, ok := obj["hosts"]; ok {
			list, ok := value.([]any)
			if !ok {
				return nil, errors.New("family and source hosts must be arrays")
			}
			hosts = append(hosts, list...)
			hasHosts = true
		}
	}
	merge(base, over)
	if hasHosts {
		base["hosts"] = hosts
	}
	params := make(map[string]string)
	if own, ok := base["params"].(map[string]any); ok {
		// Parameter expansion uses the merged values before walking the tree.
		for name, value := range own {
			if text, ok := value.(string); ok {
				params[name] = text
			} else {
				raw, _ := json.Marshal(value)
				params[name] = string(raw)
			}
		}
	}
	budget := MaxContent
	if _, err := substitute(base, params, &budget); err != nil {
		return nil, err
	}
	if hasHosts {
		unique := []string{}
		seen := make(map[string]bool)
		for _, raw := range base["hosts"].([]any) {
			host, ok := raw.(string)
			if !ok || !validHostEntry(host) {
				return nil, errors.New("resolved hosts must be DNS names or single-label wildcards")
			}
			if !seen[host] {
				unique = append(unique, host)
				seen[host] = true
			}
		}
		base["hosts"] = unique
	}
	result, err := json.MarshalIndent(base, "", "  ")
	if err != nil {
		return nil, err
	}
	if _, err := Lint(result); err != nil {
		return nil, err
	}
	return result, nil
}

func merge(base, over map[string]any) {
	for name, value := range over {
		left, baseIsObject := base[name].(map[string]any)
		right, overIsObject := value.(map[string]any)
		if baseIsObject && overIsObject {
			merge(left, right)
		} else {
			base[name] = value
		}
	}
}

func substitute(value any, params map[string]string, budget *int) (any, error) {
	switch value := value.(type) {
	case map[string]any:
		for name, item := range value {
			result, err := substitute(item, params, budget)
			if err != nil {
				return nil, err
			}
			value[name] = result
		}
	case []any:
		for i, item := range value {
			result, err := substitute(item, params, budget)
			if err != nil {
				return nil, err
			}
			value[i] = result
		}
	case string:
		var result strings.Builder
		at := 0
		for _, match := range paramReference.FindAllStringSubmatchIndex(value, -1) {
			param, ok := params[value[match[2]:match[3]]]
			if !ok {
				return nil, errors.New("a definition refers to a missing param")
			}
			*budget -= match[0] - at + len(param)
			if *budget < 0 {
				return nil, errors.New("expanded definition exceeds 2 MiB")
			}
			result.WriteString(value[at:match[0]])
			result.WriteString(param)
			at = match[1]
		}
		*budget -= len(value) - at
		if *budget < 0 {
			return nil, errors.New("expanded definition exceeds 2 MiB")
		}
		if at == 0 {
			return value, nil
		}
		result.WriteString(value[at:])
		return result.String(), nil
	}
	return value, nil
}
