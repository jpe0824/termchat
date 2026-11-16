package tui

import (
	"fmt"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// inHerdr runs the interface as if inside a Herdr terminal.
func inHerdr(o *Options) {
	o.Env = func(k string) string {
		return map[string]string{"NO_COLOR": "1", "HC_TERMINAL_ID": "t1"}[k]
	}
}

// Herdr reports focus in when a pane is clicked but no focus out when the
// person switches to another app, so a focus report there cannot be trusted:
// the interface treats focus as unknown.
func TestHerdrFocusInIsIgnored(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20, markReadOn, inHerdr)
	h.send(tea.FocusMsg{})
	if got := f.writes("setfocus "); !slices.Equal(got, []string{"setfocus r-platform false"}) {
		t.Errorf("after focus in under Herdr: setfocus = %q, want only not focused", got)
	}
	base := len(f.writes("markread "))
	f.addStored("r-platform", msg("r-platform", 31, 50, "dana", fmt.Sprint("new ", 31)))
	h.send(resyncMsg{})
	if got := len(f.writes("markread ")); got != base {
		t.Fatalf("message marked read with no input after focus in: %d marks, want %d", got, base)
	}
	h.key("end")
	if got := len(f.writes("markread ")); got != base+1 {
		t.Errorf("after a key: %d marks, want %d", got, base+1)
	}
}

func TestFocusInOutsideHerdrStillCounts(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20, markReadOn)
	h.send(tea.FocusMsg{})
	if got := f.writes("setfocus "); !slices.Contains(got, "setfocus r-platform true") {
		t.Errorf("focus in outside Herdr: setfocus = %q, want a focused report", got)
	}
}
