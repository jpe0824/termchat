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
	// The main view's actions, for the chat list and the thread.
	actHelp
	actJump
	actNew
	actPanel
	actSearch
	actMove
	actEnter
	actTab
	actEsc
	actPage
	actR
	actEdit
	actDelete
	actDiscard
	actQuit
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

// mainBinding is the chat-list and thread binding key s triggers. The
// first three groups of keyTable are the main view's; a key in none of them
// is no command there.
func mainBinding(s string) (binding, bool) {
	for _, g := range keyTable[:3] {
		for _, b := range g.binds {
			if slices.Contains(b.match, s) {
				return b, true
			}
		}
	}
	return binding{}, false
}

// guarded are the command keys of the chat list and the thread that change
// something; they act only as a lone press. Movement keys stay immediate so
// held-down auto-repeat works.
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
		out = append([]string{"⏎ " + verb + " " + sanitize.Line(h)}, out...)
	}
	return out
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
