package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/sanitize"
)

// ovAnswer takes a key while the overlay asks a question: any key ends it,
// and only a lone y, not right after the question appeared, says yes.
func (m *Model) ovAnswer(s string, recent bool) tea.Cmd {
	a := m.ov.ask
	m.ov.ask = nil
	if s != "y" {
		return nil
	}
	gap := m.opts.PasteGap
	if gap < 0 {
		return m.ovConfirm(a)
	}
	if recent || m.opts.Now().Sub(m.tm.askedAt) < gap {
		return nil
	}
	m.tm.ovAsk, m.tm.exitOn = a, m.target(m.geom())
	return m.deferExit(exitOvConfirm)
}

func (m *Model) ovConfirm(a *ovAsk) tea.Cmd {
	if m.ov == nil || !a.holds() {
		return nil
	}
	return a.yes()
}

func (m *Model) ovAsk(text string, holds func() bool, yes func() tea.Cmd) {
	m.ov.ask = &ovAsk{text: text, holds: holds, yes: yes}
	m.tm.askedAt = m.opts.Now()
}

// ovCall runs one backend write for the overlay; its answer comes back as
// ovDoneMsg and is dropped if the overlay has closed meanwhile. done is
// what the overlay says when it worked and stays open.
func (m *Model) ovCall(op, done string, call func(Backend) (convID string, err error)) tea.Cmd {
	ov := m.ov
	if m.b == nil || m.procDown {
		ov.err = "background process not running"
		return nil
	}
	ov.busy, ov.err, ov.info = true, "", ""
	b, gen := m.b, ov.gen
	return func() tea.Msg {
		id, err := call(b)
		return ovDoneMsg{b: b, gen: gen, op: op, done: done, conv: id, err: err}
	}
}

func (m *Model) onOvDone(msg ovDoneMsg) tea.Cmd {
	ov := m.ov
	if ov == nil || ov.gen != msg.gen {
		// The overlay has closed; a failure is still the user's to know.
		if msg.err == nil {
			return nil
		}
		text := failure(opVerb[msg.op], msg.err)
		if m.openID == "" {
			return m.setBanner(text)
		}
		return m.setNotice(m.openID, text, true)
	}
	ov.busy = false
	if msg.err != nil {
		ov.err = failure(opVerb[msg.op], msg.err)
		return nil
	}
	switch msg.op {
	case ipc.RoomInvite, ipc.RoomRemove, ipc.RoomRename:
		m.ovStepTo(stepMembers, "")
		ov.info = sanitize.Line(msg.done)
		return m.requestChats()
	case ipc.RoomLeave:
		m.closeOverlay()
		if m.openID == msg.conv {
			m.openID, m.thread, m.preview = "", nil, false
			m.focus = focusList
		}
		return m.requestChats()
	}
	m.closeOverlay()
	m.pendingCursor = msg.conv
	return tea.Batch(m.requestChats(), m.open(msg.conv))
}

// opVerb words each overlay write for an error.
var opVerb = map[string]string{
	opOpenDM: "open the chat", ipc.RoomCreate: "create the room", ipc.RoomJoin: "join the room",
	ipc.RoomInvite: "invite", ipc.RoomRemove: "remove", ipc.RoomRename: "rename the room",
	ipc.RoomLeave: "leave the room",
}

const opOpenDM = "opendm"
