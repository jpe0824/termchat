package tui

import (
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/notify"
)

// notification is what the terminal is sent for a notification the
// background process asked this interface to show: its escape sequence and
// a bell, "" for none. It goes to the terminal outside the view, so the
// screen is left as it is.
func (m *Model) notification(p *ipc.NotifyPayload) string {
	if p == nil || m.osc == "" {
		return ""
	}
	seq := notify.OSC(m.osc, *p)
	if seq == "" {
		return ""
	}
	return seq + "\a"
}
