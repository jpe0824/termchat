package tui

import (
	"cmp"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/sanitize"
)

// newRows are the new conversation overlay's rows for its step.
func (m *Model) newRows() []ovRow {
	ov := m.ov
	switch ov.step {
	case stepDM:
		return m.pickPeople(nil)
	case stepRoom:
		vis := "private room: by invitation — tab to change"
		if ov.open {
			vis = "open room: anyone can join — tab to change"
		}
		return []ovRow{{id: "vis", text: vis}}
	case stepJoin:
		var rows []ovRow
		for _, c := range m.openRooms {
			n := strconv.Itoa(len(c.Members)) + " members"
			if len(c.Members) == 1 {
				n = "1 member"
			}
			rows = append(rows, ovRow{id: "j:" + c.ID, text: m.convName(c) + "  " + n})
		}
		return rows
	}
	return []ovRow{{id: "dm", text: "direct message"}, {id: "room", text: "new room"},
		{id: "join", text: "join an open room"}}
}

func (m *Model) newEmpty() string {
	switch m.ov.step {
	case stepDM:
		if m.query() == "" {
			return "type a handle"
		}
		return "no matches"
	case stepJoin:
		return "no open rooms to join"
	}
	return ""
}

// newGo acts in the new conversation overlay: picks a choice, opens the DM,
// creates the room, or joins the selected one.
func (m *Model) newGo() tea.Cmd {
	ov := m.ov
	switch ov.step {
	case stepChoose:
		step := map[string]ovStep{"dm": stepDM, "room": stepRoom, "join": stepJoin}[m.ovSel().id]
		m.ovStepTo(step, "")
		ov.open = false
		if step == stepJoin {
			return m.requestChats()
		}
		return m.requestPeople()
	case stepDM:
		h, why := m.pickedHandle()
		if h == "" {
			ov.err = why
			return nil
		}
		return m.openDM(h)
	case stepRoom:
		name, why := m.roomNameIn()
		if name == "" {
			ov.err = why
			return nil
		}
		op := ipc.RoomOp{Kind: ipc.RoomCreate, Name: name, Open: ov.open}
		return m.ovCall(ipc.RoomCreate, "", func(b Backend) (string, error) {
			c, err := b.Room(op)
			return c.ID, err
		})
	case stepJoin:
		conv, ok := strings.CutPrefix(m.ovSel().id, "j:")
		if !ok {
			return nil
		}
		op := ipc.RoomOp{Kind: ipc.RoomJoin, Conv: conv}
		return m.ovCall(ipc.RoomJoin, "", func(b Backend) (string, error) {
			c, err := b.Room(op)
			return cmp.Or(c.ID, conv), err
		})
	}
	return nil
}

// roomNameIn is the room name typed in the field, normalised, or "" with
// why it cannot be used.
func (m *Model) roomNameIn() (string, string) {
	name := model.NormalizeName(m.ov.field.text)
	switch {
	case name == "":
		return "", "type a name"
	case !model.ValidName(name):
		return "", nameInvalid
	}
	return name, ""
}

const nameInvalid = "invalid name: " + model.NameRule

// nameErr is what is wrong with a room name being typed, as it is typed.
func (m *Model) nameErr() string {
	ov := m.ov
	naming := ov.kind == ovNew && ov.step == stepRoom || ov.kind == ovRoom && ov.step == stepRename
	if !naming || strings.TrimSpace(ov.field.text) == "" {
		return ""
	}
	if _, why := m.roomNameIn(); why != "" {
		return sanitize.Line(why)
	}
	return ""
}
