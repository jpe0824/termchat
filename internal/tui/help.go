package tui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jpe0824/termchat/internal/sanitize"
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

// binding is one line of the key map: shown by help, offered by footers,
// and the key handling itself.
type binding struct {
	keys, desc string
	kind       keyKind
	// match are the key names that trigger it, and run what they do; none
	// for a line that only describes.
	match []string
	run   func(*Model, keyCtx) tea.Cmd
	// foot is the footer label, "" for none; steps limits the binding to
	// some steps of its overlay; manage to people who can manage the room.
	foot   string
	steps  []ovStep
	manage bool
}

type keyGroup struct {
	name  string
	binds []binding
	// kind is the overlay the group drives, 0 for the main view; main is
	// the part of the main view it drives, 0 for none.
	kind ovKind
	main mainPart
}

// mainPart is where in the main view a group's keys work.
type mainPart int

const (
	mainAnywhere mainPart = iota + 1
	mainList
	mainThread
	// mainCmdList is the command list drawn above the input.
	mainCmdList
)

// mainBinding is the binding key s triggers in the main view: from the
// chat list's group when list is set, else the thread's, then from
// Anywhere. A key of the other group is held as it would be there and then
// does nothing, so a key the thread takes neither acts in the chat list
// nor reads as text there. A key in none of them is no command.
func mainBinding(list bool, s string) (binding, bool) {
	active, other := mainThread, mainList
	if list {
		active, other = mainList, mainThread
	}
	if b, ok := groupBinding(active, s); ok {
		return b, true
	}
	if b, ok := groupBinding(mainAnywhere, s); ok {
		return b, true
	}
	b, ok := groupBinding(other, s)
	b.run = func(*Model, keyCtx) tea.Cmd { return nil }
	return b, ok
}

func groupBinding(part mainPart, s string) (binding, bool) {
	for _, g := range keyTable {
		if g.main != part {
			continue
		}
		for _, b := range g.binds {
			if slices.Contains(b.match, s) {
				return b, true
			}
		}
	}
	return binding{}, false
}

// ovBinding is the binding key s triggers in the open overlay, if any.
func (m *Model) ovBinding(s string) (binding, bool) {
	for _, b := range m.ovBindings() {
		if slices.Contains(b.match, s) {
			return b, true
		}
	}
	return binding{}, false
}

// backKey reports whether b is the way back out of an overlay step, and
// moveKey whether it moves the selection.
func backKey(b binding) bool { return slices.Contains(b.match, "esc") }

func moveKey(b binding) bool { return slices.Contains(b.match, "up") }

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
			if backKey(b) && m.ov.step == m.ov.spec().first {
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
		case backKey(b):
			back = append(back, b.foot)
		case moveKey(b):
			move = append(move, b.foot)
		default:
			acts = append(acts, b.foot)
		}
	}
	return slices.Concat(m.pickerKeys(acts), back, move)
}

// pickerKeys names the person enter acts on in the direct message and
// invite pickers, and drops enter while there is none.
func (m *Model) pickerKeys(acts []string) []string {
	verb := map[ovStep]string{stepDM: "message", stepInvite: "invite"}[m.ov.step]
	if verb == "" || m.ov.ask != nil {
		return acts
	}
	out := slices.DeleteFunc(slices.Clone(acts), func(k string) bool { return strings.HasPrefix(k, "⏎ ") })
	if h, _ := m.pickedHandle(); h != "" {
		out = append([]string{"⏎ " + verb + " " + truncate(sanitize.Line(h), pickerHandleW)}, out...)
	}
	return out
}

// pickerHandleW caps the handle in a picker's footer label, so the label
// and the way out fit beside it in a narrow window.
const pickerHandleW = 12

// keyMsgFor is the key press a key name stands for, enough to tell
// whether it types.
func keyMsgFor(s string) tea.KeyPressMsg {
	if len([]rune(s)) == 1 {
		return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
	}
	return tea.KeyPressMsg{}
}

// helpLines is the key map, generated from keyTable, laid out width cells
// wide. The last layout is kept: the table never changes.
func (m *Model) helpLines(width int) []string {
	if h := m.helpLaid; h.lines != nil && h.width == width {
		return h.lines
	}
	m.helpLaid.width, m.helpLaid.lines = width, m.layHelp(width)
	return m.helpLaid.lines
}

func (m *Model) layHelp(width int) []string {
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
