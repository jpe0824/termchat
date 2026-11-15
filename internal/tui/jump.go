package tui

import (
	"cmp"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/sanitize"
)

// jumpRows are the user's conversations, most recently active first, then
// the people they have no DM with, filtered by the field: a substring of
// a room's name, or of a person's handle or display name, in any case.
func (m *Model) jumpRows() []ovRow {
	q := m.query()
	convs := slices.Clone(m.convs)
	slices.SortStableFunc(convs, func(a, b ipc.ConvView) int {
		return cmp.Or(b.LastAt.Compare(a.LastAt), cmp.Compare(a.ID, b.ID))
	})
	var rows []ovRow
	peers := map[string]bool{}
	for _, c := range convs {
		if c.Kind == model.ConvRoom {
			if strings.Contains(strings.ToLower(sanitize.Line(c.Name)), q) {
				rows = append(rows, ovRow{id: "c:" + c.ID, text: m.convName(c.Conversation)})
			}
			continue
		}
		peer := c.Peer(m.me())
		peers[peer] = true
		if m.personMatches(peer, q) {
			rows = append(rows, ovRow{id: "c:" + c.ID, text: m.personLabel(peer)})
		}
	}
	for _, p := range m.people {
		if p.Handle != m.me() && !peers[p.Handle] && m.personMatches(p.Handle, q) {
			rows = append(rows, ovRow{id: "p:" + p.Handle, text: m.personLabel(p.Handle) + " — new chat"})
		}
	}
	return rows
}

// jumpGo opens the selected conversation, or the DM with the selected
// person.
func (m *Model) jumpGo() tea.Cmd {
	id := m.ovSel().id
	if conv, ok := strings.CutPrefix(id, "c:"); ok {
		m.closeOverlay()
		return m.open(conv)
	}
	if h, ok := strings.CutPrefix(id, "p:"); ok {
		return m.openDM(h)
	}
	return nil
}

// ovTitle is the overlay's heading.
func (m *Model) ovTitle() string { return m.ov.spec().title(m) }

func (m *Model) newTitle() string {
	return [...]string{stepChoose: "New conversation", stepDM: "Direct message", stepRoom: "New room",
		stepJoin: "Join an open room"}[m.ov.step]
}

func (m *Model) roomTitle() string {
	name := m.roomName()
	switch m.ov.step {
	case stepInvite:
		return "Invite to " + name
	case stepRename:
		return "Rename " + name
	}
	return name
}
