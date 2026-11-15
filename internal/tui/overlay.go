package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
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
	ask      *question
	// open is the new room's visibility; conv the room the panel is about.
	open bool
	conv string
	// seq numbers the search queries; results are shown only for the
	// newest, and searched says they came back.
	seq      timer
	results  []ipc.Message
	searched bool
}

// ovRow is one row of an overlay's list. text[hlAt:hlEnd] is a part to
// mark, whole characters, none when hlEnd is 0; from is where the part of
// text that may be cut to bring it into view starts.
type ovRow struct {
	id, text          string
	hlAt, hlEnd, from int
}

// ovSpec is what one kind of overlay is made of; the shared frame draws
// and drives every kind through it.
type ovSpec struct {
	title func(*Model) string
	// field reports whether a step has a text field.
	field func(ovStep) bool
	// start fits a new overlay to the view, or says why it cannot open;
	// nil when it needs nothing. load reads what it lists.
	start func(*Model, *overlay) (refused string)
	load  func(*Model) tea.Cmd
	// edited reacts to the field's text changing; nil for nothing.
	edited func(*Model) tea.Cmd
	rows   func(*Model) []ovRow
	// body is the lines above and below the rows, and what shows in their
	// place when there are none, for content w cells wide.
	body func(m *Model, w int) (head []string, empty string, tail []string)
	// act acts on the selected row, or on the field.
	act func(*Model) tea.Cmd
	// first is the step it opens on, from which esc closes it.
	first ovStep
}

// ovSpecs are the overlay kinds. They are set in init because their
// functions reach back to the table through the shared frame.
var ovSpecs map[ovKind]ovSpec

func init() {
	always := func(ovStep) bool { return true }
	ovSpecs = map[ovKind]ovSpec{
		ovJump: {
			title: func(*Model) string { return "Jump to" }, field: always, load: (*Model).requestPeople,
			rows: (*Model).jumpRows, act: (*Model).jumpGo,
			body: func(*Model, int) ([]string, string, []string) { return nil, "no matches", nil },
		},
		ovSearch: {
			title: func(*Model) string { return "Search" }, field: always, load: (*Model).requestChats,
			edited: (*Model).searchChanged, rows: (*Model).searchRows, act: (*Model).searchGo,
			body: func(m *Model, _ int) ([]string, string, []string) { return nil, m.searchEmpty(), nil },
		},
		ovNew: {
			title: (*Model).newTitle, field: func(s ovStep) bool { return s == stepDM || s == stepRoom },
			load: (*Model).requestChats, rows: (*Model).newRows, act: (*Model).newGo,
			body: func(m *Model, _ int) ([]string, string, []string) { return nil, m.newEmpty(), nil },
		},
		ovRoom: {
			title: (*Model).roomTitle, field: func(s ovStep) bool { return s == stepInvite || s == stepRename },
			start: (*Model).roomStartPanel, load: (*Model).requestPeople, rows: (*Model).roomRows,
			act: (*Model).roomGo, body: (*Model).roomBody, first: stepMembers,
		},
	}
}

func (o *overlay) spec() ovSpec { return ovSpecs[o.kind] }

func (o *overlay) hasField() bool { return o.spec().field(o.step) }

// openOverlay opens kind over the current view. Every overlay needs the
// background process.
func (m *Model) openOverlay(kind ovKind) tea.Cmd {
	if m.b == nil || m.procDown {
		return m.setBanner(downBanner)
	}
	ov := &overlay{kind: kind}
	spec := ov.spec()
	ov.step = spec.first
	if spec.start != nil {
		if why := spec.start(m, ov); why != "" {
			return m.setBanner(why)
		}
	}
	m.ovGen++
	ov.gen = m.ovGen
	m.ov = ov
	m.clearBanner()
	return spec.load(m)
}

// closeOverlay closes the open overlay.
func (m *Model) closeOverlay() { m.ov = nil }

// overlayKey handles a key while an overlay is open. Typing and moving act
// at once; a key that acts waits out the paste gap as a lone press.
func (m *Model) overlayKey(k tea.KeyPressMsg) tea.Cmd {
	ov, s := m.ov, k.String()
	if q := ov.ask; q != nil {
		ov.ask = nil
		return m.answer(q, s, keyGuarded)
	}
	if ov.hasField() {
		if t := keyText(k); t != "" {
			return m.ovEdit(func(in *input) { in.insert(cleanField(t)) })
		}
		if edit, ok := editKeys[s]; ok {
			return m.ovEdit(edit)
		}
	}
	b, ok := m.ovBinding(s)
	if !ok {
		if s == "?" && !ov.hasField() {
			m.help = true
		}
		return nil
	}
	return m.press(b.kind, hold{run: func() tea.Cmd {
		if m.ov == nil {
			return nil
		}
		return b.run(m, keyCtx{key: s})
	}})
}

// editKeys are the keys that edit an overlay's text field.
var editKeys = map[string]func(*input){
	"left": (*input).left, "right": (*input).right, "home": (*input).home, "end": (*input).end,
	"ctrl+a": (*input).home, "ctrl+e": (*input).end, "backspace": (*input).backspace,
	"ctrl+h": (*input).backspace, "delete": (*input).del, "ctrl+w": (*input).killWord,
	"ctrl+u": (*input).killLine,
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
	if edited := ov.spec().edited; edited != nil {
		return edited(m)
	}
	return nil
}

// ovSelect runs f on the overlay, given its row count and page size as
// they were before, then keeps the selection among those rows.
func (m *Model) ovSelect(f func(ov *overlay, n, page int)) {
	ov := m.ov
	n := len(m.ovRows())
	f(ov, n, max(m.ovListRows()-1, 1))
	ov.sel = max(min(ov.sel, n-1), 0)
}

// ovMoveKey moves the selection for a key of moveKeys or listKeys.
func (m *Model) ovMoveKey(c keyCtx) tea.Cmd {
	m.ovSelect(func(ov *overlay, n, page int) {
		switch c.key {
		case "up", "k":
			ov.sel--
		case "down", "j":
			ov.sel++
		case "pgup":
			ov.sel -= page
		case "pgdown":
			ov.sel += page
		case "home":
			ov.sel = 0
		case "end":
			ov.sel = n - 1
		}
	})
	return nil
}

func (m *Model) ovGoKey(keyCtx) tea.Cmd { return m.ovGo() }

// ovBackKey only goes back: ovBack chooses the selection on the step it
// returns to, which the form's own row count says nothing about.
func (m *Model) ovBackKey(keyCtx) tea.Cmd {
	m.ovBack()
	return nil
}

func (m *Model) ovToggleKey(keyCtx) tea.Cmd {
	m.ovSelect(func(ov *overlay, _, _ int) { ov.open = !ov.open })
	return nil
}

// ovGo acts on the selected row, or on the field.
func (m *Model) ovGo() tea.Cmd {
	ov := m.ov
	if ov.busy {
		return nil
	}
	return ov.spec().act(m)
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
func (m *Model) ovRows() []ovRow { return m.ov.spec().rows(m) }

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
