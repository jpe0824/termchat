package tui

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/ipc"
)

// Pasted text arriving as keys 1 ms apart, wherever the keyboard is, must
// change nothing: no backend write, no other conversation opened, no draft
// touched. Only the usable input takes it, whole, as its draft.

var pastes = [][]string{
	{"query := d.exec(x)\n"},
	{"i said hi\n"},
	{"edit\n"},
	{"yes, do it\n"},
	{"jjenterhi\n"},
	{"dyq"},
	{"xy"},
	{"r", "un it\n"},
	{"/notify muted\n"},
	{"/", "help\n"},
}

var everyWrite = []string{"send ", "edit ", "delete ", "retry ", "discard ", "markread ", "opendm ", "room ", "configset "}

// start is one place the keyboard can be when a paste arrives.
type start struct {
	name   string
	widths []int
	setup  func(t *testing.T, w int, starts *int) (*fakeBackend, *tester)
	// hint says keys that are not commands leave a note naming the key that
	// leads to the input from there.
	hint bool
}

func openedAt(fx func() *fakeBackend, keys ...string) func(*testing.T, int, *int) (*fakeBackend, *tester) {
	return func(t *testing.T, w int, starts *int) (*fakeBackend, *tester) {
		f := fx()
		h := opened(t, f, w, 20, markReadOn, countStarts(f, starts))
		for _, k := range keys {
			h.key(k)
		}
		return f, h
	}
}

func thenDown(setup func(*testing.T, int, *int) (*fakeBackend, *tester)) func(*testing.T, int, *int) (*fakeBackend, *tester) {
	return func(t *testing.T, w int, starts *int) (*fakeBackend, *tester) {
		f, h := setup(t, w, starts)
		h.send(backendDownMsg{err: ipc.ErrClosed})
		return f, h
	}
}

var starts = []start{
	{"chat list", pasteWidths, openedAt(fixture, "esc"), true},
	{"chat list, process down", pasteWidths, thenDown(openedAt(fixture, "esc")), false},
	{"own delivered message", pasteWidths, openedAt(fixture, "up", "up"), true},
	{"own failed message", pasteWidths, openedAt(failedFixture, "up"), true},
	{"other person's message", pasteWidths, openedAt(fixture, "up", "up", "up"), true},
	{"delete question", pasteWidths, openedAt(fixture, "up", "up", "d"), true},
	{"discard question", pasteWidths, openedAt(failedFixture, "up", "x"), true},
	{"input", pasteWidths, openedAt(fixture), false},
	{"command list open", pasteWidths, func(t *testing.T, w int, starts *int) (*fakeBackend, *tester) {
		f, h := openedAt(fixture)(t, w, starts)
		h.key("/")
		if c := h.m.drafts["r-platform"]; c == nil || !c.cmd || len(h.m.slashMatches(c)) == 0 {
			t.Fatal("/ alone did not open the command list")
		}
		return f, h
	}, false},
	{"disabled input", pasteWidths, thenDown(openedAt(fixture)), false},
	{"previewed thread", []int{44, 30}, func(t *testing.T, w int, starts *int) (*fakeBackend, *tester) {
		f := fixture()
		h := newTester(t, f, markReadOn, countStarts(f, starts))
		h.size(w, 20)
		return f, h
	}, true},
	{"thread, no selection", []int{90}, openedAt(fixture, "esc", "tab"), true},
	{"help", pasteWidths, openedAt(fixture, "esc", "?"), false},
}

// state is what a paste must leave alone.
type state struct {
	open, cursor string
	focus        focusArea
	list, help   bool
	sel          string
	drafts       map[string]string
	overlay      string
}

func snapshot(h *tester) state {
	s := state{open: h.m.openID, cursor: h.m.cursorID(), focus: h.m.focus, list: h.m.listShown(),
		help: h.m.help, drafts: map[string]string{}, overlay: ovState(h)}
	if h.m.thread != nil {
		s.sel = h.m.thread.sel
	}
	for conv, c := range h.m.drafts {
		s.drafts[conv] = c.in.text
		if c.edit != nil {
			s.drafts[conv] += " (editing " + c.edit.msgID + ")"
		}
	}
	return s
}

// managedFixture is the fixture with # platform created by the user, so its
// room panel offers every key.
func managedFixture() *fakeBackend {
	f := peopleFixture()
	f.convs[3].Creator = "jason"
	return f
}

// overlayStarts put the keyboard in each overlay, and in each of their
// steps, text fields and questions.
var overlayStarts = []start{
	{"jump field", pasteWidths, openedAt(peopleFixture, "esc", "ctrl+k"), false},
	{"search field", pasteWidths, openedAt(peopleFixture, "esc", "/"), false},
	{"new: choices", pasteWidths, openedAt(peopleFixture, "esc", "ctrl+n"), false},
	{"new: handle field", pasteWidths, openedAt(peopleFixture, "esc", "ctrl+n", "enter"), false},
	{"new: room field", pasteWidths, openedAt(peopleFixture, "esc", "ctrl+n", "down", "enter"), false},
	{"new: open rooms", pasteWidths, openedAt(peopleFixture, "esc", "ctrl+n", "down", "down", "enter"), false},
	{"room panel", pasteWidths, openedAt(managedFixture, "ctrl+o"), false},
	{"room: invite field", pasteWidths, openedAt(managedFixture, "ctrl+o", "a"), false},
	{"room: rename field", pasteWidths, openedAt(managedFixture, "ctrl+o", "n"), false},
	{"room: remove question", pasteWidths, openedAt(managedFixture, "ctrl+o", "down", "x"), false},
	{"room: leave question", pasteWidths, openedAt(managedFixture, "ctrl+o", "l"), false},
	{"room panel, wide", []int{120}, openedAt(managedFixture, "ctrl+o"), false},
}

func TestPastedKeysChangeNothingInOverlays(t *testing.T) {
	for _, st := range overlayStarts {
		for _, w := range st.widths {
			for _, parts := range pastes {
				text := strings.Join(parts, "")
				t.Run(fmt.Sprintf("%s/%d/%q", st.name, w, text), func(t *testing.T) {
					_, h := st.setup(t, w, new(int))
					if h.m.ov == nil {
						t.Fatalf("%s: no overlay", st.name)
					}
					checkPaste(t, st, w, parts, 0)
					for cut := 2; cut <= len(text)-2; cut++ {
						checkPaste(t, st, w, []string{text}, cut)
					}
				})
			}
		}
	}
}

func TestPastedKeysChangeNothing(t *testing.T) {
	for _, st := range starts {
		for _, w := range st.widths {
			for _, parts := range pastes {
				text := strings.Join(parts, "")
				t.Run(fmt.Sprintf("%s/%d/%q", st.name, w, text), func(t *testing.T) {
					checkPaste(t, st, w, parts, 0)
					// The same text with a pause in it, wherever the pause does
					// not leave one key on its own: that key is a lone press.
					for cut := 2; cut <= len(text)-2; cut++ {
						if st.name == "previewed thread" && strings.Trim(text[:cut], "jk") == "" {
							continue // movement keys, then silence: a deliberate scroll
						}
						checkPaste(t, st, w, []string{text}, cut)
					}
				})
			}
		}
	}
}

// checkPaste feeds parts to st at width w, with a 40 ms pause before byte
// cut of the text when cut is not 0, and checks nothing came of it.
func checkPaste(t *testing.T, st start, w int, parts []string, cut int) {
	t.Helper()
	text := strings.Join(parts, "")
	fail := func(format string, args ...any) {
		t.Helper()
		t.Errorf("cut %d: "+format, append([]any{cut}, args...)...)
	}
	started := 0
	f, h := st.setup(t, w, &started)
	f.resetCalls()
	started = 0
	before := snapshot(h)

	if cut == 0 {
		feed(h, parts...)
	} else {
		feed(h, text[:cut])
		pause(h, 40*time.Millisecond)
		soon(h, func() { h.typeText(text[cut:]) })
	}
	typed := strings.TrimSuffix(text, "\n")
	if st.name == "input" && h.draft() != typed {
		fail("draft %q, want the pasted text %q", h.draft(), typed)
	}
	if st.name == "command list open" {
		// The text joins the command; its enter runs nothing.
		before.drafts["r-platform"] = "/" + typed
		if c := h.m.drafts["r-platform"]; c == nil || !c.cmd {
			fail("command mode ended")
		}
	}
	pause(h, time.Second)

	var wantWrites []string
	if st.name == "input" {
		// The text is the draft, intact; a line break ending the paste,
		// with nothing after it, is an enter.
		before.drafts["r-platform"] = typed
		if typed != text {
			wantWrites = []string{fmt.Sprintf("send r-platform %q tui", typed)}
			before.drafts["r-platform"] = ""
		}
	}
	if got := f.writes(everyWrite...); !slices.Equal(got, wantWrites) {
		fail("writes = %q, want %q", got, wantWrites)
	}
	if started != 0 {
		fail("started the background process %d times", started)
	}
	if h.clips != nil {
		fail("copied %q to the clipboard", h.clips)
	}
	for _, c := range f.writes("messages ") {
		if !strings.HasPrefix(c, "messages "+before.open+" ") {
			fail("read another conversation: %q", c)
		}
	}
	if h.quit {
		fail("quit")
	}
	if h.m.note != nil && h.m.note.ask != nil {
		fail("a question is shown: %q", h.m.note.text)
	}
	if h.m.ov != nil && h.m.ov.ask != nil {
		fail("a question is shown in the overlay: %q", h.m.ov.ask.text)
	}
	after := snapshot(h)
	if st.name != "input" && st.name != "disabled input" && st.name != "help" {
		// Movement keys in the text may move the cursor, the selection or
		// the view; nothing else.
		after.cursor, after.sel = before.cursor, before.sel
	}
	if after.open != before.open || after.cursor != before.cursor || after.focus != before.focus ||
		after.list != before.list || after.help != before.help || after.sel != before.sel ||
		after.overlay != before.overlay ||
		!maps.Equal(after.drafts, before.drafts) {
		fail("state %+v, want %+v", after, before)
	}
	if st.name == "previewed thread" && !h.m.preview {
		fail("the preview was opened")
	}
	switch {
	case cut != 0:
		// The pause starts the note over; what is left may be all commands.
	case st.name == "chat list":
		mustContain(t, flat(h), "press enter to open a chat")
		mustNotContain(t, flat(h), "press i to type")
	case st.hint:
		mustContain(t, flat(h), "press i to type")
		mustNotContain(t, flat(h), "press enter to open a chat")
	case st.name == "disabled input":
		if h.m.note == nil || h.m.note.text != downNote {
			fail("note = %+v, want the down note", h.m.note)
		}
	default:
		mustNotContain(t, flat(h), "press i to type", "press enter to open a chat")
	}
}

// Each key below is pressed alone, with silence longer than the gap after.

func TestDeliberateKeysStillWork(t *testing.T) {
	for _, w := range pasteWidths {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			n := 0
			// i, typing, enter: from a selection, and from the thread.
			f, h := openedAt(fixture, "up", "up")(t, w, &n)
			f.resetCalls()
			h.key("i")
			if h.m.focus != focusInput {
				t.Fatalf("i: focus %v", h.m.focus)
			}
			h.typeText("i said hi")
			h.enter()
			if got := f.writes(anyWrite...); !slices.Equal(got, []string{`send r-platform "i said hi" tui`}) {
				t.Errorf("i, text, enter: %q", got)
			}

			f, h = openedAt(fixture, "up", "up")(t, w, &n)
			f.resetCalls()
			h.key("d")
			h.key("y")
			if got := f.writes(everyWrite...); !slices.Equal(got, []string{"delete r-platform r-platform-m6"}) {
				t.Errorf("d y: %q", got)
			}

			f, h = openedAt(failedFixture, "up")(t, w, &n)
			f.resetCalls()
			h.key("r")
			h.key("x")
			h.key("y")
			if got := f.writes(everyWrite...); !slices.Equal(got, []string{"retry o1", "discard o1"}) {
				t.Errorf("r, x y: %q", got)
			}

			_, h = openedAt(fixture, "up", "up")(t, w, &n)
			h.key("e")
			if c := h.m.drafts["r-platform"]; c == nil || c.edit == nil || c.edit.msgID != "r-platform-m6" ||
				h.draft() != "on it" || h.m.focus != focusInput {
				t.Errorf("e: draft %q focus %v", h.draft(), h.m.focus)
			}

			_, h = openedAt(fixture, "up", "up")(t, w, &n)
			h.key("c")
			if !slices.Equal(h.clips, []string{"on it"}) {
				t.Errorf("c: clips %q", h.clips)
			}

			for _, down := range []func(*testing.T, int, *int) (*fakeBackend, *tester){
				thenDown(openedAt(fixture, "esc")), thenDown(openedAt(fixture)),
			} {
				n = 0
				_, h = down(t, w, &n)
				h.key("r")
				if n != 1 {
					t.Errorf("r with the process down started it %d times", n)
				}
			}

			f, h = openedAt(fixture, "esc")(t, w, &n)
			h.key("j")
			f.resetCalls()
			h.key("enter")
			if h.m.openID != "r-deploys" || h.m.focus != focusInput ||
				!slices.Equal(f.writes(everyWrite...), []string{"markread r-deploys"}) {
				t.Errorf("enter: open %q focus %v writes %q", h.m.openID, h.m.focus, f.writes(everyWrite...))
			}

			_, h = openedAt(fixture, "esc")(t, w, &n)
			h.key("tab")
			want := focusThread
			if w < 70 {
				want = focusList // one pane: nothing to switch to
			}
			if h.m.focus != want {
				t.Errorf("tab: focus %v, want %v", h.m.focus, want)
			}
			h.key("tab")
			if h.m.focus != focusList {
				t.Errorf("tab tab: focus %v", h.m.focus)
			}

			// A held j, repeating every 15 ms, moves on every repeat.
			_, h = openedAt(fixture, "esc")(t, w, &n)
			h.step = 15 * time.Millisecond
			var rows []string
			for range 3 {
				h.key("j")
				rows = append(rows, h.m.cursorID())
			}
			if !slices.Equal(rows, []string{"r-deploys", "r-random", "r-random"}) {
				t.Errorf("held j on the list: %q", rows)
			}
			_, h = openedAt(fixture, "up", "up", "up")(t, w, &n)
			h.step = 15 * time.Millisecond
			var sel []string
			for range 2 {
				h.key("j")
				sel = append(sel, selectedText(h))
			}
			if !slices.Equal(sel, []string{"on it", "pushing a fix now"}) {
				t.Errorf("held j on a selection: %q", sel)
			}
		})
	}
}

// On a preview, i, enter or a click is what opens it; a letter is not.
func TestPreviewOpensOnlyOnACommand(t *testing.T) {
	for _, w := range []int{44, 30} {
		for _, k := range []string{"i", "enter"} {
			f := fixture()
			h := newTester(t, f, markReadOn)
			h.size(w, 20)
			h.typeText("h")
			if h.m.focus != focusThread || h.draft() != "" || !h.m.preview || f.writes(everyWrite...) != nil {
				t.Fatalf("width %d: a letter on the preview: focus %v draft %q preview %v writes %q",
					w, h.m.focus, h.draft(), h.m.preview, f.writes(everyWrite...))
			}
			mustContain(t, flat(h), "press i to type")
			h.key(k)
			if h.m.focus != focusInput || h.m.preview {
				t.Fatalf("width %d: %s: focus %v preview %v", w, k, h.m.focus, h.m.preview)
			}
			h.typeText("hello")
			h.enter()
			if got := f.writes(everyWrite...); !slices.Equal(got,
				[]string{"markread r-platform", `send r-platform "hello" tui`}) {
				t.Errorf("width %d: %s, text, enter: %q", w, k, got)
			}
		}
		// A held j scrolls the preview on every repeat, and opens it once
		// the key is let go.
		f := fixture()
		h := newTester(t, f, markReadOn)
		h.size(w, 8)
		h.key("home")
		f.resetCalls()
		h.m.preview, h.m.readOpen = true, false
		h.step = 15 * time.Millisecond
		var tops []string
		for range 3 {
			h.key("j")
			tops = append(tops, h.view())
		}
		if tops[0] == tops[1] || tops[1] == tops[2] {
			t.Errorf("width %d: held j on the preview did not scroll on every repeat", w)
		}
		if f.writes("markread ") != nil {
			t.Errorf("width %d: marked read while j was held", w)
		}
		pause(h, time.Second)
		if got := f.writes("markread "); !slices.Equal(got, []string{"markread r-platform"}) {
			t.Errorf("width %d: after j was let go: %q", w, got)
		}
	}
}
