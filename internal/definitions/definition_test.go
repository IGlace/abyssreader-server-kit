// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package definitions

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func example(t *testing.T, name string) []byte {
	t.Helper()
	f, err := os.ReadFile(filepath.Join("..", "..", "examples", "definitions", name))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestExampleResolvedSelectorMatchesLocalSamplePage(t *testing.T) {
	resolved, err := Resolve(example(t, "family.json"), example(t, "source.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Operations map[string]struct {
			Steps []struct {
				Select struct {
					CSS string `json:"css"`
				} `json:"select"`
			} `json:"steps"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(resolved, &doc); err != nil {
		t.Fatal(err)
	}
	selector := doc.Operations["popular"].Steps[0].Select.CSS
	server := httptest.NewTLSServer(http.FileServer(http.Dir(filepath.Join("..", "..", "examples", "definitions"))))
	defer server.Close()
	resp, err := server.Client().Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("local sample page: %s", resp.Status)
	}
	tokens := html.NewTokenizer(resp.Body)
	for kind := tokens.Next(); kind != html.ErrorToken; kind = tokens.Next() {
		if kind == html.StartTagToken {
			tag := tokens.Token()
			for _, attr := range tag.Attr {
				if attr.Key == "class" && "."+attr.Val == selector {
					return
				}
			}
		}
	}
	t.Fatalf("resolved selector %q matched no element on the local sample page", selector)
}

func TestResolveJoinsHostsAfterSubstitution(t *testing.T) {
	family := strings.Replace(string(example(t, "family.json")), `"params":`, `"hosts": ["{{params.images}}", "*.cdn.example.com"], "params":`, 1)
	source := strings.Replace(string(example(t, "source.json")), `"params": {`, `"params": {"images": "images.lantern.example.com",`, 1)
	resolved, err := Resolve([]byte(family), []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := object(resolved)
	if err != nil {
		t.Fatal(err)
	}
	hosts, _ := json.Marshal(doc["hosts"])
	if string(hosts) != `["images.lantern.example.com","*.cdn.example.com"]` {
		t.Fatalf("resolved hosts = %s", hosts)
	}
}

func TestLintRejectsPrivateOriginsAndUndeclaredRequestHosts(t *testing.T) {
	for name, replace := range map[string]func(string) string{
		"http origin": func(text string) string {
			return strings.ReplaceAll(text, "https://lantern.example.com", "http://lantern.example.com")
		},
		"private origin": func(text string) string {
			return strings.ReplaceAll(text, "https://lantern.example.com", "https://192.168.10.5")
		},
		"local suffix": func(text string) string {
			return strings.ReplaceAll(text, "images.lantern.example.com", "pictures.lantern.local")
		},
		"public suffix wildcard": func(text string) string {
			return strings.ReplaceAll(text, "images.lantern.example.com", "*.co.uk")
		},
		"private request": func(text string) string {
			return strings.ReplaceAll(text, "{{baseUrl}}/", "https://127.0.0.1/")
		},
		"undeclared public host": func(text string) string {
			return strings.ReplaceAll(text, "{{baseUrl}}/", "https://other.example.com/")
		},
		"JavaScript": func(text string) string {
			return strings.ReplaceAll(text, `"requiresJs": false`, `"requiresJs": true`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			resolved, err := Resolve(example(t, "family.json"), example(t, "source.json"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Lint([]byte(replace(string(resolved)))); err == nil {
				t.Fatal("lint accepted an unsafe input")
			}
		})
	}
}

func TestContractEd25519Statements(t *testing.T) {
	var keys struct {
		Response struct {
			Body struct{ Items []Key }
		}
	}
	readExample(t, "listDefinitionKeys.json", &keys)
	keyMap := map[string]Key{}
	for _, k := range keys.Response.Body.Items {
		keyMap[k.KeyID] = k
	}
	for _, k := range keyMap {
		if k.Endorsement != nil {
			old := keyMap[k.Endorsement.ByKeyID]
			checkSignature(t, old.PublicKey, k.Endorsement.Signature, endorsementStatement(old, k))
		}
	}

	var manifest struct {
		Response struct {
			Body struct{ Items []Entry }
		}
	}
	readExample(t, "getDefinitionManifest.json", &manifest)
	for _, item := range manifest.Response.Body.Items {
		checkSignature(t, keyMap[item.KeyID].PublicKey, item.Signature, item.statement())
	}
}

func readExample(t *testing.T, name string, target any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contract", "examples", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatal(err)
	}
}

func checkSignature(t *testing.T, key, sig string, statement []byte) {
	t.Helper()
	public, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(public) != ed25519.PublicKeySize {
		t.Fatal("invalid key in fixture")
	}
	signature, err := base64.StdEncoding.DecodeString(sig)
	if err != nil || !ed25519.Verify(public, statement, signature) {
		t.Fatal("published fixture failed verification")
	}
}
