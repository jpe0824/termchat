package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/jpe0824/termchat/internal/ipc"
)

func TestTinyWindowDoesNotPanic(t *testing.T) {
	sizes := [][2]int{{20, 5}, {1, 1}, {0, 0}, {0, 30}, {120, 0}, {2, 40}, {19, 5}, {20, 4}, {36, 5}, {110, 5}, {70, 6}}
	for _, splash := range []bool{false, true} {
		for _, s := range sizes {
			h := newTester(t, fixture(), func(o *Options) { o.Splash = splash })
			h.size(s[0], s[1])
			v := h.view()
			if s[0] < 20 || s[1] < 5 {
				if want := "termchat: window too small"[:min(26, s[0])]; v != want {
					t.Errorf("%dx%d splash=%v: %q, want %q", s[0], s[1], splash, v, want)
				}
				continue
			}
			lines := h.lines()
			if len(lines) != s[1] {
				t.Errorf("%dx%d splash=%v: %d rows", s[0], s[1], splash, len(lines))
			}
			for _, l := range lines {
				if lipgloss.Width(l) > s[0] {
					t.Errorf("%dx%d splash=%v: row %q too wide", s[0], s[1], splash, l)
				}
			}
			for _, k := range []string{"esc", "?", "pgup", "home", "?", "esc", "enter"} {
				h.key(k)
			}
			h.click(0, 0)
			h.wheel(1, 1, true)
		}
	}
}

func TestWideCharsWrap(t *testing.T) {
	f := fixture()
	f.outbox = nil
	cjk := strings.Repeat("漢字テスト", 12)
	emoji := strings.Repeat("🙂👍🏽👨‍👩‍👧", 10)
	f.setStored("r-platform",
		msg("r-platform", 1, 1, "dana", cjk),
		msg("r-platform", 2, 2, "名前の長い人", emoji),
		msg("r-platform", 3, 3, "sam", "a"+cjk))
	f.convs = append(f.convs, room("r-cjk", "部屋の名前がとても長い部屋", false, "dana", nil, 9, -500))
	for _, w := range []int{120, 90, 71, 50, 37, 36, 35, 25, 20} {
		h := started(t, f, w, 30)
		for i, l := range h.lines() {
			if lw := lipgloss.Width(l); lw != w {
				t.Errorf("width %d row %d is %d cells: %q", w, i, lw, l)
			}
		}
		if w >= 36 {
			mustContain(t, h.view(), "漢字")
		}
	}
	h := started(t, f, 120, 30)
	_, l := lineWith(h.lines(), "部屋")
	if !strings.Contains(l, "…") {
		t.Errorf("wide room name should be cut with …: %q", l)
	}
}

func TestWrapCells(t *testing.T) {
	for _, c := range []struct {
		in    string
		width int
		want  []string
	}{
		{"hello world", 5, []string{"hello", "world"}},
		{"hello world", 11, []string{"hello world"}},
		{"abcdefgh", 3, []string{"abc", "def", "gh"}},
		{"漢字漢字", 5, []string{"漢字", "漢字"}},
		{"漢字", 1, []string{"?", "?"}},
		{"a\nb", 10, []string{"a", "b"}},
		{"", 10, []string{""}},
		{"x", 0, []string{"x"}},
	} {
		got := wrap(c.in, c.width)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("wrap(%q, %d) = %q, want %q", c.in, c.width, got, c.want)
		}
	}
}

func TestTruncateCells(t *testing.T) {
	for _, c := range []struct {
		in    string
		width int
		want  string
	}{
		{"platform", 8, "platform"},
		{"platform", 5, "plat…"},
		{"漢字漢字", 5, "漢字…"},
		{"漢字漢字", 4, "漢…"},
		{"abc", 1, "…"},
		{"abc", 0, ""},
	} {
		if got := truncate(c.in, c.width); got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.in, c.width, got, c.want)
		}
	}
}

func TestHugeThreadAtTinySize(t *testing.T) {
	f := fixture()
	f.setStored("r-platform", numbered("r-platform", 1, 50)...)
	f.outbox["r-platform"][0].Status = ipc.MsgFailed
	h := started(t, f, 20, 5)
	lines := h.lines()
	if len(lines) != 5 || !strings.HasPrefix(lines[4], "│ > ") {
		t.Errorf("20x5 frame:\n%s", h.view())
	}
}
