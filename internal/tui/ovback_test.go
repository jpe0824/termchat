package tui

import (
	"fmt"
	"testing"
)

// esc in a form returns to the choice list with the choice just left still
// selected.
func TestBackFromAFormSelectsTheChoiceLeft(t *testing.T) {
	for i, step := range []ovStep{stepDM, stepRoom, stepJoin} {
		t.Run(fmt.Sprint(step), func(t *testing.T) {
			h := started(t, peopleFixture(), 90, 24)
			h.key("ctrl+n")
			for range i {
				h.key("down")
			}
			h.enter()
			if ovState(h) != fmt.Sprintf("%d/%d", ovNew, step) {
				t.Fatalf("after enter: %q", ovState(h))
			}
			h.key("esc")
			if ovState(h) != fmt.Sprintf("%d/%d", ovNew, stepChoose) {
				t.Fatalf("after esc: %q", ovState(h))
			}
			if h.m.ov.sel != i {
				t.Fatalf("selection %d after esc, want %d", h.m.ov.sel, i)
			}
		})
	}
}
