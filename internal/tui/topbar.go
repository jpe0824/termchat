package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jpe0824/termchat/internal/ipc"
)

// state is the connection state as a glyph, its words and its style:
// ● all is well, ○ waiting, ! needs the user.
func (m *Model) state() (glyph, words string, st lipgloss.Style) {
	switch {
	case m.procDown:
		return "!", "background process not running", m.st.bad
	case !m.haveStatus:
		return "○", "connecting", m.st.warn
	}
	switch m.status.Conn {
	case ipc.StateConnected:
		return "●", "connected", m.st.good
	case ipc.StateOffline:
		return "○", "offline", m.st.warn
	case ipc.StateNotAuthorised:
		return "!", "not authorised", m.st.bad
	case ipc.StateHostKeyChanged:
		return "!", "server key changed", m.st.bad
	case ipc.StateVersionMismatch:
		return "!", "server version not supported", m.st.bad
	}
	return "○", "connecting", m.st.warn
}

// topBar is the window's top border: the name at the left, the server and
// connection state at the right, cut down to what fits.
func (m *Model) topBar(g geom) string {
	glyph, words, st := m.state()
	left := "┌ termchat "
	var options []string
	if g.mode != Tiny {
		full := glyph + " " + words
		if m.status.Syncing && !m.procDown {
			full = "syncing " + full
		}
		if m.server != "" && !m.procDown {
			options = append(options, m.server+" "+full)
		}
		options = append(options, full, glyph+" "+words)
	}
	options = append(options, glyph, "")
	for _, o := range options {
		right := " ┐"
		if o != "" {
			right = " " + o + " ┐"
		}
		if lipgloss.Width(left)+lipgloss.Width(right) <= m.w {
			if o != "" {
				right = " " + strings.Replace(o, glyph, st.Render(glyph), 1) + " ┐"
			}
			return spread(left, right, m.w, "─")
		}
	}
	return "┌" + strings.Repeat("─", max(m.w-2, 0)) + "┐"
}

// footer is the bottom border with the keys that work here.
func (m *Model) footer(g geom) string {
	var keys []string
	switch {
	case m.help:
		keys = []string{"? close", "q quit"}
	case g.mode >= Standard:
		keys = []string{"↑↓ move", "⏎ open", "tab pane", "pgup/pgdn scroll", "? help", "q quit"}
	case m.listShown() && g.mode == Tiny:
		keys = []string{"⏎ open", "? help"}
	case m.listShown():
		keys = []string{"↑↓ move", "⏎ open", "? help", "q quit"}
	case g.mode == Tiny:
		keys = []string{"esc chats", "? help"}
	default:
		keys = []string{"esc chats", "pgup/pgdn scroll", "? help", "q quit"}
	}
	for n := len(keys); n > 0; n-- {
		left := "└─ " + strings.Join(keys[:n], "  ") + " "
		if lipgloss.Width(left)+1 <= m.w {
			return spread(left, "┘", m.w, "─")
		}
	}
	return "└" + strings.Repeat("─", max(m.w-2, 0)) + "┘"
}
