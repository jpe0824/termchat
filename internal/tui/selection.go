package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
)

// selectable reports whether msg can be selected: any message that is not
// deleted. What a selection offers beyond copying depends on own.
func (m *Model) selectable(msg ipc.Message) bool {
	return !msg.Deleted
}

// own reports whether msg is the user's own: delivered and not deleted, or
// an unsent item.
func (m *Model) own(msg ipc.Message) bool {
	return msg.Status != "" || m.delivered(msg)
}

// selKeys are the keys of every selectable message, oldest first.
func (m *Model) selKeys() []string {
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

// selectLast selects the user's newest own message.
func (m *Model) selectLast() {
	if m.thread == nil {
		return
	}
	items := m.thread.items()
	for i := len(items) - 1; i >= 0; i-- {
		if m.own(items[i].m) {
			m.thread.sel = items[i].key
			m.focus = focusThread
			m.revealSel()
			return
		}
	}
}

// moveSel steps the selection d messages, stopping at the ends.
func (m *Model) moveSel(d int) {
	keys := m.selKeys()
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
// places the cursor, on any message it selects it, and on a failed one of
// the user's it also retries it. A click on the message already selected
// copies it instead.
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
	// The rule, key hints and empty row under the input are not targets.
	if l.bar && y >= l.inputY+l.inputH {
		return nil
	}
	prev := m.thread.sel
	m.focus, m.thread.sel = focusThread, ""
	it, ok := m.itemAt(g, y-l.msgY)
	if !ok || !m.selectable(it.m) {
		if wasPreview && m.inputUsable(g) {
			m.toInput()
		}
		return nil
	}
	m.thread.sel = it.key
	if it.key == prev {
		return m.copyMsg(it)
	}
	if it.m.Status == ipc.MsgFailed {
		return m.retry(it.m.MsgID)
	}
	return nil
}

// commandKey handles a key when the input does not have it: b is its
// binding, if ok. Only the commands of keyTable's main groups act here.
func (m *Model) commandKey(k tea.KeyPressMsg, g geom, b binding, ok bool) tea.Cmd {
	s := k.String()
	list := m.listActive(g)
	sel, hasSel := m.selected()
	if list {
		hasSel = false
	}
	if !ok {
		// Only the input takes text: here a key that would type does nothing.
		if keyText(k) != "" {
			return m.hintToType(list)
		}
		return nil
	}
	return b.run(m, keyCtx{key: s, g: g, list: list, sel: sel, hasSel: hasSel})
}

func (m *Model) keyHelp(keyCtx) tea.Cmd  { m.help = true; return nil }
func (m *Model) keyQuit(keyCtx) tea.Cmd  { return m.confirmQuit("q") }
func (m *Model) keyTab(c keyCtx) tea.Cmd { m.cycleFocus(c.g); return nil }
func (m *Model) keyEsc(c keyCtx) tea.Cmd { m.escape(c.g, c.list); return nil }

func (m *Model) keyMove(c keyCtx) tea.Cmd {
	d := 1
	if c.key == "up" || c.key == "k" {
		d = -1
	}
	switch {
	case c.list:
		m.move(d)
	case c.hasSel:
		m.moveSel(d)
	default:
		return m.scrollKey(d)
	}
	return nil
}

// keyListEnter opens the chat at the cursor.
func (m *Model) keyListEnter(keyCtx) tea.Cmd {
	if id := m.cursorID(); id != "" {
		return m.open(id)
	}
	return nil
}

// keyThreadEnter goes from the thread to its input.
func (m *Model) keyThreadEnter(c keyCtx) tea.Cmd {
	if m.inputUsable(c.g) {
		m.promote()
		m.toInput()
	}
	return nil
}

func (m *Model) keyPage(c keyCtx) tea.Cmd {
	return m.held(func() tea.Cmd { return m.pageKey(c.key, c.g) })
}

func (m *Model) keySearch(keyCtx) tea.Cmd {
	if m.focus != focusInput {
		return m.openOverlay(ovSearch)
	}
	return nil
}

func (m *Model) keyR(c keyCtx) tea.Cmd {
	if m.procDown {
		return m.startProcess()
	}
	if c.hasSel && c.sel.m.Status == ipc.MsgFailed {
		return m.retry(c.sel.m.MsgID)
	}
	return nil
}

// ownSel is the selected message when it is the user's own delivered one
// and the background process is there to change it.
func (m *Model) ownSel(c keyCtx) bool {
	return c.hasSel && m.delivered(c.sel.m) && m.b != nil && !m.procDown
}

func (m *Model) keyEdit(c keyCtx) tea.Cmd {
	if m.ownSel(c) {
		m.startEdit(c.sel)
	}
	return nil
}

func (m *Model) keyDelete(c keyCtx) tea.Cmd {
	if m.ownSel(c) {
		m.askDelete(c.sel)
	}
	return nil
}

func (m *Model) keyDiscard(c keyCtx) tea.Cmd {
	if c.hasSel && c.sel.m.Status == ipc.MsgFailed {
		m.askDiscard(c.sel)
	}
	return nil
}

// cycleFocus switches between the chat list and the thread in standard and
// wide layouts; the input is reached with i or enter, and left with esc.
func (m *Model) cycleFocus(g geom) {
	if g.mode < Standard {
		return
	}
	if m.focus == focusList && m.openID != "" {
		m.promote()
		m.focus = focusThread
		return
	}
	m.focus = focusList
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
