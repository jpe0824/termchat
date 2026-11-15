package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
)

// --- focus and keys ---

func TestOpeningFocusesInputAndLettersType(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	if h.m.focus != focusInput {
		t.Fatalf("focus after opening = %v", h.m.focus)
	}
	h.typeText("q?rjkedxi")
	if h.quit || h.m.help {
		t.Fatalf("a letter acted as a command: quit %v help %v", h.quit, h.m.help)
	}
	if h.draft() != "q?rjkedxi" {
		t.Errorf("draft = %q", h.draft())
	}
	if got := f.writes("send ", "edit ", "delete ", "retry ", "discard "); got != nil {
		t.Errorf("typing called %q", got)
	}
	if h.m.cursorID() != "r-platform" {
		t.Errorf("typing j/k moved the chat list to %q", h.m.cursorID())
	}
	h.key("ctrl+c") // a draft is unsent: asked once more
	h.key("ctrl+c")
	if !h.quit {
		t.Error("ctrl+c twice did not quit from the input")
	}
}

func TestEscAndTabMoveFocus(t *testing.T) {
	h := opened(t, fixture(), 90, 20)
	h.key("esc")
	if h.m.focus != focusList {
		t.Fatalf("esc from the input = %v, want the chat list", h.m.focus)
	}
	var got []focusArea
	for range 3 {
		h.key("tab")
		got = append(got, h.m.focus)
	}
	if want := []focusArea{focusThread, focusList, focusThread}; !slices.Equal(got, want) {
		t.Errorf("tab cycle = %v, want %v", got, want)
	}
	h.key("i")
	if h.m.focus != focusInput || h.draft() != "" {
		t.Errorf("i on the thread: focus %v draft %q", h.m.focus, h.draft())
	}
	h.key("esc")
	h.key("tab")
	h.key("enter")
	if h.m.focus != focusInput {
		t.Errorf("enter on the thread: focus %v", h.m.focus)
	}
	h.key("esc")
	h.key("q")
	if !h.quit {
		t.Error("q on the chat list did not quit")
	}
}

func TestEscInNarrowGoesToTheList(t *testing.T) {
	h := opened(t, fixture(), 44, 14)
	if h.m.focus != focusInput {
		t.Fatalf("focus = %v", h.m.focus)
	}
	h.key("esc")
	if !h.m.listShown() {
		t.Fatal("esc did not show the chat list")
	}
	h.key("enter")
	if h.m.listShown() || h.m.focus != focusInput {
		t.Errorf("enter: list %v focus %v", h.m.listShown(), h.m.focus)
	}
}

func TestFooterShowsKeysForFocus(t *testing.T) {
	f := failedFixture()
	h := opened(t, f, 120, 20)
	foot := func() string { return h.lines()[19] }
	mustContain(t, hintRowOf(h), "⏎ send", "esc leave")
	mustNotContain(t, foot(), "q quit", "⏎ open")
	h.key("up")
	mustContain(t, h.lines()[17], "r retry", "x discard")
	h.key("up")
	mustContain(t, h.lines()[17], "e edit", "d delete")
	mustNotContain(t, h.lines()[17], "r retry")
	h.key("esc")
	h.key("esc")
	mustContain(t, foot(), "↑↓ move", "⏎ open", "q quit")
}

func TestInputDisabledWhileDown(t *testing.T) {
	f := fixture()
	starts := 0
	h := opened(t, f, 90, 20, func(o *Options) {
		o.Start = func(context.Context) (Backend, error) { starts++; return f, nil }
	})
	h.typeText("keep")
	h.send(backendDownMsg{err: ipc.ErrClosed})
	h.typeText("zz")
	h.enter()
	if h.draft() != "keep" || f.sends() != nil {
		t.Fatalf("while down: draft %q sends %q", h.draft(), f.sends())
	}
	mustContain(t, h.lines()[inputRow(h)-1], "background process not running")
	if h.m.View().Cursor != nil {
		t.Error("a disabled input shows the cursor")
	}
	h.key("r")
	if starts != 1 {
		t.Errorf("r while down started %d times", starts)
	}
	h.send(subscribedMsg{b: f})
	h.typeText("!")
	if h.draft() != "keep!" {
		t.Errorf("after reconnect draft = %q", h.draft())
	}
}

// --- read state ---

func TestMarkReadOnOpenAndNewMessage(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20, markReadOn)
	h.send(tea.FocusMsg{}) // a terminal that reports focus
	if got := f.writes("markread "); !slices.Equal(got, []string{"markread r-platform"}) {
		t.Fatalf("on open: %q", got)
	}
	h.click(4, 2)
	if got := f.writes("markread "); len(got) != 2 || got[1] != "markread dm-dana" {
		t.Fatalf("opening dana: %q", got)
	}
	f.addStored("dm-dana", msg("dm-dana", 3, 46, "dana", "you there?"))
	h.send(resyncMsg{})
	if n := len(f.writes("markread dm-dana")); n != 2 {
		t.Errorf("new message while looking: %d marks, want 2", n)
	}
}

func TestMarkReadOnlyWhenFocused(t *testing.T) {
	f := fixture()
	var stored []ipc.Message
	for i := 1; i <= 30; i++ {
		stored = append(stored, msg("r-platform", int64(i), i, "dana", fmt.Sprint("line ", i)))
	}
	f.setStored("r-platform", stored...)
	h := opened(t, f, 90, 20, markReadOn)
	arrive := func(seq int64) {
		f.addStored("r-platform", msg("r-platform", seq, 50, "dana", fmt.Sprint("new ", seq)))
		h.send(resyncMsg{})
	}
	marks := func() int { return len(f.writes("markread ")) }
	base := marks()
	h.send(tea.BlurMsg{})
	arrive(31)
	if marks() != base {
		t.Fatal("marked read while the terminal was blurred")
	}
	h.send(tea.FocusMsg{})
	if marks() != base+1 {
		t.Errorf("focus back on an unread thread at the bottom: %d marks, want %d", marks(), base+1)
	}
	h.key("pgup")
	arrive(32)
	if marks() != base+1 {
		t.Error("marked read while scrolled up")
	}
	h.size(44, 14)
	h.key("esc")
	h.key("end") // the chat list in narrow: end does nothing to the thread
	arrive(33)
	if marks() != base+1 {
		t.Error("marked read with the chat list shown")
	}
}

func TestMarkReadOffNeverCalled(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.click(4, 2)
	f.addStored("dm-dana", msg("dm-dana", 3, 46, "dana", "x"))
	h.send(resyncMsg{})
	h.send(tea.FocusMsg{})
	if got := f.writes("markread "); got != nil {
		t.Errorf("mark_read off, yet %q", got)
	}
}

func TestMarkReadCoalesces(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20, markReadOn)
	h.send(tea.FocusMsg{}) // a terminal that reports focus
	f.resetCalls()
	h.hold = true
	for seq := int64(7); seq <= 9; seq++ {
		f.addStored("r-platform", msg("r-platform", seq, 50, "dana", "burst"))
		msgs, _ := f.Messages("r-platform", 0, 50)
		h.send(threadMsg{b: f, conv: "r-platform", limit: 50, msgs: msgs})
	}
	h.hold = false
	held := h.held
	h.held = nil
	for _, c := range held {
		h.run(c)
	}
	if n := len(f.writes("markread ")); n != 2 {
		t.Errorf("a burst of three made %d MarkRead calls, want 2 (one out, one queued)", n)
	}
}

// --- focus reporting ---

func TestSetFocusReported(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.send(tea.FocusMsg{}) // a terminal that reports focus
	h.click(4, 2)          // open dana
	h.send(tea.BlurMsg{})  // terminal loses focus
	h.send(tea.FocusMsg{}) // and gets it back
	h.size(44, 14)         // narrow: still the thread
	h.key("esc")           // the chat list: no conversation shown
	h.key("enter")         // dana again
	f2 := fixture()
	h.send(subscribedMsg{b: f2}) // reconnected: say it again
	h.key("ctrl+c")
	if got := f2.writes("setfocus "); !slices.Equal(got, []string{"setfocus dm-dana true", "setfocus dm-dana false"}) {
		t.Errorf("setfocus on the new connection = %q", got)
	}
	want := []string{
		"setfocus r-platform false",
		"setfocus r-platform true",
		"setfocus dm-dana true",
		"setfocus dm-dana false",
		"setfocus dm-dana true",
		"setfocus  true",
		"setfocus dm-dana true",
	}
	if got := f.writes("setfocus "); !slices.Equal(got, want) {
		t.Errorf("setfocus calls:\n got %q\nwant %q", got, want)
	}
}

func TestSetFocusFailureIgnored(t *testing.T) {
	f := fixture()
	f.failWith("setfocus", errors.New("boom"))
	h := opened(t, f, 90, 20)
	h.click(4, 2)
	mustNotContain(t, h.view(), "boom")
	if n := len(f.writes("setfocus ")); n != 2 {
		t.Errorf("setfocus calls = %d, want 2 and no retries", n)
	}
}

func TestLockKeysStillType(t *testing.T) {
	h := opened(t, fixture(), 90, 20)
	h.send(tea.KeyPressMsg{Code: 'A', Text: "A", Mod: tea.ModShift | tea.ModCapsLock | tea.ModNumLock})
	h.send(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	if h.draft() != "A" {
		t.Errorf("draft = %q", h.draft())
	}
}
