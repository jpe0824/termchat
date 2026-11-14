package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/notify"
)

// onNotify writes the notification the background process asked this
// interface to show as the terminal's escape sequence and a bell. It goes
// to the terminal outside the view, so the screen is left as it is.
func (m *Model) onNotify(p *ipc.NotifyPayload) tea.Cmd {
	if p == nil || m.osc == "" {
		return nil
	}
	seq := notify.OSC(m.osc, *p)
	if seq == "" {
		return nil
	}
	return tea.Raw(seq + "\a")
}
