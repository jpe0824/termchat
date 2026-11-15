package tui

import (
	"errors"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
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
	if m.procDown && !m.help && m.ov == nil && m.canStart() {
		keys = append([]string{"r start"}, keys...)
	}
	return borderKeys(keys, m.w)
}

// footerKeys are the keys for where focus is, most useful first.
func (m *Model) footerKeys(g geom) []string {
	wide := g.mode >= Standard
	tail := []string{"? help", "q quit"}
	if wide {
		tail = append([]string{"tab pane"}, tail...)
	}
	keys := m.mainKeys(g, wide, tail)
	if m.procDown {
		// Nothing reaches the input or changes a message while it is down.
		keys = slices.DeleteFunc(keys, func(k string) bool { return k == "i type" || k == "e edit" || k == "d delete" })
	}
	return keys
}

func (m *Model) mainKeys(g geom, wide bool, tail []string) []string {
	sel, hasSel := m.selected()
	room := ""
	if c, ok := m.openConv(); ok && c.Kind == model.ConvRoom {
		room = "ctrl+o room"
	}
	switch {
	case m.help:
		return []string{"↑↓ scroll", "? close", "q quit"}
	case m.ov != nil && m.ovModeFor(g) == boxMode:
		return nil
	case m.ov != nil:
		return m.ovFooterKeys()
	case m.listActive(g) && wide:
		return []string{"↑↓ move", "⏎ open", "tab pane", "/ search", "ctrl+k jump", "ctrl+n new", "? help", "q quit"}
	case m.listShown() && g.mode == Tiny:
		return []string{"⏎ open", "? help"}
	case m.listShown():
		return []string{"↑↓ move", "⏎ open", "/ search", "? help", "ctrl+k jump", "ctrl+n new", "q quit"}
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
		return append(append(keys, "i type", "c copy", "↑↓ select", "esc back"), tail...)
	case g.mode == Tiny:
		return []string{"esc chats", "i type", "? help"}
	case wide:
		return nonEmpty("i type", "↑↓ scroll", "tab pane", "/ search", "ctrl+k jump", room, "? help", "q quit")
	}
	return nonEmpty("esc chats", "i type", "? help", "/ search", "ctrl+k jump", room, "q quit")
}

func nonEmpty(keys ...string) []string {
	return slices.DeleteFunc(keys, func(k string) bool { return k == "" })
}
