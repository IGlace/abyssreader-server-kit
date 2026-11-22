// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package schemas

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pb33f/jsonschema/v6"
)

func validate(t *testing.T, b []byte) error {
	t.Helper()
	s, err := ImportManifest()
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return s.Validate(doc)
}

// Every example manifest in the repo matches the schema (CI runs this).
func TestEveryExampleManifestMatchesTheSchema(t *testing.T) {
	n := 0
	err := filepath.WalkDir(filepath.Join("..", "examples"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "manifest.json" {
			return err
		}
		n++
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := validate(t, b); err != nil {
			t.Errorf("%s: %v", path, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no example manifest found under examples/")
	}
}

func TestTheSchemaRefuses(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"manifestVersion": 1,
			"titles": []any{map[string]any{
				"ref": "a", "name": "A", "sourceTitles": []any{map[string]any{"source": "s", "url": "/a"}},
			}},
		}
	}
	title := func(m map[string]any) map[string]any { return m["titles"].([]any)[0].(map[string]any) }
	if b, _ := json.Marshal(base()); validate(t, b) != nil {
		t.Fatalf("the minimal manifest must pass: %v", validate(t, b))
	}
	for name, change := range map[string]func(m map[string]any){
		"no manifestVersion":  func(m map[string]any) { delete(m, "manifestVersion") },
		"manifestVersion 2":   func(m map[string]any) { m["manifestVersion"] = 2 },
		"unknown field":       func(m map[string]any) { m["extra"] = true },
		"unknown title field": func(m map[string]any) { title(m)["hosted"] = true },
		"no ref":              func(m map[string]any) { delete(title(m), "ref") },
		"bad ref":             func(m map[string]any) { title(m)["ref"] = "Has Spaces" },
		"no source titles":    func(m map[string]any) { title(m)["sourceTitles"] = []any{} },
		"bad url": func(m map[string]any) {
			title(m)["sourceTitles"] = []any{map[string]any{"source": "s", "url": "series/a"}}
		},
		"rating 11":      func(m map[string]any) { title(m)["rating"] = 11 },
		"unknown status": func(m map[string]any) { title(m)["status"] = "finished" },
		"http cover":     func(m map[string]any) { title(m)["coverUrl"] = "http://covers.example.org/a.png" },
		"cover url and file": func(m map[string]any) {
			title(m)["coverUrl"] = "https://covers.example.org/a.png"
			title(m)["coverFile"] = "a.png"
		},
		"cover file escapes":  func(m map[string]any) { title(m)["coverFile"] = "../secret.png" },
		"absolute cover file": func(m map[string]any) { title(m)["coverFile"] = "/etc/passwd" },
		"empty description":   func(m map[string]any) { title(m)["description"] = "" },
		"blank name":          func(m map[string]any) { title(m)["name"] = "   " },
		"bad author role":     func(m map[string]any) { title(m)["authors"] = []any{map[string]any{"name": "X", "role": "writer"}} },
		"source website path": func(m map[string]any) {
			m["sources"] = []any{map[string]any{"slug": "s", "name": "S", "website": "https://s.example.org/x", "lang": "en"}}
		},
		"source http website": func(m map[string]any) {
			m["sources"] = []any{map[string]any{"slug": "s", "name": "S", "website": "http://s.example.org", "lang": "en"}}
		},
		"bad tachiyomi id": func(m map[string]any) {
			m["sources"] = []any{map[string]any{"slug": "s", "name": "S", "website": "https://s.example.org", "lang": "en", "tachiyomiIds": []any{"-1"}}}
		},
		"source marked hosted": func(m map[string]any) {
			m["sources"] = []any{map[string]any{"slug": "s", "name": "S", "website": "https://s.example.org", "lang": "en", "hosted": true}}
		},
	} {
		m := base()
		change(m)
		b, _ := json.Marshal(m)
		if err := validate(t, b); err == nil {
			t.Errorf("%s: the schema accepted it", name)
		}
	}
}

func TestTheSchemaCarriesItsLicence(t *testing.T) {
	head := strings.Join(strings.SplitN(string(ImportManifestJSON()), "\n", 7)[:6], "\n")
	if !strings.Contains(head, "SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0") || !strings.Contains(head, "Copyright 2026 Abyss Reader") {
		t.Fatal("the schema's first lines must carry the licence")
	}
}
