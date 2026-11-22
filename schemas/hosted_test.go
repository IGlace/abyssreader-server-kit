// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package schemas

import (
	"encoding/json"
	"testing"
)

// hostedBase is a minimal hosted title: no catalog source titles, one hosted source
// title with one chapter of two pages.
func hostedBase() map[string]any {
	return map[string]any{
		"manifestVersion": 1,
		"titles": []any{map[string]any{
			"ref": "a", "name": "A",
			"hosted": []any{map[string]any{
				"lang": "en", "readingMode": "long_strip",
				"rights": map[string]any{"holder": "Ada", "licence": "CC BY 4.0", "licenceUrl": "https://creativecommons.org/licenses/by/4.0/",
					"attribution": "\"A\" by Ada, CC BY 4.0", "changes": nil},
				"chapters": []any{map[string]any{
					"ref": "1", "number": 1, "name": "One", "volume": nil, "publishedAt": "2026-11-20", "credits": "Translated by Bo",
					"pages": []any{"ch1/01.png", map[string]any{"file": "ch1/02.png", "width": 800, "height": 1200}},
				}},
			}},
		}},
	}
}

func TestTheSchemaTakesHostedTitles(t *testing.T) {
	for name, change := range map[string]func(m map[string]any){
		"as it is":         func(map[string]any) {},
		"no rights":        func(m map[string]any) { delete(hosted(m), "rights"); delete(hosted(m), "readingMode") },
		"null rights":      func(m map[string]any) { hosted(m)["rights"] = nil; hosted(m)["readingMode"] = nil },
		"empty rights":     func(m map[string]any) { hosted(m)["rights"] = map[string]any{} },
		"no chapters yet":  func(m map[string]any) { hosted(m)["chapters"] = []any{} },
		"a full timestamp": func(m map[string]any) { chapter(m)["publishedAt"] = "2026-11-20T10:00:00.000Z" },
		"minimal chapter": func(m map[string]any) {
			hosted(m)["chapters"] = []any{map[string]any{"ref": "x", "pages": []any{"p.jpg"}}}
		},
		"with source titles": func(m map[string]any) { title(m)["sourceTitles"] = []any{map[string]any{"source": "s", "url": "/a"}} },
	} {
		m := hostedBase()
		change(m)
		b, _ := json.Marshal(m)
		if err := validate(t, b); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func title(m map[string]any) map[string]any { return m["titles"].([]any)[0].(map[string]any) }
func hosted(m map[string]any) map[string]any {
	return title(m)["hosted"].([]any)[0].(map[string]any)
}
func chapter(m map[string]any) map[string]any {
	return hosted(m)["chapters"].([]any)[0].(map[string]any)
}

func TestTheSchemaRefusesBadHostedTitles(t *testing.T) {
	for name, change := range map[string]func(m map[string]any){
		"neither source titles nor hosted": func(m map[string]any) { delete(title(m), "hosted") },
		"empty hosted":                     func(m map[string]any) { title(m)["hosted"] = []any{} },
		"no lang":                          func(m map[string]any) { delete(hosted(m), "lang") },
		"no chapters":                      func(m map[string]any) { delete(hosted(m), "chapters") },
		"unknown reading mode":             func(m map[string]any) { hosted(m)["readingMode"] = "vertical" },
		"unknown rights field":             func(m map[string]any) { hosted(m)["rights"].(map[string]any)["owner"] = "x" },
		"http licence url":                 func(m map[string]any) { hosted(m)["rights"].(map[string]any)["licenceUrl"] = "http://x.example.org" },
		"empty licence":                    func(m map[string]any) { hosted(m)["rights"].(map[string]any)["licence"] = "" },
		"no chapter ref":                   func(m map[string]any) { delete(chapter(m), "ref") },
		"bad chapter ref":                  func(m map[string]any) { chapter(m)["ref"] = "Chapter 1" },
		"no pages":                         func(m map[string]any) { chapter(m)["pages"] = []any{} },
		"negative number":                  func(m map[string]any) { chapter(m)["number"] = -1 },
		"empty name":                       func(m map[string]any) { chapter(m)["name"] = "" },
		"empty credits":                    func(m map[string]any) { chapter(m)["credits"] = "" },
		"bad publishedAt":                  func(m map[string]any) { chapter(m)["publishedAt"] = "20 Nov 2026" },
		"page escapes":                     func(m map[string]any) { chapter(m)["pages"] = []any{"../x.png"} },
		"absolute page":                    func(m map[string]any) { chapter(m)["pages"] = []any{"/etc/x.png"} },
		"page url": func(m map[string]any) {
			chapter(m)["pages"] = []any{map[string]any{"file": "https://x.example.org/1.png"}}
		},
		"page zero width": func(m map[string]any) {
			chapter(m)["pages"] = []any{map[string]any{"file": "1.png", "width": 0, "height": 10}}
		},
		"unknown page field":    func(m map[string]any) { chapter(m)["pages"] = []any{map[string]any{"file": "1.png", "url": "x"}} },
		"unknown chapter field": func(m map[string]any) { chapter(m)["pageCount"] = 2 },
	} {
		m := hostedBase()
		change(m)
		b, _ := json.Marshal(m)
		if err := validate(t, b); err == nil {
			t.Errorf("%s: the schema accepted it", name)
		}
	}
}
