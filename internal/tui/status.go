package tui

import (
	"errors"
	"net"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/sanitize"
)

// failed handles a read error: a gone or stopping background process is
// shown in the top bar while the subscription goroutine reconnects.
func (m *Model) failed(err error) tea.Cmd {
	switch {
	case errors.Is(err, ipc.ErrNoAccount):
		return m.setNoAccount()
	case errors.Is(err, ipc.ErrClosed), errors.Is(err, ipc.ErrShuttingDown):
		m.procDown = true
	}
	return nil
}

// setNoAccount keeps the opening screen up and asks for the status every
// pollEvery until an account appears; one poll is pending at a time.
func (m *Model) setNoAccount() tea.Cmd {
	m.noAccount = true
	if m.polling {
		return nil
	}
	m.polling = true
	return m.opts.Tick(pollEvery, statusPollMsg{})
}

func (m *Model) setStatus(st ipc.Status) tea.Cmd {
	m.status, m.haveStatus = st, true
	if st.Account == "" {
		return m.setNoAccount()
	}
	var cmds []tea.Cmd
	if !m.serverAsk && m.opts.Server != nil {
		m.serverAsk = true
		get := m.opts.Server
		cmds = append(cmds, func() tea.Msg { return serverMsg{serverName(get())} })
	}
	if m.noAccount {
		m.noAccount = false
		cmds = append(cmds, m.requestChats(), m.requestThread())
	}
	return tea.Batch(cmds...)
}

// serverName is the host of a "host:port" address, for display.
func serverName(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}
	return sanitize.Line(addr)
}
