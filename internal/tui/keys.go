package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// helpKeys are the keys that work, for the ? overlay.
var helpKeys = [][2]string{
	{"enter", "send; open the selected chat; edit: save"},
	{"alt+enter", "new line (also ctrl+j)"},
	{"esc", "leave the input; cancel an edit; back to the chat list"},
	{"tab", "chat list, thread, input"},
	{"↑ ↓  k j", "move in the chat list; ↑ in an empty input selects your messages"},
	{"e  d", "edit or delete your selected message"},
	{"r  x", "retry or discard a failed message; r starts a stopped background process"},
	{"i", "type in the input"},
	{"pgup pgdn", "scroll the thread"},
	{"home end", "oldest / newest message"},
	{"?", "show or hide this help"},
	{"q  ctrl+c", "quit (q outside the input)"},
	{"mouse", "click a chat, your message or the input; the wheel scrolls"},
}

func (m *Model) helpLines() []string {
	out := []string{m.st.heading.Render("Keys"), ""}
	for _, k := range helpKeys {
		out = append(out, fit(k[0], 12)+k[1])
	}
	return out
}

// onKey routes a key: to the opening screen, a question in the notice
// line, the help overlay, the input when it has focus, and otherwise to
// the single-key commands.
func (m *Model) onKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	if s == "ctrl+c" {
		return m.quit()
	}
	if m.splashShown() {
		switch {
		case s == "q" && m.noAccount:
			return m.quit()
		case !m.noAccount:
			m.splashDone = true
		}
		return nil
	}
	m.flushEnter()
	if cmd, done := m.noticeKey(s); done {
		return cmd
	}
	if m.help {
		if s == "?" || s == "esc" {
			m.help = false
		}
		return nil
	}
	g := m.geom()
	if m.inputActive(g) {
		return m.inputKey(k, g)
	}
	return m.commandKey(k, g)
}

func (m *Model) onClick(p tea.Mouse) tea.Cmd {
	if m.splashShown() {
		if !m.noAccount {
			m.splashDone = true
		}
		return nil
	}
	if p.Button != tea.MouseLeft {
		return nil
	}
	// A click moves on from a question, so a later y cannot act on a
	// message the user has left.
	if m.note != nil && m.note.ask != nil {
		m.note = nil
	}
	if m.help {
		m.help = false
		return nil
	}
	g := m.geom()
	y := p.Y - g.bodyY
	if y < 0 || y >= g.bodyH {
		return nil
	}
	switch {
	case g.side.has(p.X):
		if g.mode >= Standard {
			m.focus = focusList
		}
		if i := m.sidebarHit(g, y); i >= 0 {
			return m.open(m.convs[i].ID)
		}
	case g.thr.has(p.X):
		marker := lipgloss.Width(m.backMarker(g.mode == Tiny))
		if g.mode <= Narrow && y == 0 && p.X >= g.thr.x && p.X <= g.thr.x+marker {
			m.toList(g)
			return nil
		}
		return m.clickThread(g, p.X, y)
	}
	return nil
}

func (m *Model) onWheel(p tea.Mouse) tea.Cmd {
	if m.splashShown() || m.help {
		return nil
	}
	d := 3
	switch p.Button {
	case tea.MouseWheelUp:
		d = -3
	case tea.MouseWheelDown:
	default:
		return nil
	}
	g := m.geom()
	switch {
	case g.side.has(p.X):
		m.move(d / 3)
	case g.thr.has(p.X):
		l, y := m.tlayout(g), p.Y-g.bodyY
		if c := m.drafts[m.openID]; c != nil && m.threadShown(g) && y >= l.inputY && y < l.inputY+l.inputH &&
			c.in.overflows(m.inputWidth(g), l.inputH) {
			c.in.scroll(d, m.inputWidth(g), l.inputH)
			return nil
		}
		return m.scroll(d)
	}
	return nil
}
