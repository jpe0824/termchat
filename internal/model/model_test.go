package model

import (
	"strings"
	"testing"
)

func TestNormalizeName(t *testing.T) {
	cases := map[string]string{
		" @Dana ":   "dana",
		"#Platform": "platform",
		"@@x":       "@x",
		"plain":     "plain",
	}
	for in, want := range cases {
		if got := NormalizeName(in); got != want {
			t.Errorf("NormalizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidName(t *testing.T) {
	valid := []string{"a", "dana", "a_b-c", "0abc", strings.Repeat("a", 32)}
	invalid := []string{"", "has space", "-lead", "_lead", "Upper", strings.Repeat("a", 33), "a\n", "é"}
	for _, s := range valid {
		if !ValidName(s) {
			t.Errorf("ValidName(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if ValidName(s) {
			t.Errorf("ValidName(%q) = true, want false", s)
		}
	}
}
