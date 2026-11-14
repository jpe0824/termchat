package tui

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/jpe0824/termchat/internal/ipc"
)

// pasteKeys sends s as a terminal without bracketed paste does: one key
// per character, close together.
func pasteKeys(h *tester, s string) {
	h.t.Helper()
	step := h.step
	h.now = h.now.Add(time.Second) // a paste starts after a pause
	h.step = time.Millisecond
	h.typeText(s)
	h.step = step
}

var pasteWidths = []int{90, 44, 30}

func TestUnbracketedPasteWithTabStaysInInput(t *testing.T) {
	for _, w := range pasteWidths {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			f := fixture()
			h := opened(t, f, w, 20)
			f.resetCalls()
			pasteKeys(h, "see:\n\tq := x\n")
			if h.quit {
				t.Fatal("a pasted q quit")
			}
			if h.m.focus != focusInput || h.m.help {
				t.Errorf("focus %v help %v", h.m.focus, h.m.help)
			}
			h.settle()
			if got := f.writes("send ", "edit ", "delete ", "retry ", "discard "); !slices.Equal(got,
				[]string{`send r-platform "see:\n\tq := x" tui`}) {
				t.Errorf("writes = %q", got)
			}
			if h.draft() != "" {
				t.Errorf("draft = %q", h.draft())
			}
		})
	}
}

func TestUnbracketedPasteCannotMoveTheList(t *testing.T) {
	for _, w := range pasteWidths {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			f := fixture()
			h := opened(t, f, w, 20)
			f.resetCalls()
			pasteKeys(h, "\tjj\nhello\n")
			if h.m.openID != "r-platform" || h.m.cursorID() != "r-platform" || h.m.listShown() {
				t.Errorf("open %q cursor %q list %v", h.m.openID, h.m.cursorID(), h.m.listShown())
			}
			h.settle()
			if got := f.writes("send ", "edit ", "delete ", "retry ", "discard "); !slices.Equal(got,
				[]string{`send r-platform "\tjj\nhello" tui`}) {
				t.Errorf("writes = %q", got)
			}
			if h.draft() != "" || h.m.draftText("r-random") != "" || h.m.draftText("dm-dana") != "" {
				t.Errorf("drafts: here %q, elsewhere %q %q", h.draft(), h.m.draftText("r-random"), h.m.draftText("dm-dana"))
			}
		})
	}
}

func TestUnbracketedPasteInEditCannotDelete(t *testing.T) {
	for _, w := range pasteWidths {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			f := fixture()
			h := opened(t, f, w, 20)
			h.key("up")
			h.key("up") // "on it"
			h.key("e")
			f.resetCalls()
			pasteKeys(h, "\t\tdynamic")
			h.settle()
			if got := f.writes("send ", "edit ", "delete ", "retry ", "discard "); got != nil {
				t.Errorf("writes = %q", got)
			}
			if h.draft() != "on it\t\tdynamic" || h.m.focus != focusInput {
				t.Errorf("draft %q focus %v", h.draft(), h.m.focus)
			}
		})
	}
}

func TestEscInsideABurstStaysInInput(t *testing.T) {
	for _, w := range pasteWidths {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			f := fixture()
			h := opened(t, f, w, 20)
			f.resetCalls()
			pasteKeys(h, "ab\x1bcd")
			if h.m.focus != focusInput || h.m.listShown() || h.draft() != "abcd" {
				t.Errorf("focus %v list %v draft %q", h.m.focus, h.m.listShown(), h.draft())
			}
			// Behind a waiting enter, too.
			pasteKeys(h, "e\n\x1b")
			h.settle()
			if h.m.focus != focusInput || h.m.listShown() || h.draft() != "abcde\n" {
				t.Errorf("esc behind enter: focus %v list %v draft %q", h.m.focus, h.m.listShown(), h.draft())
			}
			if got := f.writes("send ", "edit ", "delete ", "retry ", "discard ", "markread "); got != nil {
				t.Errorf("writes = %q", got)
			}
			// A lone esc still leaves.
			h.key("esc")
			if h.m.focus == focusInput && !h.m.listShown() {
				t.Error("a lone esc did not leave the input")
			}
		})
	}
}

func TestUpInsideABurstDoesNotSelect(t *testing.T) {
	h := opened(t, fixture(), 90, 20)
	pasteKeys(h, "a")
	h.step = time.Millisecond
	h.key("backspace")
	h.key("up")
	if h.m.focus != focusInput || h.m.thread.sel != "" {
		t.Errorf("up inside a burst: focus %v sel %q", h.m.focus, h.m.thread.sel)
	}
}

func TestTabInInputTypesATab(t *testing.T) {
	for _, w := range pasteWidths {
		h := opened(t, fixture(), w, 20)
		h.typeText("a")
		h.key("tab")
		if h.m.focus != focusInput || h.draft() != "a\t" {
			t.Errorf("width %d: tab gave focus %v draft %q", w, h.m.focus, h.draft())
		}
	}
	h := opened(t, fixture(), 90, 20)
	mustNotContain(t, h.lines()[19], "tab")
	h.key("esc")
	h.key("tab")
	h.key("tab")
	if h.m.focus != focusList {
		t.Errorf("tab from the list twice = %v, want back on the list", h.m.focus)
	}
}

// --- typing while the background process is down ---

func TestTypingWhileDownRunsNoCommands(t *testing.T) {
	f := fixture()
	starts := 0
	h := opened(t, f, 120, 20, func(o *Options) {
		o.Start = func(context.Context) (Backend, error) { starts++; return f, nil }
	})
	h.typeText("keep")
	h.send(backendDownMsg{err: ipc.ErrClosed})
	for _, k := range []string{"q", "?", "j", "tab"} {
		h.key(k)
		if h.quit || h.m.help || h.m.focus != focusInput {
			t.Fatalf("%s while down: quit %v help %v focus %v", k, h.quit, h.m.help, h.m.focus)
		}
	}
	h.typeText("quick? just type a whole line, ok")
	if h.quit || h.m.help || h.m.focus != focusInput || h.draft() != "keep" || starts != 0 {
		t.Fatalf("sentence while down: quit %v help %v focus %v draft %q starts %d",
			h.quit, h.m.help, h.m.focus, h.draft(), starts)
	}
	mustContain(t, h.lines()[inputRow(h)-1], "background process not running — r to start, esc to leave the input")
	h.key("r")
	if starts != 1 {
		t.Errorf("r while down started %d times", starts)
	}
	h.key("esc")
	if h.m.focus != focusList {
		t.Fatalf("esc while down: focus %v", h.m.focus)
	}
	h.key("q") // a draft is unsent: asked once more
	h.key("q")
	if !h.quit {
		t.Error("q twice on the list while down did not quit")
	}
	if h.m.draftText("r-platform") != "keep" {
		t.Errorf("draft = %q", h.m.draftText("r-platform"))
	}
}

// --- paste gap setting ---

func TestPasteGapOption(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20, func(o *Options) { o.PasteGap = -1 })
	h.typeText("now")
	h.key("enter")
	if got := f.sends(); !slices.Equal(got, []string{`send r-platform "now" tui`}) {
		t.Errorf("gap off: enter sent %q", got)
	}
	h = opened(t, fixture(), 90, 20, func(o *Options) { o.PasteGap = 80 * time.Millisecond })
	h.typeText("x")
	h.ticks = nil
	h.step = time.Millisecond
	h.key("enter")
	if len(h.ticks) != 1 || h.ticks[0].d != 80*time.Millisecond {
		t.Errorf("enter waits %v, want 80ms", h.ticks)
	}
}

// --- send feedback ---

func TestEnterWhileSendingSaysSo(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.typeText("hello")
	h.hold = true
	h.enter()
	h.enter()
	mustContain(t, h.view(), "still sending…")
	h.key("home")
	h.typeText("X")
	h.hold = false
	held := h.held
	h.held = nil
	for _, c := range held {
		h.run(c)
	}
	if len(f.sends()) != 1 || h.draft() != "Xhello" {
		t.Fatalf("sends %q draft %q", f.sends(), h.draft())
	}
	mustContain(t, h.view(), "sent — draft kept")
}

// --- previews ---

func TestPasteOpensAPreview(t *testing.T) {
	f := fixture()
	h := newTester(t, f, markReadOn)
	h.size(90, 20)
	h.paste("hi")
	if h.m.preview || h.m.focus != focusInput {
		t.Fatalf("paste into a preview: preview %v focus %v", h.m.preview, h.m.focus)
	}
	if got := f.writes("markread "); !slices.Equal(got, []string{"markread r-platform"}) {
		t.Errorf("marks = %q", got)
	}
}

// --- sizes and reconnects ---

func TestEverySizeWithDraftsFits(t *testing.T) {
	sizes := [][2]int{{0, 0}, {1, 1}, {20, 5}}
	for _, w := range []int{35, 36, 69, 70, 109, 110} {
		sizes = append(sizes, [2]int{w, 5}, [2]int{w, 14})
	}
	for _, rows := range []int{1, 5} {
		for _, s := range sizes {
			h := opened(t, fixture(), 90, 20)
			for i := range rows {
				if i > 0 {
					h.key("alt+enter")
				}
				h.typeText("日本語 and 👍🏽 text")
			}
			h.size(s[0], s[1])
			v := h.m.View()
			for i, l := range h.lines() {
				if lipgloss.Width(l) > s[0] {
					t.Errorf("%dx%d, %d-row draft: line %d is %d wide", s[0], s[1], rows, i, lipgloss.Width(l))
				}
			}
			if c := v.Cursor; c != nil && (c.X < 0 || c.X >= s[0] || c.Y < 0 || c.Y >= s[1]) {
				t.Errorf("%dx%d: cursor %+v outside the window", s[0], s[1], c.Position)
			}
		}
	}
}

func TestReconnectClearsStuckFocusAndReadCalls(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20, markReadOn)
	// A MarkRead and a SetFocus go out on the old connection and never
	// come back.
	h.hold = true
	f.addStored("r-platform", msg("r-platform", 7, 50, "dana", "new"))
	msgs, _ := f.Messages("r-platform", 0, 50)
	h.send(threadMsg{b: f, conv: "r-platform", limit: 50, msgs: msgs})
	h.send(tea.BlurMsg{})
	if len(h.held) != 2 {
		t.Fatalf("held %d calls, want a MarkRead and a SetFocus", len(h.held))
	}
	h.held, h.hold = nil, false
	f2 := fixture()
	f2.addStored("r-platform", msg("r-platform", 7, 50, "dana", "new"), msg("r-platform", 8, 51, "dana", "newer"))
	h.send(subscribedMsg{b: f2})
	h.send(tea.FocusMsg{})
	if got := f2.writes("setfocus "); !slices.Equal(got, []string{"setfocus r-platform false", "setfocus r-platform true"}) {
		t.Errorf("after reconnect setfocus = %q", got)
	}
	if got := f2.writes("markread "); !slices.Equal(got, []string{"markread r-platform"}) {
		t.Errorf("after reconnect markread = %q", got)
	}
	// A late answer from the old connection does not free the new one's.
	h.hold = true
	h.send(tea.BlurMsg{})
	h.send(focusSetMsg{b: f})
	if !h.m.focusBusy {
		t.Error("a stale SetFocus answer cleared the current call's flag")
	}
	f2.addStored("r-platform", msg("r-platform", 9, 52, "dana", "more"))
	h.send(tea.FocusMsg{})
	msgs, _ = f2.Messages("r-platform", 0, 50)
	h.m.focusBusy = false
	h.send(threadMsg{b: f2, conv: "r-platform", limit: 50, msgs: msgs})
	h.send(markedMsg{b: f, conv: "r-platform"})
	if !h.m.readBusy["r-platform"] {
		t.Error("a stale MarkRead answer cleared the current call's flag")
	}
}
