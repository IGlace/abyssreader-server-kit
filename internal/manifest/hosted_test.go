// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package manifest

import (
	"strings"
	"testing"
	"time"
)

const hostedTitle = `{"manifestVersion":1,"titles":[{"ref":"a","name":"A","hosted":[{"lang":"en","readingMode":"long_strip",
	"rights":{"holder":"Ada","licence":"CC BY 4.0"},
	"chapters":[
		{"ref":"1","number":1,"publishedAt":"2026-11-20","pages":["ch1/01.png",{"file":"ch1/02.png","width":800,"height":1200}]},
		{"ref":"2","name":"Two","publishedAt":"2026-11-21T08:30:00.5Z","credits":"Bo","pages":["ch2/01.png"]}]}]}]}`

func TestParseReadsHostedTitles(t *testing.T) {
	m, err := Parse([]byte(hostedTitle))
	if err != nil {
		t.Fatal(err)
	}
	ti := m.Titles[0]
	if ti.SourceTitles == nil || len(ti.SourceTitles) != 0 || len(ti.Hosted) != 1 {
		t.Fatalf("%+v", ti)
	}
	h := ti.Hosted[0]
	if h.Lang != "en" || *h.ReadingMode != "long_strip" || *h.Rights.Holder != "Ada" || h.Rights.LicenceURL != nil || len(h.Chapters) != 2 {
		t.Fatalf("%+v", h)
	}
	c1, c2 := h.Chapters[0], h.Chapters[1]
	if *c1.Number != 1 || c1.Name != nil || c1.Credits != nil || len(c1.Pages) != 2 ||
		c1.Pages[0] != (Page{File: "ch1/01.png"}) || c1.Pages[1] != (Page{File: "ch1/02.png", Width: 800, Height: 1200}) {
		t.Fatalf("%+v", c1)
	}
	if !c1.Published().Equal(time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)) ||
		!c2.Published().Equal(time.Date(2026, 11, 21, 8, 30, 0, 500e6, time.UTC)) {
		t.Fatalf("published: %v %v", c1.Published(), c2.Published())
	}
	if c1.Hash() == c2.Hash() {
		t.Fatal("two chapters, one hash")
	}
}

func TestParseRefusesHostedMistakes(t *testing.T) {
	for name, tc := range map[string]struct{ manifest, want string }{
		"a lang twice": {
			`{"manifestVersion":1,"titles":[{"ref":"a","name":"A","hosted":[{"lang":"en","chapters":[]},{"lang":"en","chapters":[]}]}]}`,
			`title "a": hosted lang "en" is listed twice`,
		},
		"a chapter ref twice": {
			`{"manifestVersion":1,"titles":[{"ref":"a","name":"A","hosted":[{"lang":"en","chapters":[{"ref":"1","pages":["a.png"]},{"ref":"1","pages":["b.png"]}]}]}]}`,
			`title "a" (en): chapter "1" is listed twice`,
		},
		"a date that does not exist": {
			`{"manifestVersion":1,"titles":[{"ref":"a","name":"A","hosted":[{"lang":"en","chapters":[{"ref":"1","publishedAt":"2026-13-40","pages":["a.png"]}]}]}]}`,
			`title "a" (en) chapter "1": publishedAt "2026-13-40" is not a date`,
		},
		"the hosted slug as a source": {
			`{"manifestVersion":1,"sources":[{"slug":"hosted","name":"H","website":"https://h.example.org","lang":"en"}],"titles":[]}`,
			`the source slug "hosted" is the server's own hosted source`,
		},
		"a source title on the hosted source": {
			`{"manifestVersion":1,"titles":[{"ref":"a","name":"A","sourceTitles":[{"source":"hosted","url":"/source-titles/1"}]}]}`,
			`title "a": the source slug "hosted" is the server's own hosted source`,
		},
	} {
		if _, err := Parse([]byte(tc.manifest)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestCatalogManifestsOfKitPR2StayValid(t *testing.T) {
	m, err := Parse([]byte(`{"manifestVersion":1,"titles":[{"ref":"a","name":"A","sourceTitles":[{"source":"s","url":"/a"}]}]}`))
	if err != nil || m.Titles[0].Hosted == nil || len(m.Titles[0].Hosted) != 0 {
		t.Fatalf("%+v %v", m, err)
	}
}
