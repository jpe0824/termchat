package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jpe0824/termchat/internal/ipc"
)

// threadCol is the trimmed thread-pane text of each frame row, in the
// standard and wide layouts; "" for rows without one.
func threadCol(h *tester) []string {
	var out []string
	for _, l := range h.lines() {
		parts := strings.Split(l, "│")
		s := ""
		if len(parts) > 2 {
			s = strings.TrimSpace(parts[2])
		}
		out = append(out, s)
	}
	return out
}

func isRule(s string) bool { return s != "" && strings.Trim(s, "─") == "" }

func TestInputAreaRows(t *testing.T) {
	h := opened(t, fixture(), 90, 20)
	col := threadCol(h)
	in := inputRow(h)
	if in != 15 {
		t.Fatalf("input row %d, want 15:\n%s", in, h.view())
	}
	if !isRule(col[in-1]) || !isRule(col[in+1]) {
		t.Errorf("rules around the input: %q %q", col[in-1], col[in+1])
	}
	if col[in+3] != "" {
		t.Errorf("gap row %q, want empty", col[in+3])
	}
	if l := h.lines()[19]; !strings.HasPrefix(l, "└") {
		t.Errorf("border row %q", l)
	}
}

func TestShortWindowDropsGapThenHints(t *testing.T) {
	for _, c := range []struct {
		h          int
		gap, hints bool
	}{
		{16, true, true}, {15, false, true}, {12, false, true}, {11, false, true}, {10, false, false}, {5, false, false},
	} {
		h := opened(t, fixture(), 90, c.h)
		l := h.m.tlayout(h.m.geom())
		if l.bar != c.hints || l.gap != c.gap || l.hints != c.hints {
			t.Errorf("height %d: bar %v gap %v hints %v, want %v %v %v", c.h, l.bar, l.gap, l.hints, c.hints, c.gap, c.hints)
		}
	}
}

func TestClickOnHintRowsDoesNothing(t *testing.T) {
	h := opened(t, fixture(), 90, 20)
	in := inputRow(h)
	h.key("esc")
	for _, y := range []int{in + 1, in + 2, in + 3} {
		h.click(40, y)
		if h.m.focus != focusThread || h.m.thread.sel != "" {
			t.Errorf("click on row %d: focus %v sel %q", y, h.m.focus, h.m.thread.sel)
		}
	}
}

func TestFullInputKeepsHintRow(t *testing.T) {
	f := fixture()
	var stored []ipc.Message
	for i := 1; i <= 30; i++ {
		stored = append(stored, msg("r-platform", int64(i), i, "dana", fmt.Sprintf("line %d.", i)))
	}
	f.setStored("r-platform", stored...)
	h := opened(t, f, 90, 20)
	for i := range 7 {
		if i > 0 {
			h.key("alt+enter")
		}
		h.typeText("line" + string(rune('1'+i)))
	}
	col := threadCol(h)
	if got, want := col[11:16], []string{"> line3", "line4", "line5", "line6", "line7"}; !slices.Equal(got, want) {
		t.Errorf("input rows = %q, want %q", got, want)
	}
	if !isRule(col[16]) || col[18] != "" {
		t.Errorf("bar %q, gap %q", col[16], col[18])
	}
}
