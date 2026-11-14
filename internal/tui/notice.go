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
	ask  func() tea.Cmd
	info bool
}

// setNotice shows an error; a timed one also clears after noticeFor.
func (m *Model) setNotice(conv, text string, timed bool) tea.Cmd {
	m.noteGen++
	m.note = &notice{conv: conv, text: sanitize.Line(text)}
	if timed {
		return m.opts.Tick(noticeFor, noticeExpiredMsg{gen: m.noteGen})
	}
	return nil
}

// setInfo shows a note that is not an error, cleared like a timed one.
func (m *Model) setInfo(conv, text string) tea.Cmd {
	cmd := m.setNotice(conv, text, true)
	m.note.info = true
	return cmd
}

// ask puts a yes/no question in the notice line; y runs yes.
func (m *Model) ask(question string, yes func() tea.Cmd) {
	m.noteGen++
	m.note = &notice{conv: m.openID, text: question, ask: yes}
	m.tm.askedAt = m.opts.Now()
}

func (m *Model) onNoticeExpired(msg noticeExpiredMsg) {
	if msg.gen == m.noteGen {
		m.note = nil
	}
}

// noticeKey lets a key press clear the notice shown, or answer its
// question; done reports that the key was the answer and is used up.
func (m *Model) noticeKey(s string, recent bool) (cmd tea.Cmd, done bool) {
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
	return m.answer(n, s, recent), true
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
