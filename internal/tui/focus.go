package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// quitWait bounds how long quitting waits to tell the background process
// the conversation is no longer looked at.
const quitWait = 500 * time.Millisecond

// focusReport is what the background process is told the user looks at.
type focusReport struct {
	conv    string
	focused bool
}

// shownConv is the conversation on screen, or "" when the opening screen
// or the narrow chat list hides it.
func (m *Model) shownConv() string {
	if m.splashShown() || m.openID == "" {
		return ""
	}
	if LayoutMode(m.w) <= Narrow && m.listShown() {
		return ""
	}
	return m.openID
}

// syncFocus reports what is shown whenever it differs from what was last
// reported on this connection. One report is out at a time, so they
// arrive in order; failures are ignored. A connection that has never been
// told anything already counts as showing nothing.
func (m *Model) syncFocus() tea.Cmd {
	if m.b == nil || m.procDown || m.focusBusy || m.quitting {
		return nil
	}
	want := focusReport{m.shownConv(), m.termFocused}
	if m.focusSent == nil && want.conv == "" || m.focusSent != nil && *m.focusSent == want {
		return nil
	}
	m.focusSent, m.focusBusy = &want, true
	b := m.b
	return func() tea.Msg {
		_ = b.SetFocus(want.conv, want.focused)
		return focusSetMsg{}
	}
}

// syncRead marks the open conversation read when it is owed and on screen:
// an opened conversation at once, a new message only while the terminal
// has focus and the thread is at its newest message. A mark owed while
// those do not hold waits until they do. One call per conversation is out
// at a time; what is owed meanwhile becomes one more call.
func (m *Model) syncRead() tea.Cmd {
	conv := m.openID
	if !m.opts.MarkRead || conv == "" || m.preview || m.b == nil || m.procDown || m.shownConv() != conv {
		return nil
	}
	newOK := m.readNew && m.termFocused && m.thread != nil && m.thread.atBottom
	if !m.readOpen && !newOK || m.readBusy[conv] {
		return nil
	}
	m.readOpen, m.readNew = false, false
	m.readBusy[conv] = true
	b := m.b
	return func() tea.Msg {
		_ = b.MarkRead(conv)
		return markedMsg{conv: conv}
	}
}

// quit tells the background process the conversation is no longer looked
// at, waiting at most quitWait, then ends the program.
func (m *Model) quit() tea.Cmd {
	m.quitting = true
	b, conv, cancel, after := m.b, m.shownConv(), m.cancel, m.opts.After
	report := b != nil && !m.procDown && m.focusSent != nil
	return func() tea.Msg {
		if report {
			done := make(chan struct{})
			go func() {
				_ = b.SetFocus(conv, false)
				close(done)
			}()
			select {
			case <-done:
			case <-after(quitWait):
			}
		}
		cancel()
		return tea.QuitMsg{}
	}
}

// showPreview shows conv without opening it: the chat list keeps the
// keyboard in standard and wide layouts, the thread in narrow and tiny.
func (m *Model) showPreview(conv string) tea.Cmd {
	cmd := m.open(conv)
	m.preview, m.readOpen = true, false
	m.previewFocus()
	return cmd
}

func (m *Model) previewFocus() {
	m.focus = focusThread
	if LayoutMode(m.w) >= Standard {
		m.focus = focusList
	}
}

// promote turns a preview into an opened conversation, owed a mark read,
// once the user acts on it.
func (m *Model) promote() {
	if m.preview {
		m.preview, m.readOpen = false, true
	}
}
