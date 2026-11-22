// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package schemas

import (
	"encoding/json"
	"testing"
)

// A chapter lists at most 500 page files: the served cap (pages.MaxChapterImages).
func TestAChapterListsAtMost500Pages(t *testing.T) {
	for n, ok := range map[int]bool{500: true, 501: false} {
		m := hostedBase()
		var list []any
		for i := range n {
			list = append(list, "p"+string(rune('a'+i%26))+".png")
		}
		chapter(m)["pages"] = list
		b, _ := json.Marshal(m)
		if err := validate(t, b); (err == nil) != ok {
			t.Errorf("%d pages: %v", n, err)
		}
	}
}
