package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/jpe0824/termchat/internal/sanitize"
)

// tabCells is how wide a tab is drawn; the tab itself stays in the text.
const tabCells = 4

// input is a multi-line text being written. pos is a byte offset into
// text and always sits on a grapheme cluster boundary, so the cursor never
// splits a wide character or an emoji sequence. Rows depend on the width
// the input is drawn at, so only the first row shown is kept.
type input struct {
	text string
	pos  int
	top  int
	// follow keeps the cursor's row in view; scrolling the input with the
	// wheel turns it off until the cursor next moves.
	follow bool
}

// cleanInput is typed or pasted text as it may enter a draft: carriage
// returns become line breaks, and escape sequences and control characters
// other than line break and tab are dropped.
func cleanInput(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return sanitize.Text(s)
}

// span is one drawn row: text[start:end]. soft says the line goes on in
// the next row, which starts at end.
type span struct {
	start, end int
	soft       bool
}

// cluster is the grapheme at the start of s and the cells it is drawn in,
// within a row of w cells.
func cluster(s string, w int) (string, int) {
	if s[0] == '\n' {
		return "\n", 0
	}
	if s[0] == '\t' {
		return "\t", min(tabCells, w)
	}
	g, gw := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
	if g == "" {
		return s[:1], 1
	}
	if gw > w {
		gw = 1 // drawn as "?"
	}
	return g, gw
}

// rows lays the text out in rows of w cells. One cell is kept free so the
// cursor fits after a full row.
func (in *input) rows(w int) []span {
	w = max(w-1, 1)
	var out []span
	start, cells := 0, 0
	for i := 0; i < len(in.text); {
		if in.text[i] == '\n' {
			out = append(out, span{start, i, false})
			i++
			start, cells = i, 0
			continue
		}
		g, gw := cluster(in.text[i:], w)
		if cells+gw > w && cells > 0 {
			out = append(out, span{start, i, true})
			start, cells = i, 0
		}
		cells += gw
		i += len(g)
	}
	return append(out, span{start, len(in.text), false})
}

// cells is the drawn width of text[from:to], laid out at w.
func (in *input) cells(from, to, w int) int {
	n := 0
	for i := from; i < to; {
		g, gw := cluster(in.text[i:], max(w-1, 1))
		n += gw
		i += len(g)
	}
	return n
}

// cursorRow is the row and column of the cursor. At a soft break the
// cursor belongs to the row below.
func (in *input) cursorRow(rows []span, w int) (int, int) {
	for r, s := range rows {
		if in.pos >= s.start && (in.pos < s.end || in.pos == s.end && !s.soft) {
			return r, in.cells(s.start, in.pos, w)
		}
	}
	last := len(rows) - 1
	return last, in.cells(rows[last].start, rows[last].end, w)
}

// offsetAt is the offset in row s closest to column col without passing it.
func (in *input) offsetAt(s span, col, w int) int {
	i, n := s.start, 0
	for i < s.end {
		g, gw := cluster(in.text[i:], max(w-1, 1))
		if n+gw > col {
			break
		}
		n += gw
		i += len(g)
	}
	if s.soft && i == s.end && i > s.start {
		i = in.prev(i)
	}
	return i
}

// render returns at most h rows drawn w cells wide, scrolled to keep the
// cursor in view, and the cursor's position among them; row is -1 when
// the cursor is scrolled out of view.
func (in *input) render(w, h int) (lines []string, row, col int) {
	rows := in.rows(w)
	r, c := in.cursorRow(rows, w)
	h = max(h, 1)
	if in.follow {
		in.top = min(in.top, r)
		if r >= in.top+h {
			in.top = r - h + 1
		}
	}
	in.top = max(min(in.top, len(rows)-h), 0)
	for _, s := range rows[in.top:min(in.top+h, len(rows))] {
		lines = append(lines, in.draw(s, w))
	}
	if r < in.top || r >= in.top+h {
		return lines, -1, c
	}
	return lines, r - in.top, c
}

func (in *input) draw(s span, w int) string {
	var b strings.Builder
	for i := s.start; i < s.end; {
		g, gw := cluster(in.text[i:], max(w-1, 1))
		switch {
		case g == "\t":
			b.WriteString(strings.Repeat(" ", gw))
		case gw == 1 && ansi.StringWidth(g) > 1:
			b.WriteString("?")
		default:
			b.WriteString(g)
		}
		i += len(g)
	}
	return b.String()
}

// scroll moves the rows shown by d without moving the cursor.
func (in *input) scroll(d, w, h int) {
	in.follow = false
	in.top = max(min(in.top+d, len(in.rows(w))-h), 0)
}

// overflows reports whether more rows exist than h.
func (in *input) overflows(w, h int) bool { return len(in.rows(w)) > h }

// place puts the cursor at a row shown and a column, as a click does.
func (in *input) place(row, col, w int) {
	rows := in.rows(w)
	r := min(max(in.top+row, 0), len(rows)-1)
	in.pos = in.offsetAt(rows[r], max(col, 0), w)
	in.follow = true
}

func (in *input) set(s string) { *in = input{follow: true}; in.insert(s) }

func (in *input) insert(s string) {
	in.text = in.text[:in.pos] + s + in.text[in.pos:]
	in.pos += len(s)
	in.follow = true
}

func (in *input) remove(from, to int) {
	in.text = in.text[:from] + in.text[to:]
	in.pos = from
	in.follow = true
}

// next is the end of the grapheme starting at i.
func (in *input) next(i int) int {
	if i >= len(in.text) {
		return len(in.text)
	}
	g, _ := cluster(in.text[i:], 2)
	return i + len(g)
}

// prev is the start of the grapheme ending at i.
func (in *input) prev(i int) int {
	if i <= 0 {
		return 0
	}
	if in.text[i-1] == '\n' {
		return i - 1
	}
	j := in.lineStart(i)
	for {
		k := in.next(j)
		if k >= i {
			return j
		}
		j = k
	}
}

func (in *input) lineStart(i int) int { return strings.LastIndexByte(in.text[:i], '\n') + 1 }

func (in *input) lineEnd(i int) int {
	if j := strings.IndexByte(in.text[i:], '\n'); j >= 0 {
		return i + j
	}
	return len(in.text)
}

func (in *input) left()  { in.pos, in.follow = in.prev(in.pos), true }
func (in *input) right() { in.pos, in.follow = in.next(in.pos), true }
func (in *input) home()  { in.pos, in.follow = in.lineStart(in.pos), true }
func (in *input) end()   { in.pos, in.follow = in.lineEnd(in.pos), true }

func (in *input) backspace() {
	if in.pos > 0 {
		in.remove(in.prev(in.pos), in.pos)
	}
}

func (in *input) del() {
	if in.pos < len(in.text) {
		p := in.pos
		in.remove(p, in.next(p))
	}
}

// killWord deletes the word before the cursor and the spaces after it.
func (in *input) killWord() {
	ls := in.lineStart(in.pos)
	if in.pos == ls {
		in.backspace()
		return
	}
	i := in.pos
	space := func(j int) bool { c := in.text[j]; return c == ' ' || c == '\t' }
	for i > ls && space(i-1) {
		i--
	}
	for i > ls && !space(i-1) {
		i = in.prev(i)
	}
	in.remove(i, in.pos)
}

// killLine deletes from the start of the line to the cursor.
func (in *input) killLine() { in.remove(in.lineStart(in.pos), in.pos) }

// up and down move the cursor a row, keeping its column; they report
// false when there is no row to move to.
func (in *input) up(w int) bool { return in.vertical(-1, w) }

func (in *input) down(w int) bool { return in.vertical(1, w) }

func (in *input) vertical(d, w int) bool {
	rows := in.rows(w)
	r, c := in.cursorRow(rows, w)
	if r+d < 0 || r+d >= len(rows) {
		return false
	}
	in.pos = in.offsetAt(rows[r+d], c, w)
	in.follow = true
	return true
}
