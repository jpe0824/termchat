package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/notify"
)

// herdrDoneAfter separates the working and done reports to Herdr, so it sees
// two status changes. Herdr's app reads statuses from its core as they change;
// a second is well beyond that, and short enough to feel immediate.
const herdrDoneAfter = time.Second

// herdrDoneMsg asks for the done report that follows a working one.
type herdrDoneMsg struct{}

// herdrDone schedules the done report after a notification under Herdr.
func (m *Model) herdrDone() tea.Cmd {
	if m.osc != "7501" {
		return nil
	}
	return m.opts.Tick(herdrDoneAfter, herdrDoneMsg{})
}

// notification is what the terminal is sent for a notification the
// background process asked this interface to show: its escape sequence and
// a bell, "" for none. It goes to the terminal outside the view, so the
// screen is left as it is.
func (m *Model) notification(p *ipc.NotifyPayload) string {
	if p == nil || m.osc == "" {
		return ""
	}
	seq := notify.OSC(m.osc, *p)
	if seq == "" || m.osc == "7501" {
		// Herdr plays its own sound for a status report.
		return seq
	}
	return seq + "\a"
}
