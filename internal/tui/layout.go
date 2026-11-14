package tui

import (
	"strings"

	"github.com/jpe0824/termchat/internal/model"
)

// Mode is the layout chosen for a window width.
type Mode int

const (
	Tiny     Mode = iota // under 36 columns: the thread, no timestamps
	Narrow               // 36–69: one pane, the thread first
	Standard             // 70–109: chat list and thread
	Wide                 // 110 and up: chat list, thread and members panel
)

func (m Mode) String() string {
	return [...]string{"tiny", "narrow", "standard", "wide"}[m]
}

const (
	sideCols    = 20 // chat list, padding included
	membersCols = 20 // members panel, padding included
	minW, minH  = 20, 5
	footerMinH  = 12 // shorter windows drop the footer
	topMinH     = 8  // shorter windows drop the top bar as well
	tooSmall    = "termchat: window too small"
)

// LayoutMode returns the layout for a window width in columns.
func LayoutMode(width int) Mode {
	switch {
	case width >= 110:
		return Wide
	case width >= 70:
		return Standard
	case width >= 36:
		return Narrow
	}
	return Tiny
}

// region is a column range of the body: x is its first cell inside the
// borders, w its width with one cell of padding at each side.
type region struct{ x, w int }

func (r region) has(x int) bool { return r.w > 0 && x >= r.x && x < r.x+r.w }

// inner is the region's width without padding.
func (r region) inner() int { return max(r.w-2, 0) }

// geom is where everything goes for the current window and state.
type geom struct {
	mode            Mode
	top, foot       bool
	bodyY, bodyH    int
	side, thr, memb region
}

func (m *Model) geom() geom {
	g := geom{mode: LayoutMode(m.w)}
	g.top, g.foot = m.h >= topMinH, m.h >= footerMinH
	g.bodyH = m.h
	if g.top {
		g.bodyY = 1
		g.bodyH--
	}
	if g.foot {
		g.bodyH--
	}
	inner := m.w - 2
	switch g.mode {
	case Wide, Standard:
		g.side = region{1, sideCols}
		rest := inner - sideCols - 1
		if g.mode == Wide && m.membersShown() {
			g.memb = region{m.w - 1 - membersCols, membersCols}
			rest -= membersCols + 1
		}
		g.thr = region{sideCols + 2, rest}
	default:
		if m.listShown() {
			g.side = region{1, inner}
		} else {
			g.thr = region{1, inner}
		}
	}
	return g
}

// listShown reports whether narrow and tiny layouts show the chat list
// instead of the thread.
func (m *Model) listShown() bool { return m.showList || m.openID == "" }

func (m *Model) membersShown() bool {
	c, ok := m.openConv()
	return ok && c.Kind == model.ConvRoom
}

// pad puts a pane's content lines into its region: one space each side,
// exactly h rows.
func pad(lines []string, r region, h int) []string {
	out := make([]string, h)
	for i := range out {
		l := ""
		if i < len(lines) {
			l = lines[i]
		}
		out[i] = " " + fit(l, r.inner()) + " "
		if r.w < 2 {
			out[i] = fit("", r.w)
		}
	}
	return out
}

// frame draws the main screen.
func (m *Model) frame() string {
	if m.w < minW || m.h < minH {
		return tooSmall[:min(len(tooSmall), max(m.w, 0))]
	}
	g := m.geom()
	var cols [][]string
	if m.help {
		cols = append(cols, pad(m.helpLines(), region{1, m.w - 2}, g.bodyH))
	} else {
		if g.side.w > 0 {
			cols = append(cols, pad(m.sidebarLines(g), g.side, g.bodyH))
		}
		if g.thr.w > 0 {
			cols = append(cols, pad(m.threadView(g), g.thr, g.bodyH))
		}
		if g.memb.w > 0 {
			cols = append(cols, pad(m.membersLines(), g.memb, g.bodyH))
		}
	}
	rows := make([]string, 0, m.h)
	if g.top {
		rows = append(rows, m.topBar(g))
	}
	for i := range g.bodyH {
		var b strings.Builder
		b.WriteString("│")
		for _, c := range cols {
			b.WriteString(c[i])
			b.WriteString("│")
		}
		rows = append(rows, b.String())
	}
	if g.foot {
		rows = append(rows, m.footer(g))
	}
	return strings.Join(rows, "\n")
}
