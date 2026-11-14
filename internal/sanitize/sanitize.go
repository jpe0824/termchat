// Package sanitize makes untrusted text safe to print to a terminal.
package sanitize

import (
	"strings"
	"unicode/utf8"
)

// Text removes C0 controls other than \n and \t, C1 controls, and ESC, CSI
// and OSC sequences. An escape sequence cut off by the end of input is
// dropped entirely. Invalid UTF-8 bytes become U+FFFD. All other text is
// returned unchanged.
func Text(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c == 0x1b {
			i = skipEscape(s, i)
			continue
		}
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != 0x7f || c == '\n' || c == '\t' {
				b.WriteByte(c)
			}
			i++
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		switch {
		case r == utf8.RuneError && n == 1:
			b.WriteRune(utf8.RuneError)
		case r >= 0x80 && r <= 0x9f:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// skipEscape returns the index just past the escape sequence starting at
// s[i], which must be ESC.
func skipEscape(s string, i int) int {
	i++
	if i >= len(s) {
		return i
	}
	switch c := s[i]; {
	case c == '[':
		// CSI: parameter and intermediate bytes, then a final byte 0x40-0x7e.
		for i++; i < len(s); i++ {
			switch c := s[i]; {
			case c >= 0x40 && c <= 0x7e:
				return i + 1
			case c < 0x20 || c > 0x3f:
				return i
			}
		}
		return i
	case c == ']':
		// OSC: ends at BEL or ST (ESC \).
		for i++; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b {
				if i+1 < len(s) && s[i+1] == '\\' {
					return i + 2
				}
				return i
			}
		}
		return i
	case c >= 0x20 && c <= 0x2f:
		// nF sequence: intermediates then a final byte 0x30-0x7e.
		for ; i < len(s); i++ {
			if s[i] >= 0x30 && s[i] <= 0x7e {
				return i + 1
			}
			if s[i] < 0x20 || s[i] > 0x2f {
				return i
			}
		}
		return i
	case c >= 0x30 && c <= 0x7e:
		return i + 1
	}
	return i
}
