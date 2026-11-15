package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// ovMode is how an overlay is drawn: a centred box over the view, the whole
// window, or, for the room panel in the wide layout, the members panel.
type ovMode int

const (
	boxMode ovMode = iota
	fullMode
	dockMode
)

const (
	boxMaxW = 64
	boxMaxH = 22
	// boxMinH is the shortest window that takes a box; shorter ones show
	// the overlay as the whole window.
	boxMinH = 10
)

type rect struct{ x, y, w, h int }

func (m *Model) ovModeFor(g geom) ovMode {
	switch {
	case m.ov.kind == ovRoom && g.mode == Wide && g.memb.w > 0 && m.ov.conv == m.openID:
		return dockMode
	case g.mode <= Narrow || m.h < boxMinH:
		return fullMode
	}
	return boxMode
}

// docked reports whether the room panel is drawn as the members panel.
func (m *Model) docked(g geom) bool { return m.ov != nil && m.ovModeFor(g) == dockMode }

// ovPlace is where an overlay's parts go: r is all of it, borders included;
// the content is ch rows of cw cells from (cx, cy). The list's rows start
// at content row listAt, listN of them from row top.
type ovPlace struct {
	mode             ovMode
	r                rect
	cx, cy, cw, ch   int
	lines            []string
	listAt, listN    int
	top, fieldRow, n int
}

func (m *Model) ovRect() rect { return m.ovPlace().r }

func (m *Model) ovListRows() int { return m.ovPlace().listN }

// ovPlace lays the overlay out for the window and keeps the selected row
// in view.
func (m *Model) ovPlace() ovPlace {
	g := m.geom()
	p := ovPlace{mode: m.ovModeFor(g), fieldRow: -1}
	switch p.mode {
	case dockMode:
		p.r = rect{g.memb.x, g.bodyY, g.memb.w, g.bodyH}
		p.cx, p.cy, p.cw, p.ch = g.memb.x+1, g.bodyY, g.memb.inner(), g.bodyH
	case fullMode:
		h := g.bodyH
		if g.foot {
			h++
		}
		p.r = rect{0, g.bodyY, m.w, h}
		p.cx, p.cy, p.cw, p.ch = 2, g.bodyY, max(m.w-4, 0), g.bodyH
	default:
		bw := min(boxMaxW, m.w-6)
		p.cw = bw - 4
	}
	head, rows, empty, tail := m.ovBody(max(p.cw, 1))
	if p.mode != boxMode {
		head = append([]string{m.st.heading.Render(truncate(m.ovTitle(), p.cw))}, head...)
	}
	if p.mode == boxMode {
		need := len(head) + max(len(rows), 1) + len(tail)
		p.ch = min(need, min(m.h-2, boxMaxH)-2)
		bw := p.cw + 4
		p.r = rect{(m.w - bw) / 2, (m.h - p.ch - 2) / 2, bw, p.ch + 2}
		p.cx, p.cy = p.r.x+2, p.r.y+1
	}
	for i, l := range head {
		if l == fieldMark {
			p.fieldRow = i
			head[i] = m.fieldLine(p.cw)
		}
	}
	// The list keeps one row if the window allows; the tail goes first, then
	// the end of the head.
	tailN := min(len(tail), max(p.ch-len(head)-1, 0))
	headN := min(len(head), max(p.ch-tailN-1, 0), p.ch)
	p.listAt, p.listN, p.n = headN, max(p.ch-headN-tailN, 0), len(rows)
	if p.fieldRow >= headN {
		p.fieldRow = -1
	}
	ov := m.ov
	ov.sel = max(min(ov.sel, len(rows)-1), 0)
	if ov.sel < ov.top {
		ov.top = ov.sel
	}
	if ov.sel >= ov.top+p.listN {
		ov.top = ov.sel - p.listN + 1
	}
	ov.top = max(min(ov.top, len(rows)-p.listN), 0)
	p.top = ov.top
	lines := append([]string{}, head[:headN]...)
	switch {
	case len(rows) == 0 && empty != "" && p.listN > 0:
		lines = append(lines, m.st.faint.Render(truncate(empty, p.cw)))
	default:
		for i := p.top; i < min(p.top+p.listN, len(rows)); i++ {
			lines = append(lines, m.rowLine(rows[i], i == ov.sel, p.cw))
		}
	}
	p.lines = append(lines, tail[:tailN]...)
	return p
}

// fieldMark stands in the head for the text field's line.
const fieldMark = "\x00field"

func (m *Model) fieldLine(w int) string {
	rows, _, _ := m.ov.field.render(max(w-2, 1), 1)
	text := ""
	if len(rows) > 0 {
		text = rows[0]
	}
	return "> " + text
}

// rowLine draws a row w cells wide: "› " marks the selected one, which is
// also reversed, and the row's hl part is marked.
func (m *Model) rowLine(r ovRow, sel bool, w int) string {
	lead, st, hl := "  ", lipgloss.NewStyle(), m.st.match
	if sel {
		lead, st, hl = "› ", m.st.selected, m.st.match.Reverse(true)
	}
	pre, mark, post := clipRow(r, max(w-2, 0))
	out := st.Render(lead+pre) + hl.Render(mark) + st.Render(post)
	if sel {
		out += st.Render(strings.Repeat(" ", max(w-lipgloss.Width(lead+pre+mark+post), 0)))
	}
	return out
}

// ovRowAt is the row drawn at screen row y, if any.
func (m *Model) ovRowAt(y int) (int, bool) {
	p := m.ovPlace()
	i := y - p.cy - p.listAt
	if i < 0 || i >= p.listN || p.top+i >= p.n {
		return 0, false
	}
	return p.top + i, true
}

// ovFrame draws the overlay over frame, the main view.
func (m *Model) ovFrame(g geom, frame string) string {
	p := m.ovPlace()
	switch p.mode {
	case dockMode:
		return frame
	case fullMode:
		rows := make([]string, 0, m.h)
		if g.top {
			rows = append(rows, m.topBar(g))
		}
		for _, l := range pad(p.lines, region{1, m.w - 2}, g.bodyH) {
			rows = append(rows, "│"+l+"│")
		}
		if g.foot {
			rows = append(rows, m.footer(g))
		}
		return strings.Join(rows, "\n")
	}
	rows := strings.Split(frame, "\n")
	title := m.st.heading.Render(truncate(m.ovTitle(), p.r.w-6))
	box := []string{spread("┌─ "+title+" ", "┐", p.r.w, "─")}
	for i := range p.ch {
		l := ""
		if i < len(p.lines) {
			l = p.lines[i]
		}
		box = append(box, "│ "+fit(l, p.cw)+ansi.ResetStyle+" │")
	}
	box = append(box, borderKeys(m.ovFooterKeys(), p.r.w))
	for i, b := range box {
		y := p.r.y + i
		if y < 0 || y >= len(rows) {
			continue
		}
		left := fit(ansi.Cut(rows[y], 0, p.r.x), p.r.x)
		right := fit(ansi.Cut(rows[y], p.r.x+p.r.w, m.w), m.w-p.r.x-p.r.w)
		rows[y] = left + ansi.ResetStyle + b + right
	}
	return strings.Join(rows, "\n")
}

// ovCursor puts the terminal's cursor in the overlay's text field.
func (m *Model) ovCursor() *tea.Cursor {
	if !m.ov.hasField() || m.ov.ask != nil || m.help {
		return nil
	}
	p := m.ovPlace()
	if p.fieldRow < 0 {
		return nil
	}
	_, r, col := m.ov.field.render(max(p.cw-2, 1), 1)
	if r < 0 {
		return nil
	}
	return tea.NewCursor(p.cx+2+col, p.cy+p.fieldRow)
}

// ovStatus is the overlay's status: its question, error, or note, wrapped.
func (m *Model) ovStatus(w int) []string {
	ov := m.ov
	var text string
	st := m.st.faint
	switch {
	case ov.ask != nil:
		text, st = ov.ask.text, m.st.heading
	case ov.err != "":
		text, st = ov.err, m.st.bad
	case ov.busy:
		text = "working…"
	case ov.info != "":
		text = ov.info
	default:
		if e := m.nameErr(); e != "" {
			text, st = e, m.st.bad
		}
	}
	if text == "" {
		return nil
	}
	var out []string
	for _, l := range wrap(text, w) {
		out = append(out, st.Render(l))
	}
	return out
}

// ovBody is the overlay's content below its title: the head (field and
// status), the rows, what shows when there are none, and the tail.
func (m *Model) ovBody(w int) (head []string, rows []ovRow, empty string, tail []string) {
	ov := m.ov
	if ov.hasField() {
		head = append(head, fieldMark)
	}
	head = append(head, m.ovStatus(w)...)
	rows = m.ovRows()
	switch ov.kind {
	case ovJump:
		empty = "no matches"
	case ovSearch:
		empty = m.searchEmpty()
	case ovNew:
		empty = m.newEmpty()
	case ovRoom:
		var h []string
		h, empty, tail = m.roomBody(w)
		head = append(head, h...)
	}
	return head, rows, empty, tail
}

// borderKeys is a bottom border w cells wide carrying keys, as many as fit.
func borderKeys(keys []string, w int) string {
	for n := len(keys); n > 0; n-- {
		left := "└─ " + strings.Join(keys[:n], "  ") + " "
		switch lw := lipgloss.Width(left); {
		case lw+1 == w:
			// spread keeps a cell of fill, which an exact fit has no room for.
			return left + "┘"
		case lw+1 < w:
			return spread(left, "┘", w, "─")
		}
	}
	return "└" + strings.Repeat("─", max(w-2, 0)) + "┘"
}
