package tui

import (
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/sanitize"
)

// roomStartPanel opens the room panel on the open conversation, which
// must be a room.
func (m *Model) roomStartPanel(ov *overlay) string {
	c, ok := m.openConv()
	if !ok || c.Kind != model.ConvRoom {
		return "not a room"
	}
	ov.conv = c.ID
	return ""
}

// roomConv is the room the panel is about, while the user is in it.
func (m *Model) roomConv() (model.Conversation, bool) {
	if i := m.indexOf(m.ov.conv); i >= 0 && m.convs[i].Kind == model.ConvRoom {
		return m.convs[i].Conversation, true
	}
	return model.Conversation{}, false
}

func (m *Model) roomName() string {
	if c, ok := m.roomConv(); ok {
		return m.convName(c)
	}
	return "room"
}

// canManage reports whether the user may invite, remove and rename in the
// panel's room: its creator while a member, and admins.
func (m *Model) canManage() bool {
	c, ok := m.roomConv()
	if !ok {
		return false
	}
	me := m.me()
	return m.status.Me.Admin || c.Creator != "" && c.Creator == me && slices.Contains(c.Members, me)
}

// memberLabel is a member as the panels list them.
func (m *Model) memberLabel(h string) string {
	return tagged(sanitize.Line(h), h == m.me(), m.isAgent(h))
}

// roomRows are the members, then what the user may do: invite and rename
// for those who manage the room, leave for anyone. In the invite step they
// are the people who could be invited.
func (m *Model) roomRows() []ovRow {
	c, ok := m.roomConv()
	switch {
	case !ok:
		return nil
	case m.ov.step == stepInvite:
		return m.pickPeople(c.Members)
	case m.ov.step == stepRename:
		return nil
	}
	members := slices.Clone(c.Members)
	slices.Sort(members)
	var rows []ovRow
	for _, h := range members {
		rows = append(rows, ovRow{id: "m:" + h, text: m.memberLabel(h)})
	}
	if m.canManage() {
		rows = append(rows, ovRow{id: "a:invite", text: "invite someone"},
			ovRow{id: "a:rename", text: "rename the room"})
	}
	return append(rows, ovRow{id: "a:leave", text: "leave the room"})
}

// roomBody is the panel's lines around its rows.
func (m *Model) roomBody(w int) (head []string, empty string, tail []string) {
	c, ok := m.roomConv()
	switch {
	case !ok:
		return nil, "no longer in this room", nil
	case m.ov.step == stepInvite:
		return nil, "no matches", nil
	case m.ov.step == stepRename:
		return nil, "enter renames the room", nil
	}
	head = []string{m.st.heading.Render("Members (" + strconv.Itoa(len(c.Members)) + ")")}
	kind := "Private room"
	if c.Open {
		kind = "Open room"
	}
	tail = []string{"", kind}
	if c.Creator != "" {
		tail = append(tail, truncate("created by "+sanitize.Line(c.Creator), w))
	}
	return head, "", tail
}

// roomGo acts on the selected row: a member is offered for removal, the
// other rows do what they say; in a form, enter invites or renames.
func (m *Model) roomGo() tea.Cmd {
	ov := m.ov
	if (ov.step == stepInvite || ov.step == stepRename) && !m.canManage() {
		ov.err = "you can no longer manage this room"
		return nil
	}
	switch ov.step {
	case stepInvite:
		h, why := m.pickedHandle()
		if h == "" {
			ov.err = why
			return nil
		}
		return m.roomOp(ipc.RoomOp{Kind: ipc.RoomInvite, Handle: h}, "invited "+h)
	case stepRename:
		name, why := m.roomNameIn()
		if name == "" {
			ov.err = why
			return nil
		}
		if c, _ := m.roomConv(); name == c.Name {
			m.ovBack()
			return nil
		}
		return m.roomOp(ipc.RoomOp{Kind: ipc.RoomRename, Name: name}, "renamed to #"+name)
	}
	switch id := m.ovSel().id; {
	case id == "a:invite":
		return m.roomInvite()
	case id == "a:rename":
		return m.roomRename()
	case id == "a:leave":
		return m.roomLeave()
	case strings.HasPrefix(id, "m:"):
		return m.roomRemove()
	}
	return nil
}

// roomStart is the panel's room when one of its actions may start: from
// the member list, and, unless anyone may, for those who manage the room.
func (m *Model) roomStart(anyone bool) (model.Conversation, bool) {
	if m.ov.step != stepMembers || !anyone && !m.canManage() {
		return model.Conversation{}, false
	}
	return m.roomConv()
}

func (m *Model) roomInvite() tea.Cmd {
	if _, ok := m.roomStart(false); !ok {
		return nil
	}
	m.ovStepTo(stepInvite, "")
	return m.requestPeople()
}

func (m *Model) roomRename() tea.Cmd {
	if c, ok := m.roomStart(false); ok {
		m.ovStepTo(stepRename, c.Name)
	}
	return nil
}

func (m *Model) roomRemove() tea.Cmd {
	c, ok := m.roomStart(false)
	if !ok {
		return nil
	}
	h, ok := strings.CutPrefix(m.ovSel().id, "m:")
	if !ok || h == m.me() {
		return nil
	}
	m.ovAsk("remove "+sanitize.Line(h)+" from "+m.convName(c)+"? y/n", func() bool {
		c, ok := m.roomConv()
		return ok && slices.Contains(c.Members, h) && m.canManage()
	}, func() tea.Cmd {
		return m.roomOp(ipc.RoomOp{Kind: ipc.RoomRemove, Handle: h}, "removed "+h)
	})
	return nil
}

func (m *Model) roomLeave() tea.Cmd {
	c, ok := m.roomStart(true)
	if !ok {
		return nil
	}
	m.ovAsk("leave "+m.convName(c)+"? y/n", func() bool {
		_, ok := m.roomConv()
		return ok
	}, func() tea.Cmd { return m.roomOp(ipc.RoomOp{Kind: ipc.RoomLeave}, "") })
	return nil
}

// roomOp runs op on the panel's room.
func (m *Model) roomOp(op ipc.RoomOp, done string) tea.Cmd {
	op.Conv = m.ov.conv
	return m.ovCall(op.Kind, done, func(b Backend) (string, error) {
		_, err := b.Room(op)
		return op.Conv, err
	})
}
