// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package main

import (
	"strings"
	"unicode"
)

// Strip ECMA-48 controls (including their arguments), terminal strings and
// Unicode formatting/line separators. A malformed or unfinished sequence may
// suppress the rest of this field, never the next field or row.
func terminalText(value string) string {
	const (
		plain = iota
		escape
		intermediate
		csi
		controlString
		stringEscape
	)
	state := plain
	const maxMarks = 3
	remainingMarks := 0
	var out strings.Builder
	for _, r := range value {
		switch state {
		case controlString, stringEscape:
			if r == '\a' || r == '\u009c' || (state == stringEscape && r == '\\') {
				state = plain
			} else if r == '\x1b' {
				state = stringEscape
			} else {
				state = controlString
			}
		case escape:
			switch r {
			case '[':
				state = csi
			case ']', 'P', 'X', '^', '_':
				state = controlString
			default:
				if r >= 0x20 && r <= 0x2f {
					state = intermediate
				} else {
					state = plain
				}
			}
		case intermediate:
			if r >= 0x30 && r <= 0x7e {
				state = plain
			}
		case csi:
			if r >= 0x40 && r <= 0x7e {
				state = plain
			}
		default:
			switch r {
			case '\x1b':
				state = escape
			case '\u009b':
				state = csi
			case '\u009d', '\u0090', '\u0098', '\u009e', '\u009f':
				state = controlString
			default:
				if unicode.IsMark(r) {
					if remainingMarks > 0 {
						out.WriteRune(r)
						remainingMarks--
					}
				} else if unicode.IsPrint(r) {
					out.WriteRune(r)
					remainingMarks = maxMarks
				}
			}
		}
	}
	return out.String()
}
