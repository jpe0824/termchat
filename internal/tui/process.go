package tui

import (
	"errors"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/sanitize"
)

func (m *Model) onLaunch(msg launchMsg) tea.Cmd {
	m.launching = false
	if m.splashTimed {
		m.splashDone = true
	}
	if msg.err != nil || msg.b == nil {
		m.procDown, m.downErr = true, msg.err
		return m.supervise()
	}
	m.b = msg.b
	return tea.Batch(m.supervise(), m.readStatus(), m.requestChats())
}

// startProcess is the user's explicit request to start the background
// process while it is down; reconnects on their own never start it.
func (m *Model) startProcess() tea.Cmd {
	if !m.procDown || m.starting || m.launching || !m.canStart() {
		return nil
	}
	m.starting, m.startErr = true, ""
	start, ctx := m.opts.Start, m.ctx
	return func() tea.Msg {
		b, err := start(ctx)
		return startedMsg{b: b, err: err}
	}
}

// canStart reports whether starting the process could help: not when one
// of another version is running.
func (m *Model) canStart() bool {
	return m.opts.Start != nil && !errors.Is(m.downErr, ipc.ErrVersionMismatch)
}

func (m *Model) onStarted(msg startedMsg) {
	m.starting = false
	if msg.err != nil || msg.b == nil {
		if msg.err == nil {
			msg.err = ipc.ErrClosed
		}
		m.startErr = sanitize.Line(msg.err.Error())
		return
	}
	select {
	case m.adopt <- msg.b:
	default:
		_ = msg.b.Close()
	}
}
