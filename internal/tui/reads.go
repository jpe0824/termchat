package tui

import (
	"strings"

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
	if !m.chatsRead.begin() {
		return nil
	}
	b := m.b
	return func() tea.Msg {
		mine, open, err := b.Chats()
		return chatsMsg{b: b, convs: mine, open: open, err: err}
	}
}

func (m *Model) onChats(msg chatsMsg) tea.Cmd {
	var cmds []tea.Cmd
	if m.chatsRead.end() {
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
	m.thread.owe(0)
	return m.readThread()
}

// readThread sends the re-read the thread is owed, unless one is out. One
// owed for events about messages reaches back only to the oldest of them,
// or to the oldest message held if that is newer; any other re-read covers
// the whole held window.
func (m *Model) readThread() tea.Cmd {
	t := m.thread
	if m.b == nil || t == nil || !m.threadRead.begin() {
		return nil
	}
	b, conv := m.b, t.conv
	limit := min(max(len(t.stored), pageSize, t.want), maxReread)
	var reach int64
	if t.owed && t.need > 0 && t.loaded && t.want == 0 && len(t.stored) > 0 {
		newest := t.newestSeq()
		reach = min(max(t.need, t.stored[0].Seq), newest)
		limit = int(min(int64(limit), max(t.need, newest)-reach+1))
	}
	t.owed, t.need = false, 0
	return func() tea.Msg {
		msgs, err := b.Messages(conv, 0, limit)
		return threadMsg{b: b, conv: conv, limit: limit, reach: reach, msgs: msgs, err: err}
	}
}

func (m *Model) onThread(msg threadMsg) tea.Cmd {
	var cmds []tea.Cmd
	if m.threadRead.end() {
		cmds = append(cmds, m.readThread())
	}
	if m.stale(msg.b, msg.conv) {
		return tea.Batch(cmds...)
	}
	if msg.err != nil {
		m.thread.err = sanitize.Line(msg.err.Error())
		return tea.Batch(append(cmds, m.failed(msg.err))...)
	}
	if gap(msg) {
		// More arrived than the read was sized for: what lies between it and
		// what is held is unknown, so the whole window is read instead.
		return tea.Batch(append(cmds, m.requestThread())...)
	}
	wasLoaded, newest := m.thread.loaded, m.thread.newestSeq()
	m.thread.merge(msg.msgs, msg.limit)
	if wasLoaded && m.thread.newestSeq() > newest {
		m.readNew = true
	}
	if _, ok := m.selected(); !ok {
		m.thread.sel = ""
	}
	if n := m.note; n != nil && n.ask != nil && n.conv == msg.conv && !n.ask.holds() {
		cmds = append(cmds, n.ask.no())
	}
	if m.thread.hasFailed() {
		cmds = append(cmds, m.requestOutbox())
	}
	m.thread.want = 0
	return tea.Batch(append(cmds, m.seekJump())...)
}

// gap reports whether a read sized to reach a message came back full
// without reaching it.
func gap(msg threadMsg) bool {
	if msg.reach == 0 {
		return false
	}
	n, oldest := 0, int64(0)
	for _, mg := range msg.msgs {
		if mg.Status == "" {
			if n == 0 {
				oldest = mg.Seq
			}
			n++
		}
	}
	return n >= msg.limit && oldest > msg.reach
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
	if m.stale(msg.b, msg.conv) {
		return nil
	}
	if msg.err != nil {
		return m.failed(msg.err)
	}
	m.thread.prepend(msg.before, msg.msgs)
	return m.seekJump()
}

// onEvents treats events as hints: it re-reads what they touch, each
// read once for the lot, and writes their notifications in order.
func (m *Model) onEvents(evs []ipc.Event) tea.Cmd {
	var cmds []tea.Cmd
	var notes []string
	chats, thread := false, false
	for _, ev := range evs {
		var conv string
		var seq int64
		switch ev.Kind {
		case ipc.KindStatus:
			if ev.Status != nil {
				cmds = append(cmds, m.setStatus(*ev.Status))
			}
			continue
		case ipc.KindMessage:
			if ev.Message != nil {
				conv, seq = ev.Message.ConvID, ev.Message.Seq
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
			if s := m.notification(ev.Notify); s != "" {
				notes = append(notes, s)
			}
			continue
		default:
			continue
		}
		chats = true
		if conv != "" && conv == m.openID && m.thread != nil {
			m.thread.owe(seq)
			thread = true
		}
	}
	if chats {
		cmds = append(cmds, m.requestChats())
	}
	if thread {
		cmds = append(cmds, m.readThread())
	}
	if len(notes) > 0 {
		// One write keeps the notifications in the order they came.
		cmds = append(cmds, tea.Raw(strings.Join(notes, "")))
	}
	return tea.Batch(cmds...)
}
