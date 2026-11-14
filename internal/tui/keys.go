package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// helpKeys are the keys that work, for the ? overlay.
var helpKeys = [][2]string{
	{"enter", "input: send, or save an edit. chat list: open. thread: go to the input"},
	{"alt+enter", "input: new line (also ctrl+j)"},
	{"esc", "input: cancel an edit, else leave the input. thread: go to the input or the list"},
	{"tab", "input: types a tab. chat list and thread: switch between them"},
	{"↑ ↓  k j", "chat list: move. thread: step through your messages, or scroll"},
	{"↑", "empty input: select your newest message"},
	{"i", "thread: go to the input"},
	{"e  d", "thread: edit or delete your selected message"},
	{"r  x", "thread: retry or discard a failed message. r: start a stopped background process"},
	{"pgup pgdn", "scroll the thread"},
	{"home end", "oldest / newest message"},
	{"?", "show or hide this help"},
	{"q  ctrl+c", "quit (q outside the input); asked twice when drafts are unsent"},
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
	recent := m.stamp()
	pending := m.cancelExit()
	m.clearBanner()
	if s == "ctrl+c" {
		if pending {
			return nil
		}
		return m.ctrlC(recent)
	}
	if m.splashShown() {
		switch {
		case s == "q" && m.noAccount:
			return m.confirmQuit("q")
		case !m.noAccount:
			m.splashDone = true
		}
		return nil
	}
	burst := m.inBurst() || recent
	m.flushEnter()
	if cmd, done := m.noticeKey(s, recent); done {
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
		return m.inputKey(k, g, burst)
	}
	if m.focus == focusInput && m.threadShown(g) {
		return m.disabledKey(k, g, burst)
	}
	if m.focus == focusInput || m.tm.swallow && recent {
		// No input to type into: the key, and the rest of its burst, are
		// dropped, never run as commands.
		m.focus, m.tm.swallow = focusList, true
		return nil
	}
	m.tm.swallow = false
	return m.guardKey(k, g, recent)
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

const downNote = "background process not running — r to start, esc to leave the input"

// disabledKey handles a key for the input while the background process is
// down: nothing typed may run as a command. r starts the process, a lone
// esc leaves the input, and every other key leaves a note saying so.
func (m *Model) disabledKey(k tea.KeyPressMsg, g geom, burst bool) tea.Cmd {
	switch s := k.String(); s {
	case "r":
		return m.startProcess()
	case "esc":
		if burst {
			return nil
		}
		return m.deferExit(exitEsc)
	case "pgup", "pgdown":
		return m.pageKey(s, g)
	}
	return m.setNotice(m.openID, downNote, false)
}
