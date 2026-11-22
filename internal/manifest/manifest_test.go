// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFillsTheDefaults(t *testing.T) {
	m, err := Parse([]byte(`{"manifestVersion":1,"titles":[{"ref":"a-1","name":"A","authors":[{"name":"X"}],
		"sourceTitles":[{"source":"s","url":"/a"}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	ti := m.Titles[0]
	if ti.Slug != "a-1" || ti.Status != "unknown" || ti.Authors[0].Role != "author" || ti.AltNames == nil || ti.Genres == nil || ti.SourceTitles[0].Lang != "" {
		t.Fatalf("defaults: %+v", ti)
	}
	if m.Sources != nil {
		t.Fatalf("no sources: %+v", m.Sources)
	}
}

func TestParseExplainsWhatIsWrong(t *testing.T) {
	_, err := Parse([]byte(`{"manifestVersion":1,"titles":[{"ref":"Bad Ref","name":"A","sourceTitles":[]}]}`))
	if err == nil || !strings.Contains(err.Error(), "at /titles/0/ref") || !strings.Contains(err.Error(), "at /titles/0/sourceTitles") {
		t.Fatalf("got %v", err)
	}
	if _, err := Parse([]byte(`{"manifestVersion":2,"titles":[]}`)); err == nil || !strings.Contains(err.Error(), "this kit reads manifestVersion 1; upgrade the kit") {
		t.Fatalf("a newer version: %v", err)
	}
	if _, err := Parse([]byte(`{"manifestVersion":1,`)); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("broken JSON: %v", err)
	}
	src := `{"slug":"s","name":"S","website":"https://s.example.org","lang":"en"}`
	if _, err := Parse([]byte(`{"manifestVersion":1,"sources":[` + src + `,` + src + `],"titles":[]}`)); err == nil || !strings.Contains(err.Error(), `source "s" is listed twice`) {
		t.Fatalf("a source twice: %v", err)
	}
}

func TestHashFollowsTheEntry(t *testing.T) {
	m, _ := Parse([]byte(`{"manifestVersion":1,"titles":[
		{"ref":"a","name":"A","sourceTitles":[{"source":"s","url":"/a"}]},
		{"ref":"a","name":"A","slug":"a","status":"unknown","sourceTitles":[{"source":"s","url":"/a"}]},
		{"ref":"a","name":"A2","sourceTitles":[{"source":"s","url":"/a"}]}]}`))
	if m.Titles[0].Hash() != m.Titles[1].Hash() {
		t.Error("spelling a default out does not change the entry")
	}
	if m.Titles[0].Hash() == m.Titles[2].Hash() {
		t.Error("a changed name changes the entry")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); err == nil {
		t.Fatal("a folder without manifest.json")
	}
	os.WriteFile(filepath.Join(dir, FileName), []byte(`{"manifestVersion":1,"titles":[]}`), 0o644)
	m, err := Load(dir)
	if err != nil || m.Dir != dir {
		t.Fatalf("%+v %v", m, err)
	}
}

func TestATachiyomiIDUnderTwoSourcesIsRefused(t *testing.T) {
	_, err := Parse([]byte(`{"manifestVersion":1,"sources":[
		{"slug":"a","name":"A","website":"https://a.example.org","lang":"en","tachiyomiIds":["123"]},
		{"slug":"b","name":"B","website":"https://b.example.org","lang":"en","tachiyomiIds":["7","123"]}],"titles":[]}`))
	if err == nil || !strings.Contains(err.Error(), `Tachiyomi id 123 is listed under the sources "a" and "b"`) {
		t.Fatalf("got %v", err)
	}
}
