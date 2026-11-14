package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// quitAgain is how long a first ctrl+c or q waits for the second one when
// drafts would be lost.
const quitAgain = 3 * time.Second

const quitNote = "ctrl+c again to quit — unsent drafts will be lost"

// exitKind is a key that would take the keyboard out of the input, or end
// the program, waiting to be known as more than the first key of a paste.
type exitKind int

const (
	exitNone exitKind = iota
	exitEsc
	exitUp
	exitQuit
)

// deferExit acts on k once the paste gap passes with no other key; at once
// when the gap is off.
func (m *Model) deferExit(k exitKind) tea.Cmd {
	if m.opts.PasteGap < 0 {
		return m.doExit(k)
	}
	m.exitGen++
	m.exitKind, m.exitConv = k, m.openID
	return m.opts.Tick(m.opts.PasteGap, exitMsg{gen: m.exitGen})
}

// cancelExit drops a waiting exit because another key followed it; it
// reports whether one was waiting.
func (m *Model) cancelExit() bool {
	was := m.exitKind != exitNone
	m.exitKind = exitNone
	return was
}

func (m *Model) onExitTimer(msg exitMsg) tea.Cmd {
	if msg.gen != m.exitGen || m.exitKind == exitNone {
		return nil
	}
	k := m.exitKind
	m.exitKind = exitNone
	if k != exitQuit && m.exitConv != m.openID {
		return nil
	}
	return m.doExit(k)
}

func (m *Model) doExit(k exitKind) tea.Cmd {
	g := m.geom()
	switch k {
	case exitEsc:
		if m.focus == focusInput && m.threadShown(g) {
			m.leaveInput(g, m.composeFor(m.openID))
		}
	case exitUp:
		if c := m.drafts[m.openID]; m.inputActive(g) && (c == nil || c.in.text == "" && c.edit == nil) {
			m.selectLast()
		}
	case exitQuit:
		return m.confirmQuit()
	}
	return nil
}

// ctrlC is the way out, but never from inside a paste: one that follows
// another key within the gap is ignored, and one that another key follows
// within the gap is cancelled.
func (m *Model) ctrlC(recent bool) tea.Cmd {
	if recent {
		return nil
	}
	return m.deferExit(exitQuit)
}

// confirmQuit quits, except that with unsent drafts or an edit in progress
// it first asks for the same key again within quitAgain.
func (m *Model) confirmQuit() tea.Cmd {
	now := m.opts.Now()
	if !m.hasDrafts() || !m.quitAsked.IsZero() && now.Sub(m.quitAsked) < quitAgain {
		return m.quit()
	}
	m.quitAsked = now
	return m.setInfo(m.openID, quitNote)
}

func (m *Model) hasDrafts() bool {
	for _, c := range m.drafts {
		if c.in.text != "" || c.edit != nil {
			return true
		}
	}
	return false
}
