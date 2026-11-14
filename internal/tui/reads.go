package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/sanitize"
)

func (m *Model) rereadAll() tea.Cmd {
	return tea.Batch(m.readStatus(), m.requestChats(), m.requestThread())
}

func (m *Model) readStatus() tea.Cmd {
	b := m.b
	if b == nil {
		return nil
	}
	return func() tea.Msg {
		st, err := b.Status()
		return statusMsg{b: b, st: st, err: err}
	}
}

// requestChats re-reads the chat list. At most one read is out at a time;
// a request meanwhile queues exactly one more.
func (m *Model) requestChats() tea.Cmd {
	if m.b == nil {
		return nil
	}
	if m.chatsBusy {
		m.chatsAgain = true
		return nil
	}
	m.chatsBusy = true
	b := m.b
	return func() tea.Msg {
		mine, open, err := b.Chats()
		return chatsMsg{b: b, convs: mine, open: open, err: err}
	}
}

func (m *Model) onChats(msg chatsMsg) tea.Cmd {
	m.chatsBusy = false
	var cmds []tea.Cmd
	if m.chatsAgain {
		m.chatsAgain = false
		cmds = append(cmds, m.requestChats())
	}
	if msg.b != m.b {
		return tea.Batch(cmds...)
	}
	if msg.err != nil {
		return tea.Batch(append(cmds, m.failed(msg.err))...)
	}
	cur := m.cursorID()
	m.convs = sortConvs(msg.convs)
	m.openRooms = msg.open
	m.haveList = true
	m.cursor = max(m.indexOf(cur), 0)
	if i := m.indexOf(m.pendingCursor); i >= 0 {
		m.cursor, m.pendingCursor = i, ""
	}
	if m.openID == "" && len(m.convs) > 0 {
		m.cursor = mostRecent(m.convs)
		cmds = append(cmds, m.showPreview(m.convs[m.cursor].ID))
	}
	return tea.Batch(cmds...)
}

// requestThread re-reads the open conversation: the newest messages, as many
// as are held (at least a page), and the unsent items.
func (m *Model) requestThread() tea.Cmd {
	if m.b == nil || m.thread == nil {
		return nil
	}
	if m.threadBusy {
		m.threadAgain = true
		return nil
	}
	m.threadBusy = true
	b, conv := m.b, m.thread.conv
	limit := min(max(len(m.thread.stored), pageSize, m.thread.want), maxReread)
	return func() tea.Msg {
		msgs, err := b.Messages(conv, 0, limit)
		return threadMsg{b: b, conv: conv, limit: limit, msgs: msgs, err: err}
	}
}

func (m *Model) onThread(msg threadMsg) tea.Cmd {
	m.threadBusy = false
	var cmds []tea.Cmd
	if m.threadAgain {
		m.threadAgain = false
		cmds = append(cmds, m.requestThread())
	}
	if msg.b != m.b || m.thread == nil || msg.conv != m.thread.conv {
		return tea.Batch(cmds...)
	}
	if msg.err != nil {
		m.thread.err = sanitize.Line(msg.err.Error())
		return tea.Batch(append(cmds, m.failed(msg.err))...)
	}
	wasLoaded, newest := m.thread.loaded, m.thread.newestSeq()
	m.thread.merge(msg.msgs, msg.limit)
	if wasLoaded && m.thread.newestSeq() > newest {
		m.readNew = true
	}
	if _, ok := m.selected(); !ok {
		m.thread.sel = ""
	}
	if n := m.note; n != nil && n.ask != nil && n.conv == msg.conv && !m.holds(n) {
		cmds = append(cmds, m.dropQuestion(n))
	}
	if m.thread.hasFailed() {
		cmds = append(cmds, m.requestOutbox())
	}
	m.thread.want = 0
	return tea.Batch(append(cmds, m.seekJump())...)
}

// requestOlder loads the page before the oldest held message.
func (m *Model) requestOlder() tea.Cmd {
	t := m.thread
	if m.b == nil || t == nil || m.olderBusy || !t.hasOlder() {
		return nil
	}
	m.olderBusy = true
	b, conv, before := m.b, t.conv, t.stored[0].Seq
	return func() tea.Msg {
		msgs, err := b.Messages(conv, before, pageSize)
		return olderMsg{b: b, conv: conv, before: before, msgs: msgs, err: err}
	}
}

func (m *Model) onOlder(msg olderMsg) tea.Cmd {
	m.olderBusy = false
	if msg.b != m.b || m.thread == nil || msg.conv != m.thread.conv {
		return nil
	}
	if msg.err != nil {
		return m.failed(msg.err)
	}
	m.thread.prepend(msg.before, msg.msgs)
	return m.seekJump()
}

// onEvent treats an event as a hint: it re-reads what the event touches.
func (m *Model) onEvent(ev ipc.Event) tea.Cmd {
	var conv string
	switch ev.Kind {
	case ipc.KindStatus:
		if ev.Status != nil {
			return m.setStatus(*ev.Status)
		}
		return nil
	case ipc.KindMessage:
		if ev.Message != nil {
			conv = ev.Message.ConvID
		}
	case ipc.KindOutbox:
		if ev.Message != nil {
			conv = ev.Message.ConvID
		} else if ev.Outbox != nil {
			conv = ev.Outbox.ConvID
		}
	case ipc.KindConv:
		if ev.Conv != nil {
			conv = ev.Conv.ID
		}
	case ipc.KindNotify:
		return m.onNotify(ev.Notify)
	default:
		return nil
	}
	cmds := []tea.Cmd{m.requestChats()}
	if conv != "" && conv == m.openID {
		cmds = append(cmds, m.requestThread())
	}
	return tea.Batch(cmds...)
}
