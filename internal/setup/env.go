// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package setup

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/compose-spec/compose-go/v2/dotenv"
)

// refMark stands for a ${VAR} reference while a file is parsed.
const refMark = "\x00ref\x00"

// ReadEnv reads a .env file (or an answers file, which has the same format) the way Docker
// Compose reads an env_file, with the same parser: what it returns is what the server sees.
// ${VAR} references are not looked up in the setup's own environment.
func ReadEnv(r io.Reader) (map[string]string, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(b) {
		return nil, fmt.Errorf("not UTF-8 text")
	}
	// Compose replaces $NAME and ${NAME} with a variable of the shell that runs it; for the
	// setup that would be nothing, and a value such as an access key would silently lose its
	// end. Every reference is marked and refused below: $$ is a literal $.
	m, err := dotenv.ParseWithLookup(bytes.NewReader(b), func(string) (string, bool) { return refMark, true })
	if err != nil {
		// The parser's message may quote a line, and a line may hold a secret.
		return nil, fmt.Errorf("not a valid env file (a quote may be missing; the line is not shown)")
	}
	var refs []string
	for k, v := range m {
		if strings.Contains(v, refMark) {
			refs = append(refs, k)
		}
	}
	if len(refs) > 0 {
		sort.Strings(refs)
		return nil, fmt.Errorf("%s: a $ followed by a name is a variable that Docker Compose replaces; write $$ for a "+
			"literal $, or put the value in single quotes (the value is not shown)", strings.Join(refs, ", "))
	}
	return m, nil
}

// Quote writes v as a double-quoted env-file value that Docker Compose reads back as exactly
// v: backslashes and double quotes are escaped, a dollar sign is written $$ (no ${VAR}
// expansion; the form every Compose v2 release reads), and line
// breaks and tabs become \n, \r and \t. Other control characters are refused.
func Quote(v string) (string, error) {
	if !utf8.ValidString(v) {
		return "", fmt.Errorf("not UTF-8 text")
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range v {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '$':
			b.WriteString(`$$`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				return "", fmt.Errorf("control character U+%04X", r)
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String(), nil
}
