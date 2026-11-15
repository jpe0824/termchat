package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// quitAgain is how long a first ctrl+c or q waits for the second one when
// drafts would be lost.
const quitAgain = 3 * time.Second

// hold is a key waiting out the paste gap before it acts, as what it
// captured when pressed. It acts only in the conversation it was pressed
// in, unless any is set; a bound hold acts only on the target it was
// pressed on, and a click cancels it.
type hold struct {
	conv   string
	any    bool
	bound  bool
	target string
	run    func() tea.Cmd
}

// press runs h for a key of kind by the paste rules: at once when the key
// is immediate or the gap is off; never when it followed another key
// within the gap; otherwise once the gap passes with no other key. A
// guarded key's hold is bound to what was selected when it was pressed.
func (m *Model) press(kind keyKind, h hold) tea.Cmd {
	switch {
	case kind == keyImmediate || m.opts.PasteGap < 0:
		return h.run()
	case m.tm.recent:
		return nil
	}
	if kind == keyGuarded {
		h.bound, h.target = true, m.target(m.geom())
	}
	return m.wait(h)
}

// wait runs h once the paste gap passes with no other key; at once when
// the gap is off.
func (m *Model) wait(h hold) tea.Cmd {
	if m.opts.PasteGap < 0 {
		return h.run()
	}
	h.conv = m.openID
	m.tm.pending = &h
	return m.opts.Tick(m.opts.PasteGap, m.tm.exitT.arm(timerExit))
}

// cancelExit drops a waiting key because another key followed it; it
// reports whether one was waiting.
func (m *Model) cancelExit() bool {
	was := m.tm.pending != nil
	m.tm.pending = nil
	return was
}

func (m *Model) onExitTimer(msg timerMsg) tea.Cmd {
	h := m.tm.pending
	if !m.tm.exitT.live(msg) || h == nil {
		return nil
	}
	m.tm.pending = nil
	if !h.any && h.conv != m.openID || h.bound && m.target(m.geom()) != h.target {
		return nil
	}
	return h.run()
}

// ctrlC is the way out, but never from inside a paste: one that follows
// another key within the gap is ignored, and one that another key follows
// within the gap is cancelled.
func (m *Model) ctrlC() tea.Cmd {
	return m.press(keyDeferred, hold{any: true, run: func() tea.Cmd { return m.confirmQuit("ctrl+c") }})
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
	return m.showBanner(bannerNote{text: m.quitWarning(key), short: key + " again to quit"}, quitAgain)
}

func (m *Model) hasDrafts() bool {
	for _, c := range m.drafts {
		if c.unsent() {
			return true
		}
	}
	return false
}
