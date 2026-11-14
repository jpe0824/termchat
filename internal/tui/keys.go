package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// helpKeys are the keys that work, for the ? overlay.
var helpKeys = [][2]string{
	{"↑ ↓  k j", "move in the chat list"},
	{"enter", "open the selected chat"},
	{"tab", "switch between chat list and thread"},
	{"esc", "back to the chat list (narrow windows)"},
	{"pgup pgdn", "scroll the thread"},
	{"home end", "oldest / newest message"},
	{"r", "start the background process when it is not running"},
	{"?", "show or hide this help"},
	{"q  ctrl+c", "quit"},
	{"mouse", "click a chat to open it; the wheel scrolls"},
}

func (m *Model) helpLines() []string {
	out := []string{m.st.heading.Render("Keys"), ""}
	for _, k := range helpKeys {
		out = append(out, fit(k[0], 12)+k[1])
	}
	return out
}

func (m *Model) quit() tea.Cmd {
	m.cancel()
	return tea.Quit
}

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
	if s == "q" {
		return m.quit()
	}
	if m.help {
		if s == "?" || s == "esc" {
			m.help = false
		}
		return nil
	}
	g := m.geom()
	_, vh := m.threadPane(g)
	page := max(vh-1, 1)
	switch s {
	case "?":
		m.help = true
	case "tab":
		if g.mode >= Standard {
			m.focus = 1 - m.focus
		}
	case "esc":
		if g.mode <= Narrow && !m.listShown() {
			m.showList = true
			m.cursor = max(m.indexOf(m.openID), 0)
		}
	case "up", "k":
		if m.listActive(g) {
			m.move(-1)
			return nil
		}
		return m.scroll(-1)
	case "down", "j":
		if m.listActive(g) {
			m.move(1)
			return nil
		}
		return m.scroll(1)
	case "enter":
		if m.listActive(g) {
			if id := m.cursorID(); id != "" {
				return m.open(id)
			}
		}
	case "pgup":
		return m.scroll(-page)
	case "pgdown":
		return m.scroll(page)
	case "home":
		return m.scrollToTop()
	case "end":
		return m.scrollToBottom()
	case "r":
		return m.startProcess()
	}
	return nil
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
			m.focus = paneSidebar
		}
		if i := m.sidebarHit(g, y); i >= 0 {
			return m.open(m.convs[i].ID)
		}
	case g.thr.has(p.X):
		if g.mode >= Standard {
			m.focus = paneThread
			return nil
		}
		marker := lipgloss.Width(m.backMarker(g.mode == Tiny))
		if y == 0 && p.X >= g.thr.x && p.X <= g.thr.x+marker {
			m.showList = true
			m.cursor = max(m.indexOf(m.openID), 0)
		}
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
		return m.scroll(d)
	}
	return nil
}
