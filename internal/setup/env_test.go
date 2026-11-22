// SPDX-License-Identifier: PolyForm-Noncommercial-1.0.0
// Copyright 2026 Abyss Reader

package setup

import (
	"strings"
	"testing"
)

// Values that break naive .env writers: each must come back exactly, through the parser
// Docker Compose uses for env_file (ReadEnv is that parser).
var awkward = []string{
	"",
	"plain",
	"two words",
	`double "quotes" inside`,
	`single 'quotes' inside`,
	`it's`,
	`$HOME and ${HOME} and $$ and $`,
	`back\slash \n not a newline \\ two \$ escaped dollar`,
	`ends with a backslash\`,
	"line one\nline two\r\nwith\ttab",
	" leading and trailing spaces ",
	"# not a comment",
	"value # with hash",
	"équipe 漫画 🙂",
	`=equals=`,
	`'`, `"`, `\`, `\"`, `\'`,
}

func TestQuoteRoundTripsThroughComposeParser(t *testing.T) {
	var b strings.Builder
	for i, v := range awkward {
		q, err := Quote(v)
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		b.WriteString("K" + string(rune('A'+i)) + "=" + q + "\n")
	}
	got, err := ReadEnv(strings.NewReader(b.String()))
	if err != nil {
		t.Fatalf("%v\n%s", err, b.String())
	}
	for i, v := range awkward {
		if g := got["K"+string(rune('A'+i))]; g != v {
			t.Errorf("round trip\n got %q\nwant %q\nfile line %s", g, v, strings.Split(b.String(), "\n")[i])
		}
	}
}

func TestQuoteRefusesOtherControlCharacters(t *testing.T) {
	for _, v := range []string{"a\x00b", "a\x1bb", "a\x7fb", "bad \xff"} {
		if _, err := Quote(v); err == nil {
			t.Errorf("%q must be refused", v)
		}
	}
}

func TestReadEnvHandEditedForms(t *testing.T) {
	got, err := ReadEnv(strings.NewReader(`# a comment
ABYSS_SERVER_NAME=Lantern Library # inline comment
export ABYSS_OPERATOR_NAME='The $team'
ABYSS_OPERATOR_CONTACT="mailto:team@example.org"
EMPTY=
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"ABYSS_SERVER_NAME": "Lantern Library", "ABYSS_OPERATOR_NAME": "The $team", "ABYSS_OPERATOR_CONTACT": "mailto:team@example.org", "EMPTY": ""}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestReadEnvNeverQuotesTheBadLine(t *testing.T) {
	_, err := ReadEnv(strings.NewReader("ABYSS_ACCESS_KEY=\"s3cret-unterminated\n"))
	if err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("want an error without the value: %v", err)
	}
}
