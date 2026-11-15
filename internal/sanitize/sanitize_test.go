package sanitize

import "testing"

func TestSanitizeStripsEscapes(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"csi and osc", "a\x1b[31mb\x1b]0;x\x07c\x07", "abc"},
		{"unicode intact", "héllo 👋\n\tok", "héllo 👋\n\tok"},
		{"c0 dropped", "a\x00b\x08c\rd\x7f", "abcd"},
		{"c1 dropped", "a\u0085b\u009bc", "abc"},
		{"osc st terminator", "a\x1b]8;;http://x\x1b\\b", "ab"},
		{"two byte escape", "a\x1bMb", "ab"},
		{"unterminated esc", "ab\x1b", "ab"},
		{"unterminated csi", "ab\x1b[31", "ab"},
		{"unterminated osc", "ab\x1b]0;title", "ab"},
		{"invalid utf8", "a\xffb\xc3", "a�b�"},
	}
	for _, c := range cases {
		if got := Text(c.in); got != c.want {
			t.Errorf("%s: Text(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

func TestLine(t *testing.T) {
	if got, want := Line("  a\n\tb \x1b[31mc\x1b[0m\r\n  d "), "a b c d"; got != want {
		t.Errorf("Line = %q, want %q", got, want)
	}
}
