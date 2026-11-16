package tui

import (
	"slices"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
)

// hintRow is the thread pane's line of message key hints, blank when the
// keyboard is elsewhere.
func (m *Model) hintRow(g geom, l tlayout) string {
	msg, _ := m.hintKeys(g)
	return m.st.faint.Render(fitKeys(msg, l.width))
}

// fitKeys joins as many leading keys as fit in w cells.
func fitKeys(keys []string, w int) string {
	for n := len(keys); n > 0; n-- {
		if s := strings.Join(keys[:n], "  "); lipgloss.Width(s) <= w {
			return s
		}
	}
	return ""
}

// hintKeys are the keys for where focus is, most useful first: msg for the
// messages, drawn under the input, and app for the window, drawn on the
// bottom border. Keys that type in the input are not offered while it has
// focus.
func (m *Model) hintKeys(g geom) (msg, app []string) {
	wide := g.mode >= Standard
	msg, app = m.mainKeys(g, wide)
	if m.procDown {
		// Nothing reaches the input or changes a message while it is down.
		msg = slices.DeleteFunc(msg, func(k string) bool { return k == "i type" || k == "e edit" || k == "d delete" })
	}
	return msg, app
}

func (m *Model) mainKeys(g geom, wide bool) (msg, app []string) {
	sel, hasSel := m.selected()
	room := ""
	if c, ok := m.openConv(); ok && c.Kind == model.ConvRoom {
		room = "ctrl+o room"
	}
	tail := []string{"? help", "q quit"}
	if wide {
		tail = append([]string{"tab pane"}, tail...)
	}
	switch {
	case m.help:
		return nil, []string{"↑↓ scroll", "? close", "q quit"}
	case m.ov != nil && m.ovModeFor(g) == boxMode:
		return nil, nil
	case m.ov != nil:
		return nil, m.ovFooterKeys()
	case m.listActive(g) && wide:
		return nil, []string{"↑↓ move", "⏎ open", "tab pane", "/ search", "ctrl+k jump", "ctrl+n new", "? help", "q quit"}
	case m.listShown() && g.mode == Tiny:
		return nil, []string{"⏎ open", "? help"}
	case m.listShown():
		return nil, []string{"↑↓ move", "⏎ open", "/ search", "? help", "ctrl+k jump", "ctrl+n new", "q quit"}
	case len(m.shownMatches(g)) > 0:
		if wide {
			return cmdListFoot(), nonEmpty("ctrl+k jump", "ctrl+n new", room, "ctrl+c quit")
		}
		return cmdListFoot(), []string{"ctrl+c quit"}
	case m.inputActive(g):
		if c := m.drafts[m.openID]; c != nil && c.edit != nil {
			msg = []string{"⏎ save", "esc cancel", "alt+⏎ newline"}
		} else if wide {
			msg = []string{"⏎ send", "alt+⏎ newline", "↑ select", "esc leave"}
		} else {
			return []string{"⏎ send", "alt+⏎ newline"}, []string{"esc chats", "ctrl+c quit"}
		}
		return msg, nonEmpty("ctrl+k jump", "ctrl+n new", room, "ctrl+c quit")
	case hasSel:
		switch {
		case sel.m.Status == ipc.MsgFailed:
			msg = []string{"r retry", "x discard"}
		case m.delivered(sel.m):
			msg = []string{"e edit", "d delete"}
		}
		return append(msg, "i type", "c copy", "↑↓ select", "esc back"), tail
	case g.mode == Tiny:
		return []string{"i type"}, []string{"esc chats", "? help"}
	case wide:
		return []string{"i type", "↑↓ scroll"}, nonEmpty("tab pane", "/ search", "ctrl+k jump", room, "? help", "q quit")
	}
	return []string{"i type"}, nonEmpty("esc chats", "? help", "/ search", "ctrl+k jump", room, "q quit")
}

func nonEmpty(keys ...string) []string {
	return slices.DeleteFunc(keys, func(k string) bool { return k == "" })
}
