package tui

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/ipc"
)

// openers are the keys that open an overlay, with where they are pressed.
var openers = []struct {
	key  string
	kind ovKind
	// room says the key needs a room open, so the test opens one first.
	room bool
}{
	{"ctrl+k", ovJump, false},
	{"ctrl+n", ovNew, false},
	{"/", ovSearch, false},
	{"ctrl+o", ovRoom, true},
}

func TestOverlayOpenersAreGuarded(t *testing.T) {
	for _, o := range openers {
		for _, w := range []int{90, 44} {
			t.Run(fmt.Sprintf("%s/%d", o.key, w), func(t *testing.T) {
				setup := func() *tester {
					h := opened(t, peopleFixture(), w, 20)
					h.key("esc") // the list (narrow) or a lone esc (standard): no text field
					if w >= 70 {
						h.key("tab")
					}
					return h
				}
				h := setup()
				h.key(o.key)
				if h.m.ov == nil || h.m.ov.kind != o.kind {
					t.Fatalf("a lone %s opened %q", o.key, ovState(h))
				}
				// Inside a burst: typed right after another key.
				h = setup()
				feed(h, "x")
				soon(h, func() { h.key(o.key) })
				pause(h, time.Second)
				if h.m.ov != nil {
					t.Errorf("%s inside a burst opened %q", o.key, ovState(h))
				}
				// Followed by another key within the gap.
				h = setup()
				feed(h)
				h.now = h.now.Add(time.Second)
				soon(h, func() { h.key(o.key); h.key("x") })
				pause(h, time.Second)
				if h.m.ov != nil {
					t.Errorf("%s followed by a key opened %q", o.key, ovState(h))
				}
			})
		}
	}
	// From the input, too, ctrl+k, ctrl+n and ctrl+o are guarded, and / types.
	h := opened(t, peopleFixture(), 90, 20)
	h.key("ctrl+k")
	if ovState(h) != fmt.Sprintf("%d/0", ovJump) {
		t.Errorf("ctrl+k from the input: %q", ovState(h))
	}
	h = opened(t, peopleFixture(), 90, 20)
	feed(h, "hi", "/")
	soon(h, func() { h.key("ctrl+k") })
	pause(h, time.Second)
	if h.m.ov != nil || h.draft() != "hi/" {
		t.Errorf("typed ctrl+k: overlay %q draft %q", ovState(h), h.draft())
	}
}

func TestOverlaysDisabledWhileDown(t *testing.T) {
	for _, k := range []string{"ctrl+k", "ctrl+n", "/", "ctrl+o"} {
		h := opened(t, peopleFixture(), 90, 20)
		h.key("esc")
		h.send(backendDownMsg{err: ipc.ErrClosed})
		h.key(k)
		if h.m.ov != nil {
			t.Errorf("%s opened %q with the process down", k, ovState(h))
		}
		mustContain(t, flat(h), "background process not running — r to start")
	}
}

// pastedLine feeds text as a paste into an overlay's field and lets time
// pass; nothing may be written.
func pastedLine(t *testing.T, f *fakeBackend, h *tester, text string) {
	t.Helper()
	state := ovState(h)
	f.resetCalls()
	feed(h, text)
	pause(h, time.Second)
	h.fireSearch()
	pause(h, time.Second)
	if got := f.writes(append(roomWrites, everyWrite...)...); got != nil {
		t.Errorf("pasted %q wrote %q", text, got)
	}
	if ovState(h) != state {
		t.Errorf("pasted %q: overlay %q, want %q", text, ovState(h), state)
	}
}

func TestPastedLinesActNowhere(t *testing.T) {
	// Jump: the list is filtered, nothing opens; a lone enter then does.
	f := peopleFixture()
	h := started(t, f, 90, 24)
	h.key("ctrl+k")
	pastedLine(t, f, h, "platform\n")
	if h.m.ov.field.text != "platform" || h.m.openID != "r-platform" || h.m.focus != focusList {
		t.Errorf("jump: text %q open %q focus %v", h.m.ov.field.text, h.m.openID, h.m.focus)
	}
	h.enter()
	if h.m.ov != nil || h.m.focus != focusInput {
		t.Errorf("jump: a lone enter: overlay %q focus %v", ovState(h), h.m.focus)
	}

	// Search.
	f = peopleFixture()
	f.ov.results = map[string][]ipc.Message{"platform": {msg("r-deploys", 1, 1, "sam", "platform deploy")}}
	h = started(t, f, 90, 24)
	h.key("/")
	pastedLine(t, f, h, "platform\n")
	if h.m.openID != "r-platform" || len(rowTexts(h)) != 1 {
		t.Errorf("search: open %q rows %q", h.m.openID, rowTexts(h))
	}
	h.enter()
	if h.m.ov != nil || h.m.openID != "r-deploys" {
		t.Errorf("search: a lone enter: overlay %q open %q", ovState(h), h.m.openID)
	}

	// New room.
	f = peopleFixture()
	h = started(t, f, 90, 24)
	h.key("ctrl+n")
	h.key("down")
	h.enter()
	pastedLine(t, f, h, "newroom\n")
	h.enter()
	if got := f.writes(roomWrites...); !slices.Equal(got, []string{"room create  newroom  false"}) {
		t.Errorf("new room: a lone enter wrote %q", got)
	}

	// Direct message.
	f = peopleFixture()
	h = started(t, f, 90, 24)
	h.key("ctrl+n")
	h.enter()
	pastedLine(t, f, h, "priya\n")
	h.enter()
	if got := f.writes(roomWrites...); !slices.Equal(got, []string{"opendm priya"}) {
		t.Errorf("dm: a lone enter wrote %q", got)
	}

	// Invite.
	f = peopleFixture()
	h = deploysPanel(t, f)
	h.key("a")
	pastedLine(t, f, h, "dana\n")
	h.enter()
	if got := f.writes(roomWrites...); !slices.Equal(got, []string{"room invite r-deploys  dana false"}) {
		t.Errorf("invite: a lone enter wrote %q", got)
	}

	// Rename.
	f = peopleFixture()
	h = deploysPanel(t, f)
	h.key("n")
	h.key("ctrl+u")
	pastedLine(t, f, h, "ops\n")
	h.enter()
	if got := f.writes(roomWrites...); !slices.Equal(got, []string{"room rename r-deploys ops  false"}) {
		t.Errorf("rename: a lone enter wrote %q", got)
	}
}

// deploysPanel is the room panel of # deploys, which the user created.
func deploysPanel(t *testing.T, f *fakeBackend) *tester {
	t.Helper()
	h := started(t, f, 90, 24)
	h.key("down")
	h.key("enter")
	h.key("ctrl+o")
	if ovState(h) != fmt.Sprintf("%d/%d", ovRoom, stepMembers) {
		t.Fatalf("room panel: %q", ovState(h))
	}
	return h
}

func TestPromptsInOverlaysNeedALoneY(t *testing.T) {
	for _, c := range []struct {
		name, key, want string
	}{
		{"remove", "x", "room remove r-deploys  sam false"},
		{"leave", "l", "room leave r-deploys   false"},
	} {
		for _, text := range []string{"yes, do it\n", "y\n", "dyq", "xy"} {
			f := peopleFixture()
			h := deploysPanel(t, f)
			h.key("down") // sam
			h.key(c.key)
			if h.m.ov.ask == nil {
				t.Fatalf("%s: no question", c.name)
			}
			f.resetCalls()
			feed(h, text)
			pause(h, time.Second)
			if got := f.writes(roomWrites...); got != nil {
				t.Errorf("%s: pasted %q wrote %q", c.name, text, got)
			}
		}
		// y right after the question was asked is refused as well.
		f := peopleFixture()
		h := deploysPanel(t, f)
		h.key("down")
		soon(h, func() { h.key(c.key); h.key("y") })
		pause(h, time.Second)
		if got := f.writes(roomWrites...); got != nil {
			t.Errorf("%s: y within the gap of the question wrote %q", c.name, got)
		}
		// A lone y confirms.
		h.key(c.key)
		h.key("y")
		if got := f.writes(roomWrites...); !slices.Equal(got, []string{c.want}) {
			t.Errorf("%s: a lone y wrote %q", c.name, got)
		}
	}
}

func TestBracketedPasteIntoOverlays(t *testing.T) {
	f := peopleFixture()
	h := started(t, f, 90, 24)
	h.key("ctrl+k")
	h.paste("plat\x1b[31m\nform\n")
	if h.m.ov.field.text != "plat" || h.m.openID != "r-platform" {
		t.Errorf("paste into jump: text %q open %q", h.m.ov.field.text, h.m.openID)
	}
	pause(h, time.Second)
	if h.m.ov == nil {
		t.Fatal("a paste closed the overlay")
	}
	// No text field: ignored with a note.
	h = deploysPanel(t, f)
	f.resetCalls()
	h.paste("x\ny\n")
	pause(h, time.Second)
	if h.m.ov == nil || h.m.ov.ask != nil || f.writes(roomWrites...) != nil {
		t.Errorf("paste into the room panel: overlay %q ask %v writes %q", ovState(h), h.m.ov.ask,
			f.writes(roomWrites...))
	}
	mustContain(t, flat(h), "paste ignored")
}

func TestOverlayEscIsDeferred(t *testing.T) {
	h := started(t, peopleFixture(), 90, 24)
	h.key("ctrl+k")
	h.typeText("pl")
	soon(h, func() { h.key("esc"); h.key("a") })
	pause(h, time.Second)
	if h.m.ov == nil || h.m.ov.field.text != "pla" {
		t.Fatalf("esc followed by a key: overlay %q", ovState(h))
	}
	h.key("esc")
	if h.m.ov != nil || h.m.focus != focusList {
		t.Errorf("a lone esc: overlay %q focus %v", ovState(h), h.m.focus)
	}
	// Tab inserts nothing and moves nothing in a field.
	h.key("ctrl+k")
	h.typeText("pl")
	h.key("tab")
	if h.m.ov.field.text != "pl" || h.m.ov.sel != 0 {
		t.Errorf("tab: text %q sel %d", h.m.ov.field.text, h.m.ov.sel)
	}
	// esc from a step goes back to the choices.
	h.key("esc")
	h.key("ctrl+n")
	h.enter()
	h.key("esc")
	if ovState(h) != fmt.Sprintf("%d/%d", ovNew, stepChoose) {
		t.Errorf("esc from the dm step: %q", ovState(h))
	}
}

func TestSettingsPaste(t *testing.T) {
	f, h := settingsOpen(t, 90, 30)
	// The list takes no paste, typed or bracketed.
	pastedLine(t, f, h, "y\n")
	h.paste("true\n")
	if w := f.writes(everyWrite...); w != nil || ovState(h) != fmt.Sprintf("%d/%d", ovSettings, stepSettings) {
		t.Errorf("paste into the list: %q wrote %q", ovState(h), w)
	}
	mustContain(t, flat(h), "paste ignored")
	// The text step takes a paste whole and saves only on a lone enter.
	selectSetting(t, h, "notify.hook")
	h.enter()
	pastedLine(t, f, h, "notify-send hi\n")
	if h.m.ov.field.text != "notify-send hi" {
		t.Errorf("typed paste: field %q", h.m.ov.field.text)
	}
	h.key("ctrl+u")
	h.paste("say done\nrm -rf /\n")
	if h.m.ov.field.text != "say done" || f.writes(everyWrite...) != nil {
		t.Errorf("bracketed paste: field %q writes %q", h.m.ov.field.text, f.writes(everyWrite...))
	}
	h.enter()
	if got := f.writes("configset "); !slices.Equal(got, []string{"configset notify.hook say done"}) {
		t.Errorf("a lone enter wrote %q", got)
	}
}
