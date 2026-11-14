package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// onKey routes a key: to the opening screen, a question in the notice
// line, the help overlay, the input when it has focus, and otherwise to
// the single-key commands.
func (m *Model) onKey(k tea.KeyPressMsg) tea.Cmd {
	s := k.String()
	recent := m.stamp()
	pending := m.cancelExit()
	if !recent || !m.tm.hint {
		m.clearBanner()
	}
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
		m.helpKey(s)
		return nil
	}
	if m.ov != nil {
		return m.overlayKey(k, recent)
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
	// A command key waiting out the paste gap was pressed for what was
	// selected then, which a click is about to change.
	if k := m.tm.exitKind; k == exitGuarded || k == exitOverlay || k == exitOvConfirm {
		m.cancelExit()
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
	if m.ov != nil {
		return m.ovClick(p.X, p.Y)
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
	if m.splashShown() {
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
	case m.help:
		m.helpTop += d
		m.clampHelp(g)
		return nil
	case m.ov != nil:
		m.ovWheel(d < 0)
		return nil
	}
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
// down: nothing typed may run as a command. A lone r starts the process, a
// lone esc leaves the input, and every other key leaves a note saying so.
func (m *Model) disabledKey(k tea.KeyPressMsg, g geom, burst bool) tea.Cmd {
	switch s := k.String(); s {
	case "r":
		if !burst || m.opts.PasteGap < 0 {
			return m.guardKey(k, g, false)
		}
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
