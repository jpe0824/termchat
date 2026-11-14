package tui

import (
	"slices"
	"testing"
)

// The conversation shown at launch is a preview: shown, not read, and not
// holding the keyboard.

func TestLaunchPreviewIsNotOpened(t *testing.T) {
	f := fixture()
	h := newTester(t, f, markReadOn)
	h.size(90, 20)
	if got := f.writes("markread "); got != nil {
		t.Errorf("launch marked read: %q", got)
	}
	mustContain(t, h.view(), "● # platform     5", "did the meter import")
	if h.m.focus != focusList || h.m.cursorID() != "r-platform" {
		t.Errorf("launch focus %v cursor %q, want the chat list on # platform", h.m.focus, h.m.cursorID())
	}
	h.key("enter")
	if h.m.focus != focusInput {
		t.Errorf("enter on the row: focus %v", h.m.focus)
	}
	if got := f.writes("markread "); !slices.Equal(got, []string{"markread r-platform"}) {
		t.Errorf("enter on the row marked %q", got)
	}
}

func TestQAndHelpWorkAtLaunch(t *testing.T) {
	for _, w := range []int{120, 90, 44, 30} {
		h := newTester(t, fixture(), markReadOn)
		h.size(w, 14)
		h.key("?")
		if !h.m.help {
			t.Errorf("width %d: ? at launch did not show help", w)
		}
		h.key("?")
		h.key("q")
		if !h.quit {
			t.Errorf("width %d: q at launch did not quit", w)
		}
	}
}

func TestLaunchPreviewNarrow(t *testing.T) {
	f := fixture()
	h := newTester(t, f, markReadOn)
	h.size(44, 14)
	if h.m.listShown() || h.m.focus != focusThread {
		t.Fatalf("narrow launch: list %v focus %v, want the thread with focus", h.m.listShown(), h.m.focus)
	}
	mustContain(t, h.view(), "on it")
	if f.writes("markread ") != nil {
		t.Errorf("narrow launch marked read")
	}
	h.typeText("h")
	if h.m.focus != focusInput || h.draft() != "h" {
		t.Errorf("typing on the preview: focus %v draft %q", h.m.focus, h.draft())
	}
	if got := f.writes("markread "); !slices.Equal(got, []string{"markread r-platform"}) {
		t.Errorf("typing opened it but marked %q", got)
	}
}

func TestClickInThreadOpensPreview(t *testing.T) {
	f := fixture()
	h := newTester(t, f, markReadOn)
	h.size(90, 20)
	h.click(30, h.row("did the meter import"))
	if got := f.writes("markread "); !slices.Equal(got, []string{"markread r-platform"}) {
		t.Errorf("click in the thread marked %q", got)
	}

	f = fixture()
	h = newTester(t, f, markReadOn)
	h.size(90, 20)
	h.click(30, inputRow(h))
	if h.m.focus != focusInput || len(f.writes("markread ")) != 1 {
		t.Errorf("click in the input: focus %v marks %q", h.m.focus, f.writes("markread "))
	}
}

func TestScrollingOpensPreview(t *testing.T) {
	f := fixture()
	h := newTester(t, f, markReadOn)
	h.size(90, 12)
	h.key("pgup")
	if got := f.writes("markread "); !slices.Equal(got, []string{"markread r-platform"}) {
		t.Errorf("scrolling the preview marked %q", got)
	}
}

func TestReconnectKeepsPreviewAndOpened(t *testing.T) {
	f := fixture()
	h := newTester(t, f, markReadOn)
	h.size(90, 20)
	h.send(subscribedMsg{b: f})
	if f.writes("markread ") != nil || h.m.focus != focusList {
		t.Errorf("reconnect on a preview: marks %q focus %v", f.writes("markread "), h.m.focus)
	}
	h.key("enter")
	h.send(subscribedMsg{b: f})
	if h.m.focus != focusInput || len(f.writes("markread ")) != 1 {
		t.Errorf("reconnect on an opened chat: focus %v marks %q", h.m.focus, f.writes("markread "))
	}
	h.typeText("x")
	if h.draft() != "x" {
		t.Errorf("after reconnect the input lost focus: draft %q", h.draft())
	}
}
