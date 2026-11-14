package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
)

// Action names, as the notice line words them.
const (
	opEdit    = "edit"
	opDelete  = "delete"
	opRetry   = "retry"
	opDiscard = "discard"
)

// delivered reports whether msg is the user's own stored message, which
// can be edited and deleted.
func (m *Model) delivered(msg ipc.Message) bool {
	return msg.Status == "" && !msg.Deleted && m.me() != "" && msg.Author == m.me()
}

// startEdit loads msg's text into the input; the draft is set aside until
// the edit ends.
func (m *Model) startEdit(it item) {
	c := m.composeFor(m.openID)
	if c.edit != nil {
		c.in = c.edit.saved
	}
	text := cleanInput(it.m.Text)
	c.edit = &editing{msgID: it.m.MsgID, orig: text, saved: c.in}
	c.in = input{}
	c.in.set(text)
	m.focus = focusInput
}

// cancelEdit drops the edit, restores the draft and returns to the message.
func (m *Model) cancelEdit(c *compose) {
	c.in, c.edit = c.edit.saved, nil
	m.focus = focusThread
}

// endEdit leaves edit mode for the input with the draft restored.
func (m *Model) endEdit(c *compose, conv string) {
	c.in, c.edit = c.edit.saved, nil
	if conv == m.openID && m.thread != nil {
		m.thread.sel = ""
	}
}

// saveEdit sends the edited text. Unchanged or empty text ends the edit
// without a call.
func (m *Model) saveEdit(conv string, c *compose) tea.Cmd {
	if c.saving {
		return nil
	}
	text := strings.TrimRight(c.in.text, "\n")
	if strings.TrimSpace(text) == "" || text == c.edit.orig {
		m.endEdit(c, conv)
		return nil
	}
	if len(text) > model.MaxTextBytes {
		return m.tooLong(conv, text)
	}
	c.saving = true
	id := c.edit.msgID
	return m.act(opEdit, conv, id, func(b Backend) error { return b.Edit(conv, id, text) })
}

func (m *Model) askDelete(it item) {
	conv, id := m.openID, it.m.MsgID
	m.ask("delete this message? y/n", func() tea.Cmd {
		return m.act(opDelete, conv, id, func(b Backend) error { return b.Delete(conv, id) })
	})
}

func (m *Model) askDiscard(it item) {
	conv, id := m.openID, it.m.MsgID
	m.ask("discard this message? y/n", func() tea.Cmd {
		return m.act(opDiscard, conv, id, func(b Backend) error { return b.Discard(id) })
	})
}

func (m *Model) retry(id string) tea.Cmd {
	return m.act(opRetry, m.openID, id, func(b Backend) error { return b.Retry(id) })
}

// act runs one backend write for a message of conv.
func (m *Model) act(op, conv, id string, call func(Backend) error) tea.Cmd {
	b := m.b
	if b == nil {
		return nil
	}
	return func() tea.Msg {
		return actionMsg{b: b, op: op, conv: conv, msgID: id, err: call(b)}
	}
}

// onAction shows a failure in the conversation it was for, if that is
// still open; a saved edit ends edit mode in its own conversation.
func (m *Model) onAction(msg actionMsg) tea.Cmd {
	if msg.op == opEdit {
		if c := m.drafts[msg.conv]; c != nil {
			c.saving = false
			if msg.err == nil && c.edit != nil && c.edit.msgID == msg.msgID {
				m.endEdit(c, msg.conv)
			}
		}
	}
	if msg.conv != m.openID {
		return nil
	}
	if msg.err != nil {
		return m.setNotice(msg.conv, failure(msg.op, msg.err), true)
	}
	if msg.b == m.b {
		return m.requestThread()
	}
	return nil
}

// requestOutbox reads why the open conversation's unsent items failed.
// Like the other reads, one is out at a time and one more may queue.
func (m *Model) requestOutbox() tea.Cmd {
	if m.b == nil || m.thread == nil {
		return nil
	}
	if m.outboxBusy {
		m.outboxAgain = true
		return nil
	}
	m.outboxBusy = true
	b, conv := m.b, m.thread.conv
	return func() tea.Msg {
		items, err := b.Outbox(conv)
		return outboxMsg{b: b, conv: conv, items: items, err: err}
	}
}

func (m *Model) onOutbox(msg outboxMsg) tea.Cmd {
	m.outboxBusy = false
	var cmd tea.Cmd
	if m.outboxAgain {
		m.outboxAgain = false
		cmd = m.requestOutbox()
	}
	if msg.b != m.b || m.thread == nil || msg.conv != m.thread.conv || msg.err != nil {
		return cmd
	}
	m.thread.reasons = map[string]string{}
	for _, it := range msg.items {
		m.thread.reasons[it.MsgID] = it.Err
	}
	return cmd
}
