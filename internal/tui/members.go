package tui

import (
	"slices"
	"strconv"

	"github.com/jpe0824/termchat/internal/sanitize"
)

// membersLines is the wide layout's panel for the open room: its members,
// whether it is open, and who created it. Membership carries no agent flag,
// so a member is tagged [agent] when one of their loaded messages is.
func (m *Model) membersLines() []string {
	c, ok := m.openConv()
	if !ok {
		return nil
	}
	agents := map[string]bool{}
	if m.status.Me.Agent {
		agents[m.me()] = true
	}
	if m.thread != nil {
		for _, msg := range m.thread.stored {
			if msg.Agent {
				agents[msg.Author] = true
			}
		}
	}
	members := slices.Clone(c.Members)
	slices.Sort(members)
	var names []string
	for _, h := range members {
		n := sanitize.Line(h)
		if h == m.me() {
			n += " (you)"
		}
		if agents[h] {
			n += " [agent]"
		}
		names = append(names, n)
	}
	kind := "Private room"
	if c.Open {
		kind = "Open room"
	}
	tail := []string{"", kind}
	if c.Creator != "" {
		tail = append(tail, "created by "+sanitize.Line(c.Creator))
	}
	room := m.geom().bodyH - 1 - len(tail)
	if len(names) > room {
		keep := max(room-1, 0)
		names = append(names[:keep], "+"+strconv.Itoa(len(names)-keep)+" more")
	}
	out := []string{m.st.heading.Render("Members (" + strconv.Itoa(len(c.Members)) + ")")}
	out = append(out, names...)
	return append(out, tail...)
}
