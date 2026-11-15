package tui

import tea "charm.land/bubbletea/v2"

// timerID says what a timer is for.
type timerID int

const (
	timerNote timerID = iota + 1
	timerBanner
	timerFlash
	timerExit
	timerEnter
	timerSearch
)

// timer numbers the armings of one timer; only the latest is live, so
// arming it again, or counting it on without arming, makes an earlier one
// that is still running stale.
type timer int

// arm counts the timer on and returns the message its tick delivers.
func (t *timer) arm(id timerID) timerMsg {
	*t++
	return timerMsg{id: id, gen: *t}
}

func (t timer) live(msg timerMsg) bool { return msg.gen == t }

// onTimer handles a timer's tick, if it is still live.
func (m *Model) onTimer(msg timerMsg) tea.Cmd {
	switch msg.id {
	case timerNote:
		if m.noteT.live(msg) {
			m.note = nil
		}
	case timerBanner:
		if m.tm.bannerT.live(msg) {
			m.clearBanner()
		}
	case timerFlash:
		if m.flashT.live(msg) && m.thread != nil {
			m.thread.flash = ""
		}
	case timerExit:
		return m.onExitTimer(msg)
	case timerEnter:
		return m.onEnterTimer(msg)
	case timerSearch:
		return m.onSearchTick(msg)
	}
	return nil
}
