package tui

import (
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/sanitize"
)

// noticeFor is how long an action's error stays up without a key press.
const noticeFor = 5 * time.Second

// notice is the one-line message above the input: an error, or a question
// that y answers. It belongs to one conversation and shows only there.
type notice struct {
	conv string
	text string
	ask  *question
	info bool
}

// question is a y/n question. holds says it still applies as asked; yes is
// what y does then, and no, if set, what y does once it no longer holds.
type question struct {
	text  string
	holds func() bool
	yes   func() tea.Cmd
	no    func() tea.Cmd
}

func (q *question) confirm() tea.Cmd {
	switch {
	case q.holds():
		return q.yes()
	case q.no != nil:
		return q.no()
	}
	return nil
}

// answer takes key s while question q is shown. Any key ends the question;
// y confirms only as a lone press that did not follow the question within
// the paste gap, held as a key of kind, and anything else is the safe
// answer, no.
func (m *Model) answer(q *question, s string, kind keyKind) tea.Cmd {
	if gap := m.opts.PasteGap; s != "y" || gap >= 0 && m.opts.Now().Sub(m.tm.askedAt) < gap {
		return nil
	}
	return m.press(kind, hold{run: q.confirm})
}

// setNotice shows an error; a timed one also clears after noticeFor.
func (m *Model) setNotice(conv, text string, timed bool) tea.Cmd {
	msg := m.noteT.arm(timerNote)
	m.note = &notice{conv: conv, text: sanitize.Line(text)}
	if timed {
		return m.opts.Tick(noticeFor, msg)
	}
	return nil
}

// setInfo shows a note that is not an error, cleared like a timed one.
func (m *Model) setInfo(conv, text string) tea.Cmd {
	cmd := m.setNotice(conv, text, true)
	m.note.info = true
	return cmd
}

// ask puts a yes/no question about doing op to message msgID in the notice
// line; y runs yes.
func (m *Model) ask(text, op, msgID string, yes func() tea.Cmd) {
	conv := m.openID
	m.noteT.arm(timerNote)
	m.note = &notice{conv: conv, text: text, ask: &question{
		text: text, yes: yes,
		holds: func() bool { return m.holdsMsg(conv, op, msgID) },
		no:    func() tea.Cmd { return m.setInfo(conv, "message changed — "+op+" cancelled") },
	}}
	m.tm.askedAt = m.opts.Now()
}

// holdsMsg reports whether message msgID of conv is still held and can
// still have op done to it as asked: its ID alone would act on whatever it
// has become.
func (m *Model) holdsMsg(conv, op, msgID string) bool {
	if m.thread == nil || m.thread.conv != conv {
		return false
	}
	for _, it := range m.thread.items() {
		if it.m.MsgID != msgID {
			continue
		}
		if op == opDiscard && it.m.Status == ipc.MsgFailed || op == opDelete && m.delivered(it.m) {
			return true
		}
	}
	return false
}

// noticeKey lets a key press clear the notice shown, or answer its
// question; done reports that the key was the answer and is used up.
func (m *Model) noticeKey(s string) (cmd tea.Cmd, done bool) {
	n := m.note
	if n == nil {
		return nil, false
	}
	if n.conv != m.openID {
		if n.ask != nil {
			m.note = nil
		}
		return nil, false
	}
	m.note = nil
	if n.ask == nil {
		return nil, false
	}
	// The question checks for itself that its message is still as asked,
	// so its y is not bound to the selection.
	return m.answer(n.ask, s, keyDeferred), true
}

// noteLine is what the line above the input says, if anything.
func (m *Model) noteLine() (string, lipgloss.Style) {
	switch c := m.drafts[m.openID]; {
	case m.note != nil && m.note.conv == m.openID && (m.note.ask != nil || m.note.info):
		return m.note.text, m.st.heading
	case m.note != nil && m.note.conv == m.openID:
		return m.note.text, m.st.bad
	case m.procDown:
		return "background process not running", m.st.faint
	case c != nil && c.edit != nil:
		return "editing — enter saves, esc cancels", m.st.faint
	}
	return "", lipgloss.Style{}
}

// failure words a failed action for the notice line.
func failure(verb string, err error) string {
	switch {
	case errors.Is(err, ipc.ErrNotConnected):
		return "can't " + verb + " while offline"
	case errors.Is(err, ipc.ErrForbidden):
		return "can't " + verb + ": not allowed"
	case errors.Is(err, ipc.ErrNotFound):
		return "can't " + verb + ": message not found"
	case errors.Is(err, ipc.ErrClosed), errors.Is(err, ipc.ErrShuttingDown):
		return "can't " + verb + ": background process not running"
	case errors.Is(err, ipc.ErrTimeout):
		// The request may have taken effect.
		return "no answer to " + verb + " in time — check before trying again"
	}
	return "can't " + verb + ": " + sanitize.Line(err.Error())
}
