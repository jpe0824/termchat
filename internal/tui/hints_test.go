package tui

import (
	"errors"
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
	h.typeText("draft")
	in := inputRow(h)
	for _, y := range []int{in + 1, in + 2, in + 3} {
		h.click(40, y)
		if h.m.focus != focusInput || h.draft() != "draft" {
			t.Errorf("input focused, click on row %d: focus %v draft %q", y, h.m.focus, h.draft())
		}
	}
	_, h = onOwnMessage(t, 90)
	sel := h.m.thread.sel
	for _, y := range []int{in + 1, in + 2, in + 3} {
		h.click(40, y)
		if h.m.focus != focusThread || h.m.thread.sel != sel {
			t.Errorf("message selected, click on row %d: focus %v sel %q, want %q", y, h.m.focus, h.m.thread.sel, sel)
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

// hintRowOf is the hint row's text, for a one-row input.
func hintRowOf(h *tester) string { return threadCol(h)[inputRow(h)+2] }

func TestHintSplit(t *testing.T) {
	h := opened(t, fixture(), 90, 20)
	if got, want := hintRowOf(h), "⏎ send  alt+⏎ newline  ↑ select  esc leave"; got != want {
		t.Errorf("input hints %q, want %q", got, want)
	}
	foot := footerOf(h)
	mustContain(t, foot, "ctrl+k jump", "ctrl+n new", "ctrl+o room", "ctrl+c quit")
	mustNotContain(t, foot, "⏎ send", "tab pane", "/ search", "? help")

	h.key("esc") // leaves the input for the chat list
	if got := hintRowOf(h); got != "" {
		t.Errorf("list focused: hint row %q, want empty", got)
	}
	want := "└─ ↑↓ move  ⏎ open  tab pane  / search  ctrl+k jump  ctrl+n new  ? help  q quit ─────────┘"
	if got := footerOf(h); got != want {
		t.Errorf("list footer\n got %q\nwant %q", got, want)
	}

	h.key("tab")
	if got, want := hintRowOf(h), "i type  ↑↓ scroll"; got != want {
		t.Errorf("thread hints %q, want %q", got, want)
	}
	foot = footerOf(h)
	mustContain(t, foot, "tab pane", "/ search", "? help", "q quit")
	mustNotContain(t, foot, "i type")

	_, h = onOwnMessage(t, 90)
	if got, want := hintRowOf(h), "e edit  d delete  i type  ↑↓ select  esc back"; got != want {
		t.Errorf("selection hints %q, want %q", got, want)
	}

	h = opened(t, fixture(), 50, 20)
	if got, want := strings.Trim(strings.Split(h.lines()[inputRow(h)+2], "│")[1], " "), "⏎ send  alt+⏎ newline"; got != want {
		t.Errorf("narrow input hints %q, want %q", got, want)
	}
	mustContain(t, footerOf(h), "esc chats", "ctrl+c quit")
}

func TestFitKeysDropsWholeLabels(t *testing.T) {
	keys := []string{"⏎ send", "alt+⏎ newline", "↑ select", "esc leave"}
	for _, c := range []struct {
		w    int
		want string
	}{
		{30, "⏎ send  alt+⏎ newline"},
		{5, ""},
		{100, "⏎ send  alt+⏎ newline  ↑ select  esc leave"},
	} {
		if got := fitKeys(keys, c.w); got != c.want {
			t.Errorf("fitKeys(%d) = %q, want %q", c.w, got, c.want)
		}
	}
}

func TestHintRowWhileDown(t *testing.T) {
	st := &starter{err: errors.New("boom")}
	h := opened(t, fixture(), 90, 20, st.opt)
	h.key("up")
	h.key("up")
	h.send(backendDownMsg{err: ipc.ErrClosed})
	mustContain(t, hintRowOf(h), "↑↓ select")
	mustNotContain(t, hintRowOf(h), "i type", "e edit", "d delete")
	mustContain(t, footerOf(h), "r start")
}

func TestBannerStaysOnBorder(t *testing.T) {
	h := opened(t, fixture(), 90, 20)
	h.typeText("x")
	h.key("ctrl+c")
	if foot := footerOf(h); !strings.HasPrefix(foot, "└─ ") || !strings.Contains(foot, "ctrl+c again to quit") {
		t.Errorf("border row %q, want the quit warning", foot)
	}
	if got, want := hintRowOf(h), "⏎ send  alt+⏎ newline  ↑ select  esc leave"; got != want {
		t.Errorf("hint row %q, want %q", got, want)
	}
}
