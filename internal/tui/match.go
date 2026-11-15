package tui

import (
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

// clipRow cuts a row's text to w cells, split around its marked part: a
// part past the edge brings the text from r.from on into view from a
// little before the part, the gap marked by "…".
func clipRow(r ovRow, w int) (pre, mark, post string) {
	text, at, end := r.text, r.hlAt, r.hlEnd
	if end <= at || at < 0 || end > len(text) {
		return truncate(text, w), "", ""
	}
	if cut := min(max(r.from, 0), at); lipgloss.Width(text[:end]) > w {
		from := at
		for n := 0; n < 4 && from > cut; n++ {
			from = (&input{text: text}).prev(from)
		}
		lead := text[:cut] + "…"
		text, at, end = lead+text[from:], at-from+len(lead), end-from+len(lead)
	}
	p := clip(w, text[:at], text[at:end], text[end:])
	pre, mark, post = p[0], p[1], p[2]
	if mark != text[at:end] {
		// Only a whole match is marked; a cut one reads as plain text.
		return pre + mark, "", post
	}
	return pre, mark, post
}

// foldMatch is where word first appears in s, ignoring case rune by rune,
// as byte offsets; it matches only whole characters (grapheme clusters),
// and is -1, -1 when there is no such match.
func foldMatch(s, word string) (int, int) {
	if word == "" {
		return -1, -1
	}
	whole := map[int]bool{0: true}
	n := 0
	graphemes(s, func(g string, _ int) bool {
		n += len(g)
		whole[n] = true
		return true
	})
	for i := range s {
		if !whole[i] {
			continue
		}
		j, ok := i, true
		for _, wr := range word {
			sr, size := utf8.DecodeRuneInString(s[j:])
			if j >= len(s) || !strings.EqualFold(string(sr), string(wr)) {
				ok = false
				break
			}
			j += size
		}
		if ok && whole[j] {
			return i, j
		}
	}
	return -1, -1
}
