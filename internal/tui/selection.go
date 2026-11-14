package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
)

// selectable reports whether msg can be selected: the user's own messages
// that are not deleted, and their unsent items.
func (m *Model) selectable(msg ipc.Message) bool {
	return msg.Status != "" || m.delivered(msg)
}

func (m *Model) ownKeys() []string {
	if m.thread == nil {
		return nil
	}
	var keys []string
	for _, it := range m.thread.items() {
		if m.selectable(it.m) {
			keys = append(keys, it.key)
		}
	}
	return keys
}

// selected is the selected message, if it is still there and selectable.
func (m *Model) selected() (item, bool) {
	if m.thread == nil || m.thread.sel == "" {
		return item{}, false
	}
	for _, it := range m.thread.items() {
		if it.key == m.thread.sel && m.selectable(it.m) {
			return it, true
		}
	}
	return item{}, false
}

// selectLast selects the user's newest message.
func (m *Model) selectLast() {
	keys := m.ownKeys()
	if len(keys) == 0 {
		return
	}
	m.thread.sel = keys[len(keys)-1]
	m.focus = focusThread
	m.revealSel()
}

// moveSel steps the selection d of the user's messages, stopping at the ends.
func (m *Model) moveSel(d int) {
	keys := m.ownKeys()
	for i, k := range keys {
		if k == m.thread.sel {
			m.thread.sel = keys[min(max(i+d, 0), len(keys)-1)]
			m.revealSel()
			return
		}
	}
}

// revealSel scrolls just enough to show the whole selected message.
func (m *Model) revealSel() {
	g := m.geom()
	if m.thread == nil || g.thr.w == 0 {
		return
	}
	width, vh := m.threadPane(g)
	lines := m.threadLines(width, g.mode)
	first, last := -1, -1
	for i, l := range lines {
		if l.key == m.thread.sel {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return
	}
	top := m.thread.top(lines, vh)
	switch {
	case first < top:
		m.thread.setTop(lines, first, vh)
	case last >= top+vh:
		m.thread.setTop(lines, max(last-vh+1, first), vh)
	}
}

// itemAt is the message drawn at row y of the message area.
func (m *Model) itemAt(g geom, y int) (item, bool) {
	width, vh := m.threadPane(g)
	lines := m.threadLines(width, g.mode)
	i := m.thread.top(lines, vh) + y
	if y < 0 || y >= vh || i >= len(lines) || lines[i].sep {
		return item{}, false
	}
	for _, it := range m.thread.items() {
		if it.key == lines[i].key {
			return it, true
		}
	}
	return item{}, false
}

// clickThread handles a click at row y of the thread pane: in the input it
// places the cursor, on the user's message it selects it, and on a failed
// one it also retries it.
func (m *Model) clickThread(g geom, x, y int) tea.Cmd {
	if !m.threadShown(g) {
		return nil
	}
	l := m.tlayout(g)
	wasPreview := m.preview
	m.promote()
	if y >= l.inputY && y < l.inputY+l.inputH {
		if m.inputUsable(g) {
			m.toInput()
			m.composeFor(m.openID).in.place(y-l.inputY, x-(g.thr.x+3), m.inputWidth(g))
		}
		return nil
	}
	m.focus, m.thread.sel = focusThread, ""
	it, ok := m.itemAt(g, y-l.msgY)
	if !ok || !m.selectable(it.m) {
		if wasPreview && m.inputUsable(g) {
			m.toInput()
		}
		return nil
	}
	m.thread.sel = it.key
	if it.m.Status == ipc.MsgFailed {
		return m.retry(it.m.MsgID)
	}
	return nil
}

// commandKey handles a key when the input does not have it.
func (m *Model) commandKey(k tea.KeyPressMsg, g geom) tea.Cmd {
	s := k.String()
	list := m.listActive(g)
	sel, hasSel := m.selected()
	if list {
		hasSel = false
	}
	switch s {
	case "q":
		return m.quit()
	case "?":
		m.help = true
	case "tab":
		m.cycleFocus(g)
	case "esc":
		m.escape(g, list)
	case "up", "k", "down", "j":
		d := 1
		if s == "up" || s == "k" {
			d = -1
		}
		switch {
		case list:
			m.move(d)
		case hasSel:
			m.moveSel(d)
		default:
			return m.scroll(d)
		}
	case "enter", "i":
		if list && s == "enter" {
			if id := m.cursorID(); id != "" {
				return m.open(id)
			}
		} else if !list && m.inputUsable(g) {
			m.promote()
			m.toInput()
		}
	case "pgup", "pgdown", "home", "end":
		return m.pageKey(s, g)
	case "r":
		if m.procDown {
			return m.startProcess()
		}
		if hasSel && sel.m.Status == ipc.MsgFailed {
			return m.retry(sel.m.MsgID)
		}
	case "e", "d":
		if hasSel && m.delivered(sel.m) && m.b != nil && !m.procDown {
			if s == "e" {
				m.startEdit(sel)
			} else {
				m.askDelete(sel)
			}
		}
	case "x":
		if hasSel && sel.m.Status == ipc.MsgFailed {
			m.askDiscard(sel)
		}
	default:
		if t := keyText(k); t != "" && !list && m.inputUsable(g) {
			m.promote()
			m.toInput()
			m.composeFor(m.openID).in.insert(cleanInput(t))
		}
	}
	return nil
}

// cycleFocus steps chat list → thread → input in standard and wide layouts.
func (m *Model) cycleFocus(g geom) {
	if g.mode < Standard {
		return
	}
	switch {
	case m.focus == focusList && m.openID != "":
		m.promote()
		m.focus = focusThread
	case m.focus == focusThread && m.inputUsable(g):
		m.promote()
		m.toInput()
	default:
		m.focus = focusList
	}
}

// escape leaves a selection for the input, and the thread for the list.
func (m *Model) escape(g geom, list bool) {
	switch {
	case list:
	case m.thread != nil && m.thread.sel != "":
		m.thread.sel = ""
		if m.inputUsable(g) {
			m.focus = focusInput
		}
	default:
		m.toList(g)
	}
}

func (m *Model) pageKey(s string, g geom) tea.Cmd {
	_, vh := m.threadPane(g)
	page := max(vh-1, 1)
	switch s {
	case "pgup":
		return m.scroll(-page)
	case "pgdown":
		return m.scroll(page)
	case "home":
		return m.scrollToTop()
	}
	return m.scrollToBottom()
}
