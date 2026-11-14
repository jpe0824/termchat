package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
)

// ovKind is an overlay: one is open at a time, and while open it takes
// every key; the views underneath keep updating.
type ovKind int

const (
	ovJump ovKind = iota + 1
	ovNew
	ovSearch
	ovRoom
)

// ovStep is where an overlay is: the new conversation overlay's choices
// and their three forms, and the room panel's member list and its forms.
// Jump and search have one step, stepChoose.
type ovStep int

const (
	stepChoose ovStep = iota
	stepDM
	stepRoom
	stepJoin
	stepMembers
	stepInvite
	stepRename
)

// downBanner is what an overlay key says while it cannot work.
const downBanner = "background process not running — r to start"

type overlay struct {
	kind ovKind
	step ovStep
	// gen tells results meant for this overlay from those for one closed
	// since.
	gen   int
	field input
	// sel is the selected row; top the first row shown; clicked the row a
	// click selected, which a second click acts on.
	sel, top int
	clicked  string
	err      string
	info     string
	busy     bool
	ask      *ovAsk
	// open is the new room's visibility; conv the room the panel is about.
	open bool
	conv string
	// seq numbers the search queries; results are shown only for the
	// newest, and searched says they came back.
	seq      int
	results  []ipc.Message
	searched bool
}

// ovAsk is a y/n question in an overlay; holds says the question still
// applies when the answer comes.
type ovAsk struct {
	text  string
	holds func() bool
	yes   func() tea.Cmd
}

// ovRow is one row of an overlay's list. hl is a part of text to mark,
// looked for from byte from on.
type ovRow struct {
	id, text, hl string
	from         int
}

func (o *overlay) hasField() bool {
	switch o.kind {
	case ovJump, ovSearch:
		return true
	case ovNew:
		return o.step == stepDM || o.step == stepRoom
	}
	return o.step == stepInvite || o.step == stepRename
}

// openOverlay opens kind over the current view. Every overlay needs the
// background process.
func (m *Model) openOverlay(kind ovKind) tea.Cmd {
	if m.b == nil || m.procDown {
		return m.setBanner(downBanner)
	}
	ov := &overlay{kind: kind}
	if kind == ovRoom {
		c, ok := m.openConv()
		if !ok || c.Kind != model.ConvRoom {
			return m.setBanner("not a room")
		}
		ov.conv, ov.step = c.ID, stepMembers
	}
	m.ovGen++
	ov.gen = m.ovGen
	m.ov = ov
	m.clearBanner()
	switch kind {
	case ovSearch:
		return nil
	case ovNew:
		return m.requestChats()
	}
	return m.requestPeople()
}

// closeOverlay closes it; focus is where it was before it opened.
func (m *Model) closeOverlay() { m.ov = nil }

// overlayKey handles a key while an overlay is open. Typing and moving act
// at once; a key that acts waits out the paste gap as a lone press.
func (m *Model) overlayKey(k tea.KeyPressMsg, recent bool) tea.Cmd {
	ov, s := m.ov, k.String()
	if ov.ask != nil {
		return m.ovAnswer(s, recent)
	}
	if ov.hasField() {
		if t := keyText(k); t != "" {
			return m.ovEdit(func(in *input) { in.insert(cleanField(t)) })
		}
		if editKey(nil, s) {
			return m.ovEdit(func(in *input) { editKey(in, s) })
		}
	}
	b, ok := m.ovBinding(s)
	if !ok {
		if s == "?" && !ov.hasField() {
			m.help = true
		}
		return nil
	}
	if b.kind == keyImmediate || m.opts.PasteGap < 0 {
		return m.ovDo(b.act)
	}
	if recent {
		return nil
	}
	m.tm.exitAct, m.tm.exitOn = b.act, m.target(m.geom())
	return m.deferExit(exitOverlay)
}

// editKey applies an editing key to in, or with in nil reports whether s
// is one.
func editKey(in *input, s string) bool {
	edits := map[string]func(*input){
		"left": (*input).left, "right": (*input).right, "home": (*input).home, "end": (*input).end,
		"ctrl+a": (*input).home, "ctrl+e": (*input).end, "backspace": (*input).backspace,
		"ctrl+h": (*input).backspace, "delete": (*input).del, "ctrl+w": (*input).killWord,
		"ctrl+u": (*input).killLine,
	}
	f, ok := edits[s]
	if ok && in != nil {
		f(in)
	}
	return ok
}

// cleanField is typed or pasted text as a one-line field takes it.
func cleanField(s string) string {
	return strings.NewReplacer("\n", "", "\t", " ").Replace(cleanInput(s))
}

// ovEdit changes the field's text and reacts to the change.
func (m *Model) ovEdit(f func(*input)) tea.Cmd {
	ov := m.ov
	before := ov.field.text
	f(&ov.field)
	if ov.field.text == before {
		return nil
	}
	ov.sel, ov.top, ov.err, ov.info, ov.clicked = 0, 0, "", "", ""
	if ov.kind == ovSearch {
		return m.searchChanged()
	}
	return nil
}

// ovDo runs an overlay action.
func (m *Model) ovDo(a action) tea.Cmd {
	ov := m.ov
	n := len(m.ovRows())
	page := max(m.ovListRows()-1, 1)
	switch a {
	case actUp:
		ov.sel--
	case actDown:
		ov.sel++
	case actPgUp:
		ov.sel -= page
	case actPgDown:
		ov.sel += page
	case actFirst:
		ov.sel = 0
	case actLast:
		ov.sel = n - 1
	case actGo:
		return m.ovGo()
	case actBack:
		m.ovBack()
	case actToggle:
		ov.open = !ov.open
	case actInvite, actRemove, actRename, actLeave:
		return m.roomAction(a)
	}
	ov.sel = max(min(ov.sel, n-1), 0)
	return nil
}

// ovGo acts on the selected row, or on the field.
func (m *Model) ovGo() tea.Cmd {
	ov := m.ov
	if ov.busy {
		return nil
	}
	switch ov.kind {
	case ovJump:
		return m.jumpGo()
	case ovNew:
		return m.newGo()
	case ovSearch:
		return m.searchGo()
	}
	return m.roomGo()
}

// ovBack leaves a form for its list, or closes the overlay.
func (m *Model) ovBack() {
	ov := m.ov
	switch {
	case ov.kind == ovNew && ov.step != stepChoose:
		ov.sel = int(ov.step) - int(stepDM)
		ov.step = stepChoose
	case ov.kind == ovRoom && ov.step != stepMembers:
		ov.step, ov.sel = stepMembers, 0
	default:
		m.closeOverlay()
		return
	}
	ov.field, ov.err, ov.info, ov.top, ov.clicked = input{}, "", "", 0, ""
}

// ovStepTo moves to step with an empty form.
func (m *Model) ovStepTo(step ovStep, text string) {
	ov := m.ov
	ov.step, ov.sel, ov.top, ov.err, ov.info, ov.clicked = step, 0, 0, "", "", ""
	ov.field = input{}
	ov.field.set(text)
}

// ovTarget names what an overlay key acts on, which it must still be on
// when it acts.
func (m *Model) ovTarget() string {
	return fmt.Sprintf("overlay %d %d %s %v", m.ov.gen, m.ov.step, m.ovSel().id, m.ov.ask != nil)
}

// ovSel is the selected row, if there is one.
func (m *Model) ovSel() ovRow {
	rows := m.ovRows()
	if m.ov.sel >= 0 && m.ov.sel < len(rows) {
		return rows[m.ov.sel]
	}
	return ovRow{}
}

// ovRows are the overlay's rows as they stand.
func (m *Model) ovRows() []ovRow {
	ov := m.ov
	switch ov.kind {
	case ovJump:
		return m.jumpRows()
	case ovSearch:
		return m.searchRows()
	case ovNew:
		return m.newRows()
	}
	return m.roomRows()
}

// ovPaste takes a bracketed paste: a field takes its first line.
func (m *Model) ovPaste(s string) tea.Cmd {
	m.ov.ask = nil
	if !m.ov.hasField() {
		return m.setBanner("paste ignored")
	}
	line, _, _ := strings.Cut(strings.ReplaceAll(s, "\r", "\n"), "\n")
	return m.ovEdit(func(in *input) { in.insert(cleanField(line)) })
}

// ovClick handles a left click: outside the overlay it closes it, on a row
// it selects it, and on the selected row clicked before it acts.
func (m *Model) ovClick(x, y int) tea.Cmd {
	ov := m.ov
	r := m.ovRect()
	if x < r.x || x >= r.x+r.w || y < r.y || y >= r.y+r.h {
		m.closeOverlay()
		return nil
	}
	ov.ask = nil
	i, ok := m.ovRowAt(y)
	if !ok {
		return nil
	}
	row := m.ovRows()[i]
	if row.id == "vis" {
		ov.open = !ov.open
		return nil
	}
	ov.sel = i
	if ov.clicked == row.id {
		ov.clicked = ""
		return m.ovGo()
	}
	ov.clicked = row.id
	return nil
}

// ovWheel moves the selection with the wheel.
func (m *Model) ovWheel(up bool) {
	if up {
		m.ovDo(actUp)
	} else {
		m.ovDo(actDown)
	}
}
