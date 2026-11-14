package tui

import (
	"errors"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jpe0824/termchat/internal/ipc"
)

// state is the connection state as a glyph, its words and its style:
// ● all is well, ○ waiting, ! needs the user.
func (m *Model) state() (glyph, words string, st lipgloss.Style) {
	switch {
	case m.procDown && m.startErr != "":
		return "!", "could not start the background process: " + m.startErr, m.st.bad
	case m.procDown && errors.Is(m.downErr, ipc.ErrVersionMismatch):
		return "!", "background process is a different version — run: termchat daemon stop", m.st.bad
	case m.procDown:
		return "!", "background process not running", m.st.bad
	case m.launching || !m.haveStatus:
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
		if m.server != "" {
			options = append(options, m.server+" "+full)
		}
		options = append(options, full, glyph+" "+words)
		// The words cut to the room left, before falling back to the glyph.
		room := m.w - lipgloss.Width(left) - lipgloss.Width(glyph) - 6
		if room > 1 {
			options = append(options, glyph+" "+truncate(words, room))
		}
	}
	options = append(options, glyph, "")
	for _, o := range options {
		right := " ┐"
		if o != "" {
			right = " " + o + " ┐"
		}
		if lipgloss.Width(left)+lipgloss.Width(right)+1 <= m.w {
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
	keys := m.footerKeys(g)
	if m.procDown && !m.help && m.canStart() {
		keys = append([]string{"r start"}, keys...)
	}
	for n := len(keys); n > 0; n-- {
		left := "└─ " + strings.Join(keys[:n], "  ") + " "
		switch lw := lipgloss.Width(left); {
		case lw+1 == m.w:
			// spread keeps a cell of fill, which an exact fit has no room for.
			return left + "┘"
		case lw+1 < m.w:
			return spread(left, "┘", m.w, "─")
		}
	}
	return "└" + strings.Repeat("─", max(m.w-2, 0)) + "┘"
}

// footerKeys are the keys for where focus is, most useful first.
func (m *Model) footerKeys(g geom) []string {
	wide := g.mode >= Standard
	tail := []string{"? help", "q quit"}
	if wide {
		tail = append([]string{"tab pane"}, tail...)
	}
	sel, hasSel := m.selected()
	switch {
	case m.help:
		return []string{"? close", "q quit"}
	case m.listActive(g) && wide:
		return []string{"↑↓ move", "⏎ open", "tab pane", "pgup/pgdn scroll", "? help", "q quit"}
	case m.listShown() && g.mode == Tiny:
		return []string{"⏎ open", "? help"}
	case m.listShown():
		return []string{"↑↓ move", "⏎ open", "? help", "q quit"}
	case m.inputActive(g):
		if c := m.drafts[m.openID]; c != nil && c.edit != nil {
			return []string{"⏎ save", "esc cancel", "alt+⏎ newline"}
		}
		if wide {
			return []string{"⏎ send", "alt+⏎ newline", "↑ select", "esc leave", "ctrl+c quit"}
		}
		return []string{"⏎ send", "esc chats", "alt+⏎ newline", "ctrl+c quit"}
	case hasSel:
		var keys []string
		switch {
		case sel.m.Status == ipc.MsgFailed:
			keys = []string{"r retry", "x discard"}
		case m.delivered(sel.m):
			keys = []string{"e edit", "d delete"}
		}
		return append(append(keys, "i type", "↑↓ select", "esc back"), tail...)
	case g.mode == Tiny:
		return []string{"esc chats", "i type", "? help"}
	case wide:
		return []string{"i type", "↑↓ scroll", "tab pane", "pgup/pgdn scroll", "? help", "q quit"}
	}
	return []string{"esc chats", "i type", "? help", "pgup/pgdn scroll", "q quit"}
}
