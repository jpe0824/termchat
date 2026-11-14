package tui

import (
	"strconv"
	"strings"

	"github.com/jpe0824/termchat/internal/model"
)

// unreadElsewhere totals unread messages outside the open conversation.
func (m *Model) unreadElsewhere() int {
	n := 0
	for _, c := range m.convs {
		if c.ID != m.openID {
			n += c.Unread
		}
	}
	return n
}

// backMarker is the narrow and tiny header's way back to the chat list.
func (m *Model) backMarker(tiny bool) string {
	s := "‹ chats"
	if tiny {
		s = "‹"
	}
	if n := m.unreadElsewhere(); n > 0 {
		s += " ●" + strconv.Itoa(n)
	}
	return s
}

func (m *Model) threadHeader(g geom, width int) string {
	c, ok := m.openConv()
	name := ""
	if ok {
		name = m.convName(c.Conversation)
	}
	switch g.mode {
	case Tiny:
		return truncate(m.backMarker(true)+"  "+name, width)
	case Narrow:
		return truncate(m.backMarker(false)+"    "+name, width)
	case Standard:
		if ok && c.Kind == model.ConvRoom {
			n := len(c.Members)
			count := strconv.Itoa(n) + " members"
			if n == 1 {
				count = "1 member"
			}
			return spread(m.st.heading.Render(truncate(name, width-len(count)-1)), count, width, " ")
		}
	}
	return m.st.heading.Render(truncate(name, width))
}

// threadView draws the thread pane: header, messages, and the input line.
func (m *Model) threadView(g geom) []string {
	width, vh := m.threadPane(g)
	rule := m.st.faint.Render(strings.Repeat("─", width))
	out := []string{m.threadHeader(g, width)}
	if g.mode != Tiny {
		out = append(out, rule)
	}
	out = append(out, m.messageArea(width, vh, g.mode)...)
	return append(out, rule, m.st.faint.Render("> _"))
}

func (m *Model) messageArea(width, vh int, mode Mode) []string {
	t := m.thread
	area := make([]string, vh)
	note := func(s string) []string {
		if vh > 0 {
			area[0] = m.st.faint.Render(truncate(s, width))
		}
		return area
	}
	switch {
	case t == nil:
		return area
	case t.err != "":
		return note("could not load messages: " + t.err)
	case !t.loaded:
		return note("loading…")
	case len(t.stored)+len(t.outbox) == 0:
		return note("no messages yet")
	}
	lines := m.threadLines(width, mode)
	top := t.top(lines, vh)
	for i := range vh {
		if top+i < len(lines) {
			area[i] = lines[top+i].text
		}
	}
	if t.newBelow > 0 && top < max(len(lines)-vh, 0) && vh > 0 {
		area[vh-1] = spread("", m.st.unread.Render("↓ "+strconv.Itoa(t.newBelow)+" new"), width, " ")
	}
	return area
}
