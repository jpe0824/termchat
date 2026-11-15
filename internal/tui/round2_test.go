package tui

import (
	"fmt"
	"slices"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
)

// burst sends keys 1 ms apart, as a paste without bracketed paste arrives,
// then lets the clock run on past the paste gap.
func burst(h *tester, keys ...string) {
	h.t.Helper()
	step := h.step
	h.now = h.now.Add(time.Second) // a paste starts after a pause
	h.step = time.Millisecond
	for _, k := range keys {
		h.key(k)
	}
	h.step = step
	h.settle()
}

var anyWrite = []string{"send ", "edit ", "delete ", "retry ", "discard "}

// atWidths runs fn at standard, narrow and tiny widths on an opened chat.
func atWidths(t *testing.T, fn func(t *testing.T, f *fakeBackend, h *tester)) {
	for _, w := range pasteWidths {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			f := fixture()
			h := opened(t, f, w, 20)
			f.resetCalls()
			fn(t, f, h)
		})
	}
}

func inInput(t *testing.T, h *tester, draft string) {
	t.Helper()
	if h.quit {
		t.Fatal("quit")
	}
	if h.m.focus != focusInput || h.m.listShown() || h.draft() != draft {
		t.Errorf("focus %v list %v draft %q, want the input with %q", h.m.focus, h.m.listShown(), h.draft(), draft)
	}
}

func TestPastedUpThenQ(t *testing.T) {
	atWidths(t, func(t *testing.T, f *fakeBackend, h *tester) {
		burst(h, "up", "q")
		inInput(t, h, "q")
		if got := f.writes(anyWrite...); got != nil {
			t.Errorf("writes = %q", got)
		}
	})
}

func TestPastedUpUpDY(t *testing.T) {
	atWidths(t, func(t *testing.T, f *fakeBackend, h *tester) {
		burst(h, "up", "up", "d", "y")
		inInput(t, h, "dy")
		if got := f.writes(anyWrite...); got != nil {
			t.Errorf("writes = %q", got)
		}
	})
}

func TestPastedEscThenQ(t *testing.T) {
	atWidths(t, func(t *testing.T, f *fakeBackend, h *tester) {
		burst(h, "esc", "q")
		inInput(t, h, "q")
	})
}

func TestPastedEscCannotOpenAnotherChat(t *testing.T) {
	atWidths(t, func(t *testing.T, f *fakeBackend, h *tester) {
		burst(h, "esc", "j", "j", "enter", "h", "i", "enter")
		if h.m.openID != "r-platform" || h.m.cursorID() != "r-platform" {
			t.Errorf("open %q cursor %q", h.m.openID, h.m.cursorID())
		}
		if got := f.writes(anyWrite...); !slices.Equal(got, []string{`send r-platform "jj\nhi" tui`}) {
			t.Errorf("writes = %q", got)
		}
		inInput(t, h, "")
	})
}

func TestLoneEscAndUpStillAct(t *testing.T) {
	atWidths(t, func(t *testing.T, f *fakeBackend, h *tester) {
		burst(h, "esc")
		if h.m.focus == focusInput && !h.m.listShown() {
			t.Error("a lone esc did not leave the input")
		}
	})
	atWidths(t, func(t *testing.T, f *fakeBackend, h *tester) {
		burst(h, "up")
		if h.m.focus != focusThread || h.m.thread.sel != "o:o1" {
			t.Errorf("a lone ↑: focus %v sel %q", h.m.focus, h.m.thread.sel)
		}
	})
}

func TestPastedCtrlCDoesNotQuit(t *testing.T) {
	atWidths(t, func(t *testing.T, f *fakeBackend, h *tester) {
		burst(h, "a", "b", "ctrl+c")
		inInput(t, h, "ab")
	})
	atWidths(t, func(t *testing.T, f *fakeBackend, h *tester) {
		burst(h, "ctrl+c", "a", "b")
		inInput(t, h, "ab")
	})
}

func TestCtrlCWithDraftsAsksTwice(t *testing.T) {
	h := opened(t, fixture(), 90, 20)
	h.typeText("x")
	h.key("ctrl+c")
	if h.quit {
		t.Fatal("one ctrl+c with a draft quit")
	}
	mustContain(t, h.view(), quitNote)
	h.key("ctrl+c")
	if !h.quit {
		t.Fatal("the second ctrl+c did not quit")
	}

	h = opened(t, fixture(), 90, 20)
	h.typeText("x")
	h.key("ctrl+c")
	h.now = h.now.Add(4 * time.Second)
	h.key("ctrl+c")
	if h.quit {
		t.Fatal("a ctrl+c 4 s after the first quit")
	}
	mustContain(t, h.view(), quitNote)
	h.key("ctrl+c")
	if !h.quit {
		t.Error("ctrl+c twice after the reminder did not quit")
	}

	// q from the chat list asks too.
	h = opened(t, fixture(), 90, 20)
	h.typeText("x")
	h.key("esc")
	h.key("q")
	if h.quit {
		t.Fatal("q with a draft quit at once")
	}
	h.key("q")
	if !h.quit {
		t.Error("the second q did not quit")
	}
}

func TestCtrlCWithoutDraftsQuits(t *testing.T) {
	h := opened(t, fixture(), 90, 20)
	h.key("ctrl+c")
	if !h.quit {
		t.Error("ctrl+c with no drafts did not quit")
	}
}

func TestPasteGapZeroLeavesAtOnce(t *testing.T) {
	h := opened(t, fixture(), 90, 20, func(o *Options) { o.PasteGap = -1 })
	h.step = time.Millisecond
	h.key("esc")
	if h.m.focus != focusList || len(h.ticks) != 0 {
		t.Errorf("gap off: esc gave focus %v with ticks %v", h.m.focus, h.ticks)
	}
}

func TestPastedEscWhileDown(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.typeText("keep")
	h.send(backendDownMsg{err: ipc.ErrClosed})
	burst(h, "esc", "q")
	inInput(t, h, "keep")
	burst(h, "esc")
	if h.m.focus != focusList {
		t.Errorf("a lone esc while down: focus %v", h.m.focus)
	}
}

func TestInputFocusWithNoInputSwallowsKeys(t *testing.T) {
	f := fixture()
	f.convs = nil
	h := started(t, f, 90, 20)
	h.m.focus = focusInput
	h.key("q")
	if h.quit || h.m.focus != focusList {
		t.Fatalf("q with focus on a missing input: quit %v focus %v", h.quit, h.m.focus)
	}
	h.key("q")
	if !h.quit {
		t.Error("q on the chat list did not quit")
	}
}

func TestOverflowResubscribeKeepsCallsOut(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	f.resetCalls()
	h.hold = true
	h.send(tea.BlurMsg{})
	// The subscription overflowed and came back on the same connection.
	h.send(subscribedMsg{b: f})
	h.hold = false
	held := h.held
	h.held = nil
	for _, c := range held {
		h.run(c)
	}
	if got := f.writes("setfocus "); !slices.Equal(got, []string{"setfocus r-platform false"}) {
		t.Errorf("setfocus = %q, want one call: the connection already knows", got)
	}
}

const quitNote = "ctrl+c again to quit — unsent drafts will be lost"
