package tui

import (
	"slices"
	"strconv"

	"github.com/jpe0824/termchat/internal/sanitize"
)

// membersLines is the wide layout's panel for the open room: its members,
// whether it is open, and who created it. A member is tagged [agent] when
// the people list says so or one of their loaded messages is.
func (m *Model) membersLines(g geom) []string {
	c, ok := m.openConv()
	if !ok {
		return nil
	}
	members := slices.Clone(c.Members)
	slices.Sort(members)
	var names []string
	for _, h := range members {
		agent := m.isAgent(h) || m.thread != nil && m.thread.agents[h]
		names = append(names, tagged(sanitize.Line(h), h == m.me(), agent))
	}
	kind := "Private room"
	if c.Open {
		kind = "Open room"
	}
	tail := []string{"", kind}
	if c.Creator != "" {
		tail = append(tail, "created by "+sanitize.Line(c.Creator))
	}
	room := g.bodyH - 1 - len(tail)
	if len(names) > room {
		keep := max(room-1, 0)
		names = append(names[:keep], "+"+strconv.Itoa(len(names)-keep)+" more")
	}
	out := []string{m.st.heading.Render("Members (" + strconv.Itoa(len(c.Members)) + ")")}
	out = append(out, names...)
	return append(out, tail...)
}
