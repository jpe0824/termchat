package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// quitAgain is how long a first ctrl+c or q waits for the second one when
// drafts would be lost.
const quitAgain = 3 * time.Second

// exitKind is a key that would take the keyboard out of the input, or end
// the program, waiting to be known as more than the first key of a paste.
type exitKind int

const (
	exitNone exitKind = iota
	exitEsc
	exitUp
	exitQuit
	exitGuarded
	exitConfirm
	exitPromote
)

// deferExit acts on k once the paste gap passes with no other key; at once
// when the gap is off.
func (m *Model) deferExit(k exitKind) tea.Cmd {
	if m.opts.PasteGap < 0 {
		return m.doExit(k)
	}
	m.tm.exitGen++
	m.tm.exitKind, m.tm.exitConv = k, m.openID
	return m.opts.Tick(m.opts.PasteGap, exitMsg{gen: m.tm.exitGen})
}

// cancelExit drops a waiting exit because another key followed it; it
// reports whether one was waiting.
func (m *Model) cancelExit() bool {
	was := m.tm.exitKind != exitNone
	m.tm.exitKind = exitNone
	return was
}

func (m *Model) onExitTimer(msg exitMsg) tea.Cmd {
	if msg.gen != m.tm.exitGen || m.tm.exitKind == exitNone {
		return nil
	}
	k := m.tm.exitKind
	m.tm.exitKind = exitNone
	if k != exitQuit && m.tm.exitConv != m.openID {
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
		return m.confirmQuit("ctrl+c")
	case exitGuarded:
		// The key acts on what it was pressed on, or on nothing.
		if m.target(g) == m.tm.exitOn {
			return m.commandKey(m.tm.exitKey, g)
		}
	case exitConfirm:
		return m.confirm(m.tm.exitAsk)
	case exitPromote:
		m.promote()
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
// it first warns, and quits on the same key again within quitAgain.
func (m *Model) confirmQuit(key string) tea.Cmd {
	now := m.opts.Now()
	again := m.tm.quitKey == key && !m.tm.quitAsked.IsZero() && now.Sub(m.tm.quitAsked) < quitAgain
	if !m.hasDrafts() || again {
		return m.quit()
	}
	m.tm.quitAsked, m.tm.quitKey = now, key
	return m.showBanner(m.quitWarning(key), key+" again to quit", quitAgain)
}

func (m *Model) hasDrafts() bool {
	for _, c := range m.drafts {
		if c.unsent() {
			return true
		}
	}
	return false
}
