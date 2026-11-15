package tui

import (
	"fmt"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// unknownFocus opens r-platform with mark_read on and no focus event yet,
// and returns a way to make a message arrive and a count of marks.
func unknownFocus(t *testing.T) (h *tester, f *fakeBackend, arrive func(int64), marks func() int) {
	t.Helper()
	f = fixture()
	h = opened(t, f, 90, 20, markReadOn)
	arrive = func(seq int64) {
		t.Helper()
		f.addStored("r-platform", msg("r-platform", seq, 50, "dana", fmt.Sprint("new ", seq)))
		h.send(resyncMsg{})
	}
	marks = func() int { return len(f.writes("markread ")) }
	return h, f, arrive, marks
}

func TestUnknownFocusReportedAsNotFocused(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20, markReadOn)
	if got := f.writes("markread "); !slices.Equal(got, []string{"markread r-platform"}) {
		t.Fatalf("opening under unknown focus: markread = %q", got)
	}
	if got := f.writes("setfocus "); !slices.Equal(got, []string{"setfocus r-platform false"}) {
		t.Fatalf("unknown focus: setfocus = %q", got)
	}
	f2 := fixture()
	h.send(subscribedMsg{b: f2})
	if got := f2.writes("setfocus "); !slices.Equal(got, []string{"setfocus r-platform false"}) {
		t.Errorf("unknown focus after reconnect: setfocus = %q", got)
	}
}

func TestUnknownFocusMarksReadOnNextInput(t *testing.T) {
	h, _, arrive, marks := unknownFocus(t)
	base := marks()
	steps := []struct {
		name  string
		input func()
	}{
		{"key", func() { h.key("end") }},
		{"paste", func() { h.paste("x") }},
		{"click", func() { h.click(40, 8) }},
		{"wheel", func() { h.wheel(40, 8, false) }},
	}
	for i, s := range steps {
		arrive(int64(31 + i))
		if got := marks(); got != base+i {
			t.Fatalf("%s: marked read before any input: %d marks, want %d", s.name, got, base+i)
		}
		s.input()
		if got := marks(); got != base+i+1 {
			t.Fatalf("%s: %d marks after input, want %d", s.name, got, base+i+1)
		}
	}
}

func TestUnknownFocusInputBeforeArrivalDoesNotCount(t *testing.T) {
	h, _, arrive, marks := unknownFocus(t)
	base := marks()
	h.key("end")
	arrive(31)
	h.send(resyncMsg{})
	if got := marks(); got != base {
		t.Errorf("input before the message marked it read: %d marks, want %d", got, base)
	}
}

func TestBlurAfterUnknownIgnoresInput(t *testing.T) {
	h, f, arrive, marks := unknownFocus(t)
	base := marks()
	h.send(tea.BlurMsg{})
	arrive(31)
	h.key("end")
	if got := marks(); got != base {
		t.Errorf("blurred terminal marked read on input: %d marks, want %d", got, base)
	}
	if got := f.writes("setfocus "); !slices.Equal(got, []string{"setfocus r-platform false"}) {
		t.Errorf("blurred: setfocus = %q", got)
	}
	h.send(tea.FocusMsg{})
	if got := marks(); got != base+1 {
		t.Errorf("focus back: %d marks, want %d", got, base+1)
	}
	if got := f.writes("setfocus "); !slices.Equal(got, []string{"setfocus r-platform false", "setfocus r-platform true"}) {
		t.Errorf("focus back: setfocus = %q", got)
	}
}
