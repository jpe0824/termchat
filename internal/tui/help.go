package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// keyKind is how a key is told from pasted text. With the paste gap off,
// every key is immediate.
type keyKind int

const (
	// keyImmediate acts on arrival: movement only, and ?.
	keyImmediate keyKind = iota
	// keyGuarded acts only as a lone press: ignored within the paste gap of
	// the key before, cancelled by a key within the gap after, and bound to
	// what was selected when it was pressed.
	keyGuarded
	// keyDeferred waits out the gap and is cancelled by a key within it.
	keyDeferred
)

// action is what an overlay binding does.
type action int

const (
	actNone action = iota
	actUp
	actDown
	actPgUp
	actPgDown
	actFirst
	actLast
	actGo
	actBack
	actToggle
	actInvite
	actRemove
	actRename
	actLeave
)

// binding is one line of the key map: shown by help, offered by footers,
// and, in the overlays, the key handling itself.
type binding struct {
	keys, desc string
	kind       keyKind
	// match are the key names that trigger it; none for a line that only
	// describes.
	match []string
	act   action
	// foot is the footer label, "" for none; steps limits the binding to
	// some steps of its overlay; manage to people who can manage the room.
	foot   string
	steps  []ovStep
	manage bool
}

type keyGroup struct {
	name  string
	binds []binding
	// kind is the overlay the group drives, 0 for the main view.
	kind ovKind
}

var (
	moveKeys = []binding{
		{keys: "↑ ↓", desc: "move", match: []string{"up", "down"}, foot: "↑↓ move"},
		{keys: "pgup pgdn", desc: "move a page", match: []string{"pgup", "pgdown"}},
	}
	listKeys = []binding{
		{keys: "k j home end", desc: "move, to the first, to the last (lists without a text field)",
			match: []string{"k", "j", "home", "end"}},
	}
	fieldKeys = binding{keys: "letters", desc: "type; ← → home end backspace ctrl+w ctrl+u edit; tab does nothing"}
)

// keyTable is every key the interface takes, by where the keyboard is.
var keyTable = []keyGroup{
	{name: "Anywhere", binds: []binding{
		{keys: "?", desc: "help; ? or esc closes it, ↑ ↓ pgup pgdn scroll it (outside a text field)"},
		{keys: "ctrl+k", desc: "jump to a conversation or a person", kind: keyGuarded, match: []string{"ctrl+k"}},
		{keys: "ctrl+n", desc: "new conversation: direct message, room, or join an open room", kind: keyGuarded,
			match: []string{"ctrl+n"}},
		{keys: "ctrl+o", desc: "room panel: members, invite, remove, rename, leave", kind: keyGuarded,
			match: []string{"ctrl+o"}},
		{keys: "ctrl+c", desc: "quit; asked twice when drafts are unsent", kind: keyDeferred},
		{keys: "mouse", desc: "click a chat, a message, the input or an overlay row; a second click on a row acts; click outside an overlay to close it; the wheel scrolls"},
	}},
	{name: "Chat list", binds: []binding{
		{keys: "↑ ↓ k j", desc: "move"},
		{keys: "enter", desc: "open the chat", kind: keyGuarded, match: []string{"enter"}},
		{keys: "tab", desc: "to the thread (standard and wide layouts)", kind: keyGuarded, match: []string{"tab"}},
		{keys: "/", desc: "search history", kind: keyGuarded, match: []string{"/"}},
		{keys: "pgup pgdn", desc: "scroll the thread; home end: oldest, newest"},
		{keys: "r", desc: "start a stopped background process", kind: keyGuarded, match: []string{"r"}},
		{keys: "q", desc: "quit", kind: keyGuarded, match: []string{"q"}},
	}},
	{name: "Thread", binds: []binding{
		{keys: "↑ ↓ k j", desc: "step through your messages, or scroll"},
		{keys: "i enter", desc: "go to the input — letters type only in the input", kind: keyGuarded,
			match: []string{"i", "enter"}},
		{keys: "e d", desc: "edit or delete your selected message", kind: keyGuarded, match: []string{"e", "d"}},
		{keys: "r x", desc: "retry or discard a failed message", kind: keyGuarded, match: []string{"r", "x"}},
		{keys: "esc", desc: "from a selection to the input, else to the chat list"},
		{keys: "tab", desc: "to the chat list", kind: keyGuarded, match: []string{"tab"}},
		{keys: "/", desc: "search history", kind: keyGuarded, match: []string{"/"}},
		{keys: "pgup pgdn", desc: "scroll; home end: oldest, newest message"},
		{keys: "q", desc: "quit", kind: keyGuarded, match: []string{"q"}},
	}},
	{name: "Input", binds: []binding{
		{keys: "letters", desc: "type; ← → home end ctrl+a ctrl+e backspace delete ctrl+w ctrl+u edit"},
		{keys: "enter", desc: "send, or save an edit", kind: keyDeferred},
		{keys: "alt+enter", desc: "new line (also ctrl+j)"},
		{keys: "esc", desc: "cancel an edit, else leave the input", kind: keyDeferred},
		{keys: "↑", desc: "in an empty input: select your newest message", kind: keyDeferred},
		{keys: "tab", desc: "types a tab"},
		{keys: "pgup pgdn", desc: "scroll the thread"},
	}},
	{name: "Questions (y/n)", binds: []binding{
		{keys: "y", desc: "yes, as a lone key press", kind: keyGuarded},
		{keys: "any other", desc: "no"},
	}},
	{name: "Jump (ctrl+k)", kind: ovJump, binds: slices.Concat([]binding{fieldKeys}, moveKeys, []binding{
		{keys: "enter", desc: "open the selected conversation, or start a DM with the person",
			kind: keyGuarded, match: []string{"enter"}, act: actGo, foot: "⏎ open"},
		{keys: "esc", desc: "close", kind: keyDeferred, match: []string{"esc"}, act: actBack, foot: "esc close"},
	})},
	{name: "New conversation (ctrl+n)", kind: ovNew, binds: slices.Concat(moveKeys, listKeys, []binding{
		{keys: "enter", desc: "choose", kind: keyGuarded, match: []string{"enter"}, act: actGo,
			foot: "⏎ choose", steps: []ovStep{stepChoose}},
		{keys: "enter", desc: "direct message: open the DM with the selected person", kind: keyGuarded,
			match: []string{"enter"}, act: actGo, foot: "⏎ open", steps: []ovStep{stepDM}},
		{keys: "enter", desc: "new room: create it", kind: keyGuarded, match: []string{"enter"}, act: actGo,
			foot: "⏎ create", steps: []ovStep{stepRoom}},
		{keys: "tab", desc: "new room: open or private", kind: keyGuarded, match: []string{"tab"},
			act: actToggle, foot: "tab open/private", steps: []ovStep{stepRoom}},
		{keys: "enter", desc: "join the selected open room", kind: keyGuarded, match: []string{"enter"},
			act: actGo, foot: "⏎ join", steps: []ovStep{stepJoin}},
		{keys: "esc", desc: "back to the choices, or close", kind: keyDeferred, match: []string{"esc"},
			act: actBack, foot: "esc back"},
		fieldKeys,
	})},
	{name: "Search (/)", kind: ovSearch, binds: slices.Concat([]binding{fieldKeys}, moveKeys, []binding{
		{keys: "enter", desc: "open the conversation at that message", kind: keyGuarded,
			match: []string{"enter"}, act: actGo, foot: "⏎ open"},
		{keys: "esc", desc: "close", kind: keyDeferred, match: []string{"esc"}, act: actBack, foot: "esc close"},
	})},
	{name: "Room panel (ctrl+o)", kind: ovRoom, binds: slices.Concat(moveKeys, listKeys, []binding{
		{keys: "a", desc: "invite someone (creator and admins)", kind: keyGuarded, match: []string{"a"},
			act: actInvite, foot: "a invite", steps: []ovStep{stepMembers}, manage: true},
		{keys: "x", desc: "remove the selected member, after y/n (creator and admins)", kind: keyGuarded,
			match: []string{"x"}, act: actRemove, foot: "x remove", steps: []ovStep{stepMembers}, manage: true},
		{keys: "n", desc: "rename the room (creator and admins)", kind: keyGuarded, match: []string{"n"},
			act: actRename, foot: "n rename", steps: []ovStep{stepMembers}, manage: true},
		{keys: "l", desc: "leave the room, after y/n", kind: keyGuarded, match: []string{"l"}, act: actLeave,
			foot: "l leave", steps: []ovStep{stepMembers}},
		{keys: "enter", desc: "act on the selected row", kind: keyGuarded, match: []string{"enter"}, act: actGo,
			steps: []ovStep{stepMembers}},
		{keys: "enter", desc: "invite: invite the selected person", kind: keyGuarded, match: []string{"enter"},
			act: actGo, foot: "⏎ invite", steps: []ovStep{stepInvite}},
		{keys: "enter", desc: "rename: save the new name", kind: keyGuarded, match: []string{"enter"},
			act: actGo, foot: "⏎ rename", steps: []ovStep{stepRename}},
		{keys: "esc", desc: "back to the members, or close", kind: keyDeferred, match: []string{"esc"},
			act: actBack, foot: "esc back"},
		fieldKeys,
	})},
}

// guarded are the command keys of the chat list and the thread that change
// something; they act only as a lone press.
var guarded = func() map[string]bool {
	out := map[string]bool{}
	for _, g := range keyTable[:3] {
		for _, b := range g.binds {
			if b.kind == keyGuarded {
				for _, k := range b.match {
					out[k] = true
				}
			}
		}
	}
	return out
}()

// ovBinding is the binding key s triggers in the open overlay, if any.
func (m *Model) ovBinding(s string) (binding, bool) {
	for _, b := range m.ovBindings() {
		if slices.Contains(b.match, s) {
			if b.act == actNone {
				b.act = listAct(s)
			}
			return b, true
		}
	}
	return binding{}, false
}

// listAct is the movement a key of moveKeys or listKeys makes.
func listAct(s string) action {
	return map[string]action{"up": actUp, "k": actUp, "down": actDown, "j": actDown, "pgup": actPgUp,
		"pgdown": actPgDown, "home": actFirst, "end": actLast}[s]
}

// ovBindings are the bindings that apply in the overlay's current step.
func (m *Model) ovBindings() []binding {
	var out []binding
	for _, g := range keyTable {
		if g.kind != m.ov.kind {
			continue
		}
		for _, b := range g.binds {
			if len(b.steps) > 0 && !slices.Contains(b.steps, m.ov.step) || b.manage && !m.canManage() {
				continue
			}
			if b.act == actBack && m.ov.kind == ovNew && m.ov.step == stepChoose ||
				b.act == actBack && m.ov.kind == ovRoom && m.ov.step == stepMembers {
				b.foot = "esc close"
			}
			out = append(out, b)
		}
	}
	return out
}

// ovFooterKeys are the footer labels of the overlay's current step.
func (m *Model) ovFooterKeys() []string {
	if m.ov.ask != nil {
		return []string{"y yes", "any other key no"}
	}
	// What the step does first, then the way out, then moving, so the
	// narrowest footers keep the first two.
	var acts, back, move []string
	for _, b := range m.ovBindings() {
		switch {
		case b.foot == "" || m.ov.hasField() && len(b.match) > 0 && keyText(keyMsgFor(b.match[0])) != "":
		case b.act == actBack:
			back = append(back, b.foot)
		case b.act == actNone:
			move = append(move, b.foot)
		default:
			acts = append(acts, b.foot)
		}
	}
	return slices.Concat(acts, back, move)
}

// keyMsgFor is the key press a key name stands for, enough to tell
// whether it types.
func keyMsgFor(s string) tea.KeyPressMsg {
	if len([]rune(s)) == 1 {
		return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
	}
	return tea.KeyPressMsg{}
}

// helpLines is the key map, generated from keyTable.
func (m *Model) helpLines(width int) []string {
	out := []string{m.st.heading.Render("Keys") + m.st.faint.Render("  ↑↓ scroll  ? close"), ""}
	keyW := 0
	for _, g := range keyTable {
		for _, b := range g.binds {
			keyW = max(keyW, lipgloss.Width(b.keys))
		}
	}
	keyW = min(keyW, max(width/3, 1))
	for _, g := range keyTable {
		out = append(out, m.st.heading.Render(g.name))
		for _, b := range g.binds {
			for i, l := range wrap(b.desc, max(width-keyW-2, 1)) {
				lead := strings.Repeat(" ", keyW)
				if i == 0 {
					lead = fit(b.keys, keyW)
				}
				out = append(out, lead+"  "+l)
			}
		}
		out = append(out, "")
	}
	return out
}

// helpKey takes a key while the help is shown: ? and esc close it, the
// movement keys scroll it.
func (m *Model) helpKey(s string) {
	g := m.geom()
	page := max(g.bodyH-1, 1)
	switch s {
	case "?", "esc":
		m.help, m.helpTop = false, 0
	case "up", "k":
		m.helpTop--
	case "down", "j":
		m.helpTop++
	case "pgup":
		m.helpTop -= page
	case "pgdown":
		m.helpTop += page
	case "home":
		m.helpTop = 0
	case "end":
		m.helpTop = len(m.helpLines(m.w - 4))
	}
	m.clampHelp(g)
}

func (m *Model) clampHelp(g geom) {
	m.helpTop = max(min(m.helpTop, len(m.helpLines(m.w-4))-g.bodyH), 0)
}

// helpView is the part of the help that fits the body.
func (m *Model) helpView(g geom) []string {
	m.clampHelp(g)
	return m.helpLines(m.w - 4)[m.helpTop:]
}
