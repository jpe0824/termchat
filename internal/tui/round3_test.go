package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/ipc"
)

// pause lets d pass with no input; timers due by then fire.
func pause(h *tester, d time.Duration) {
	h.t.Helper()
	h.now = h.now.Add(d)
	if d > h.m.opts.PasteGap {
		h.settle()
	}
}

// keysAt sends keys 1 ms apart without letting timers fire in between.
func keysAt(h *tester, keys ...string) {
	h.t.Helper()
	step := h.step
	h.step = time.Millisecond
	for _, k := range keys {
		h.key(k)
	}
	h.step = step
}

// flat is the frame with every run of whitespace, line breaks included, as
// one space, so text wrapped over rows reads as one line.
func flat(h *tester) string { return strings.Join(strings.Fields(h.view()), " ") }

func noWrites(t *testing.T, f *fakeBackend) {
	t.Helper()
	if got := f.writes(anyWrite...); got != nil {
		t.Errorf("writes = %q", got)
	}
}

// onOwnMessage opens # platform at width w and selects "on it".
func onOwnMessage(t *testing.T, w int) (*fakeBackend, *tester) {
	f := fixture()
	h := opened(t, f, w, 20)
	h.key("up")
	h.key("up")
	if selectedText(h) != "on it" {
		t.Fatalf("selected %q", selectedText(h))
	}
	f.resetCalls()
	return f, h
}

// --- the quit warning ---

func TestQuitWarningVisibleEverywhere(t *testing.T) {
	for _, w := range pasteWidths {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			h := opened(t, fixture(), w, 20)
			h.typeText("x")
			h.key("ctrl+c")
			mustContain(t, flat(h), "ctrl+c again to quit — unsent drafts will be lost")
			h.key("esc") // chat list: the list view in narrow and tiny
			h.key("q")
			if h.quit {
				t.Fatal("q with a draft quit at once")
			}
			mustContain(t, flat(h), "q again to quit — unsent drafts will be lost")
			h.key("?")
			h.key("ctrl+c")
			mustContain(t, flat(h), "ctrl+c again to quit")
		})
	}
	h := opened(t, fixture(), 90, 6) // no footer, no top bar
	h.typeText("x")
	h.key("ctrl+c")
	if h.quit {
		t.Fatal("quit")
	}
	mustContain(t, flat(h), "ctrl+c again to quit")
}

func TestWhitespaceDraftIsNoDraft(t *testing.T) {
	h := opened(t, fixture(), 90, 20)
	h.typeText("   ")
	h.key("esc")
	h.key("q")
	if !h.quit {
		t.Error("a whitespace-only draft held up q")
	}
}

func TestQuitWarningNamesTheDraftsChat(t *testing.T) {
	h := opened(t, fixture(), 90, 20)
	h.typeText("x")
	h.click(4, 2) // dana
	h.key("esc")
	h.key("q")
	mustContain(t, flat(h), "unsent draft in #platform — q again to quit")
}

// --- guarded keys in the thread ---

func TestPastedCommandsOnASelectionDoNothing(t *testing.T) {
	for _, w := range pasteWidths {
		for _, keys := range [][]string{{"d", "y", "q"}, {"x", "y"}, {"e", "d", "i", "t"}} {
			t.Run(fmt.Sprint(w, keys), func(t *testing.T) {
				f, h := onOwnMessage(t, w)
				burst(h, keys...)
				noWrites(t, f)
				if h.quit || h.m.drafts["r-platform"].edit != nil {
					t.Errorf("quit %v edit %v", h.quit, h.m.drafts["r-platform"].edit)
				}
				mustNotContain(t, h.view(), "delete this message?", "discard this message?")
			})
		}
	}
}

func TestPastedYesDoesNotConfirm(t *testing.T) {
	for _, w := range pasteWidths {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			f, h := onOwnMessage(t, w)
			h.key("d")
			mustContain(t, h.view(), "delete this message? y/n")
			burst(h, "y", "e", "s")
			noWrites(t, f)
			mustNotContain(t, h.view(), "delete this message?")
		})
	}
}

func TestPromptNotConfirmedRightAfterItOpens(t *testing.T) {
	f, h := onOwnMessage(t, 90)
	keysAt(h, "d")
	pause(h, 30*time.Millisecond) // d acts and asks
	keysAt(h, "y")                // 31 ms after d, 5 ms after the question
	pause(h, time.Second)
	noWrites(t, f)
}

// --- guarded keys in the chat list ---

func TestPastedKeysInTheListOpenNothing(t *testing.T) {
	for _, w := range pasteWidths {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			f := fixture()
			h := opened(t, f, w, 20)
			h.key("esc")
			f.resetCalls()
			burst(h, "j", "j", "enter", "h", "e", "l", "l", "o", "enter")
			if h.m.openID != "r-platform" || h.quit {
				t.Errorf("open %q quit %v", h.m.openID, h.quit)
			}
			noWrites(t, f)
			burst(h, "q", "a", "b")
			if h.quit {
				t.Error("a pasted leading q quit")
			}
		})
	}
}

// --- pastes split by a pause ---

func TestSplitPastes(t *testing.T) {
	for _, w := range pasteWidths {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			f := fixture()
			f.outbox = nil // so ↑ selects "on it", which d can delete
			h := opened(t, f, w, 20)
			f.resetCalls()
			pause(h, time.Second)
			keysAt(h, "up")
			pause(h, 40*time.Millisecond)
			keysAt(h, "d", "y")
			pause(h, time.Second)
			noWrites(t, f)

			f = fixture()
			h = opened(t, f, w, 20)
			f.resetCalls()
			pause(h, time.Second)
			keysAt(h, "esc")
			pause(h, 40*time.Millisecond)
			keysAt(h, "j", "j", "enter", "h", "i", "enter")
			pause(h, time.Second)
			if h.m.openID != "r-platform" {
				t.Errorf("opened %q", h.m.openID)
			}
			noWrites(t, f)
		})
	}
}

func TestLoneCtrlCThenTypingQuitsWithoutDrafts(t *testing.T) {
	h := opened(t, fixture(), 90, 20)
	pause(h, time.Second)
	keysAt(h, "ctrl+c")
	pause(h, 40*time.Millisecond)
	if !h.quit {
		t.Error("a lone ctrl+c with no drafts did not quit")
	}
}

// --- pastes and swallowing ---

func TestBracketedPasteOutsideInputIgnored(t *testing.T) {
	for _, w := range pasteWidths {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			f := fixture()
			h := newTester(t, f)
			h.size(w, 20) // a preview: the list or the thread has focus
			h.paste("d\ny")
			if h.draft() != "" || h.m.focus == focusInput {
				t.Errorf("draft %q focus %v", h.draft(), h.m.focus)
			}
			note := "paste ignored — press i to type"
			if w >= 70 {
				note = "paste ignored — press enter to open a chat"
			}
			mustContain(t, flat(h), note)
			noWrites(t, f)
		})
	}
}

func TestSwallowLastsTheBurst(t *testing.T) {
	f := fixture()
	f.convs = nil
	h := started(t, f, 90, 20)
	h.m.focus = focusInput
	burst(h, "x", "q", "q")
	if h.quit {
		t.Fatal("keys in the burst after the first ran as commands")
	}
	h.key("q")
	if !h.quit {
		t.Error("a lone q afterwards did not quit")
	}
}

func TestGapZeroHasNoGuards(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20, func(o *Options) { o.PasteGap = -1 })
	h.key("up")
	h.key("up")
	keysAt(h, "d", "y")
	if got := f.writes("delete "); !slices.Equal(got, []string{"delete r-platform r-platform-m6"}) {
		t.Errorf("deletes = %q", got)
	}
}

// --- deliberate keys still work ---

func TestDeliberateKeysAct(t *testing.T) {
	for _, w := range pasteWidths {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			f, h := onOwnMessage(t, w)
			h.key("d")
			h.key("y")
			if got := f.writes("delete "); !slices.Equal(got, []string{"delete r-platform r-platform-m6"}) {
				t.Errorf("d y: %q", got)
			}

			f = failedFixture()
			h = opened(t, f, w, 20)
			h.key("up")
			h.key("x")
			h.key("y")
			h.key("up")
			h.key("down")
			h.key("r")
			if got := f.writes("discard ", "retry "); !slices.Equal(got, []string{"discard o1", "retry o1"}) {
				t.Errorf("x y, r: %q", got)
			}

			_, h = onOwnMessage(t, w)
			h.key("e")
			if h.draft() != "on it" || h.m.focus != focusInput {
				t.Errorf("e: draft %q focus %v", h.draft(), h.m.focus)
			}
			h.key("esc") // cancel: back on the message
			h.key("i")
			if h.m.focus != focusInput {
				t.Errorf("i: focus %v", h.m.focus)
			}

			h = opened(t, fixture(), w, 20)
			h.key("esc")
			h.key("j")
			h.key("enter")
			if h.m.openID != "r-deploys" {
				t.Errorf("enter opened %q", h.m.openID)
			}
			h.key("q")
			if h.quit {
				t.Error("q in the input quit")
			}
		})
	}
	h := opened(t, fixture(), 90, 20)
	h.key("esc")
	h.key("tab")
	if h.m.focus != focusThread {
		t.Errorf("tab: focus %v", h.m.focus)
	}
	h.key("q")
	if !h.quit {
		t.Error("q did not quit")
	}
}

func TestDeliberateRStartsTheProcess(t *testing.T) {
	f := fixture()
	starts := 0
	h := opened(t, f, 90, 20, func(o *Options) {
		o.Start = func(context.Context) (Backend, error) { starts++; return f, nil }
	})
	h.key("esc")
	h.send(backendDownMsg{err: ipc.ErrClosed})
	h.key("r")
	if starts != 1 {
		t.Errorf("r started %d times", starts)
	}
}

func TestAutoRepeatMovesEveryTime(t *testing.T) {
	h := opened(t, fixture(), 90, 20)
	h.key("esc")
	h.step = 15 * time.Millisecond
	var got []string
	for range 3 {
		h.key("j")
		got = append(got, h.m.cursorID())
	}
	if want := []string{"r-deploys", "r-random", "r-random"}; strings.Join(got, ",") != strings.Join(want, ",") {
		// The list is dana, sam, ci-bot, # platform, # deploys, # random.
		t.Errorf("held j: %q, want %q", got, want)
	}
	h = opened(t, fixture(), 90, 20)
	h.key("up")
	h.key("up")
	h.key("up") // yes, 10 min ago
	h.step = 15 * time.Millisecond
	var sel []string
	for range 2 {
		h.key("j")
		sel = append(sel, selectedText(h))
	}
	if want := []string{"on it", "pushing a fix now"}; !slices.Equal(sel, want) {
		t.Errorf("held j on a selection: %q", sel)
	}
}
