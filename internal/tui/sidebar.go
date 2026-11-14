package tui

import (
	"cmp"
	"slices"
	"strconv"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
)

// sortConvs orders the chat list: direct messages, then rooms, each by last
// activity, newest first.
func sortConvs(in []ipc.ConvView) []ipc.ConvView {
	out := slices.Clone(in)
	slices.SortStableFunc(out, func(a, b ipc.ConvView) int {
		if a.Kind != b.Kind {
			if a.Kind == model.ConvDM {
				return -1
			}
			return 1
		}
		if c := b.LastAt.Compare(a.LastAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return out
}

// mostRecent is the index of the most recently active conversation.
func mostRecent(convs []ipc.ConvView) int {
	best := 0
	for i, c := range convs {
		if c.LastAt.After(convs[best].LastAt) {
			best = i
		}
	}
	return best
}

// sbRow is one row of the chat list: a section heading (conv -1) or a
// conversation.
type sbRow struct {
	heading string
	conv    int
}

func (m *Model) sidebarRows() []sbRow {
	var rows []sbRow
	section := model.ConvKind("")
	for i, c := range m.convs {
		if c.Kind != section {
			section = c.Kind
			h := "Rooms"
			if c.Kind == model.ConvDM {
				h = "Direct"
			}
			rows = append(rows, sbRow{heading: h, conv: -1})
		}
		rows = append(rows, sbRow{conv: i})
	}
	return rows
}

// sidebarOffset is the first row shown, enough to keep the cursor in view.
func (m *Model) sidebarOffset(rows []sbRow, h int) int {
	for i, r := range rows {
		if r.conv == m.cursor {
			return max(i-h+1, 0)
		}
	}
	return 0
}

// listActive reports whether the arrow keys move in the chat list.
func (m *Model) listActive(g geom) bool {
	return g.side.w > 0 && (g.mode <= Narrow || m.focus == focusList)
}

func (m *Model) sidebarLines(g geom) []string {
	width := g.side.inner()
	if !m.haveList {
		return nil
	}
	if len(m.convs) == 0 {
		return []string{m.st.faint.Render("no chats yet")}
	}
	rows := m.sidebarRows()
	off := m.sidebarOffset(rows, g.bodyH)
	var out []string
	for _, r := range rows[off:] {
		if r.conv < 0 {
			out = append(out, m.st.heading.Render(r.heading))
			continue
		}
		out = append(out, m.sidebarRow(m.convs[r.conv], r.conv == m.cursor, m.listActive(g), width))
	}
	return out
}

func (m *Model) sidebarRow(c ipc.ConvView, cursor, active bool, width int) string {
	dot, count := "  ", ""
	if c.Unread > 0 {
		dot, count = "● ", strconv.Itoa(c.Unread)
	}
	nameW := width - 2
	if count != "" {
		nameW -= len(count) + 1
	}
	name := truncate(m.convName(c.Conversation), nameW)
	plain := spread(dot+name, count, width, " ")
	switch {
	case cursor && active:
		return m.st.selected.Render(plain)
	case cursor || c.ID == m.openID:
		return m.st.open.Render(plain)
	case c.Unread > 0:
		return m.st.unread.Render("●") + plain[len("●"):]
	}
	return plain
}

// sidebarHit returns the conversation index at body row y, or -1.
func (m *Model) sidebarHit(g geom, y int) int {
	rows := m.sidebarRows()
	i := y + m.sidebarOffset(rows, g.bodyH)
	if i < 0 || i >= len(rows) {
		return -1
	}
	return rows[i].conv
}

// move steps the chat list cursor.
func (m *Model) move(d int) {
	if len(m.convs) > 0 {
		m.cursor = min(max(m.cursor+d, 0), len(m.convs)-1)
	}
}
