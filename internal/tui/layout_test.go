package tui

import (
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/jpe0824/termchat/internal/ipc"
)

func TestLayoutModeBreakpoints(t *testing.T) {
	for _, c := range []struct {
		w    int
		want Mode
	}{{200, Wide}, {110, Wide}, {109, Standard}, {70, Standard}, {69, Narrow}, {36, Narrow}, {35, Tiny}, {0, Tiny}} {
		if got := LayoutMode(c.w); got != c.want {
			t.Errorf("LayoutMode(%d) = %v, want %v", c.w, got, c.want)
		}
	}
}

func TestWideShowsMembersPanel(t *testing.T) {
	f := fixture()
	h := started(t, f, 120, 24)
	v := h.view()
	mustContain(t, v, "Members (4)", "jason (you)", "ci-bot [agent]", "Open room", "created by dana")
	// The panel lists members, not presence: no dot before a handle.
	_, l := lineWith(h.lines(), "Members (4)")
	if !strings.HasSuffix(strings.TrimRight(l, " │"), "Members (4)") {
		t.Errorf("members heading not in the right-hand panel: %q", l)
	}
	mustNotContain(t, v, "● sam ")

	// A DM has no members panel; the thread takes the space.
	h.click(5, 2) // "dana" in the sidebar
	if h.m.openID != "dm-dana" {
		t.Fatalf("open = %q, want dm-dana", h.m.openID)
	}
	mustNotContain(t, h.view(), "Members (")
	sep := h.lines()[2] // the rule under the thread header
	if w := lipgloss.Width(strings.TrimSpace(strings.Split(sep, "│")[2])); w != 120-25 {
		t.Errorf("DM thread separator is %d cells, want the width beside the sidebar", w)
	}
}

func TestPrivateRoomInMembersPanel(t *testing.T) {
	f := fixture()
	h := started(t, f, 120, 24)
	h.click(5, 7) // "# deploys"
	if h.m.openID != "r-deploys" {
		t.Fatalf("open = %q, want r-deploys", h.m.openID)
	}
	mustContain(t, h.view(), "Members (2)", "Private room", "created by jason")
}

func TestStandardHeaderShowsMemberCount(t *testing.T) {
	h := started(t, fixture(), 90, 24)
	_, l := lineWith(h.lines(), "# platform")
	if !strings.Contains(l, "4 members") {
		t.Errorf("standard header = %q, want the member count", l)
	}
	mustNotContain(t, h.view(), "Members (")
	mustContain(t, h.view(), "Direct", "Rooms")
}

func TestNarrowOpensOnThread(t *testing.T) {
	f := fixture()
	f.convs[2].Unread = 7 // dana
	h := started(t, f, 60, 20)
	v := h.view()
	_, hdr := lineWith(h.lines(), "# platform")
	if !strings.Contains(hdr, "‹ chats ●7") {
		t.Errorf("narrow header = %q, want the ‹ chats ●7 marker", hdr)
	}
	mustContain(t, v, "did the meter import")
	mustNotContain(t, v, "Direct", "Rooms")

	h.key("esc")
	v = h.view()
	mustContain(t, v, "Direct", "Rooms", "# deploys")
	mustNotContain(t, v, "did the meter import")

	h.key("enter")
	mustContain(t, h.view(), "did the meter import", "‹ chats ●7")
	mustNotContain(t, h.view(), "Rooms")

	// The marker is clickable, and a click on a row opens that thread.
	h.click(3, 1)
	mustContain(t, h.view(), "Rooms")
	h.click(4, 2) // "dana"
	if h.m.openID != "dm-dana" || h.m.showList {
		t.Fatalf("after click open=%q showList=%v", h.m.openID, h.m.showList)
	}
	mustContain(t, h.view(), "got a minute?")
}

func TestNarrowMarkerOmitsZeroUnread(t *testing.T) {
	f := fixture()
	f.convs[2].Unread = 0
	h := started(t, f, 60, 20)
	_, hdr := lineWith(h.lines(), "# platform")
	if !strings.Contains(hdr, "‹ chats") || strings.Contains(hdr, "●") {
		t.Errorf("narrow header = %q, want ‹ chats without a count", hdr)
	}
}

func TestNarrowWithNoChatsShowsList(t *testing.T) {
	f := fixture()
	f.convs = nil
	h := started(t, f, 50, 20)
	mustContain(t, h.view(), "no chats yet")
}

func TestTinyDropsTimestampsAndGroupsAuthor(t *testing.T) {
	f := fixture()
	f.setStored("r-platform",
		msg("r-platform", 1, 2, "dana", "did the meter import finish?"),
		msg("r-platform", 2, 3, "dana", "second"),
		msg("r-platform", 3, 4, "jason", "yes, 10 min ago"),
	)
	f.outbox = nil
	h := started(t, f, 30, 16)
	v := h.view()
	mustNotContain(t, v, "10:02", "10:03")
	_, hdr := lineWith(h.lines(), "# platform")
	if !strings.Contains(hdr, "‹ ●2") || strings.Contains(hdr, "chats") {
		t.Errorf("tiny header = %q, want ‹ ●2", hdr)
	}
	lines := h.lines()
	i, _ := lineWith(lines, "│ dana")
	if i < 0 {
		t.Fatalf("no author line for dana:\n%s", v)
	}
	if !strings.HasPrefix(lines[i+1], "│  did the meter") {
		t.Errorf("text not indented one space under the author: %q", lines[i+1])
	}
	if strings.Count(v, "│ dana") != 1 {
		t.Errorf("consecutive messages by dana should share one author line:\n%s", v)
	}
	mustContain(t, v, "│  second", "│ you ", "│  yes, 10 min ago")
	if !strings.HasSuffix(lines[0], "● ┐") || strings.Contains(lines[0], "connected") {
		t.Errorf("tiny top bar = %q, want only the state glyph", lines[0])
	}
}

func TestShortWindowDropsFooterThenTopBar(t *testing.T) {
	h := started(t, fixture(), 90, 12)
	lines := h.lines()
	if len(lines) != 12 || !strings.Contains(lines[0], "termchat") || !strings.HasPrefix(lines[11], "└─") {
		t.Fatalf("12 rows should have top bar and footer:\n%s", h.view())
	}
	h.size(90, 11)
	lines = h.lines()
	if len(lines) != 11 || !strings.Contains(lines[0], "termchat") || strings.HasPrefix(lines[10], "└─") {
		t.Errorf("11 rows: want top bar, no footer:\n%s", h.view())
	}
	if !strings.Contains(lines[10], "> ") {
		t.Errorf("11 rows: input should be the last line, got %q", lines[10])
	}
	h.size(90, 7)
	lines = h.lines()
	if len(lines) != 7 || strings.Contains(h.view(), "termchat") {
		t.Errorf("7 rows: want no top bar:\n%s", h.view())
	}
	if !strings.Contains(lines[6], "> ") || !strings.Contains(h.view(), "# platform") {
		t.Errorf("7 rows: header and input must stay:\n%s", h.view())
	}
}

func TestTooSmallWindow(t *testing.T) {
	for _, s := range [][2]int{{19, 10}, {40, 4}} {
		h := started(t, fixture(), s[0], s[1])
		if v := h.view(); v != "termchat: window too small"[:min(len("termchat: window too small"), s[0])] {
			t.Errorf("%dx%d frame = %q", s[0], s[1], v)
		}
	}
	h := started(t, fixture(), 20, 5)
	if strings.Contains(h.view(), "too small") || len(h.lines()) != 5 {
		t.Errorf("20x5 is the smallest full frame:\n%s", h.view())
	}
}

func TestResizeAcrossModesKeepsSelection(t *testing.T) {
	f := fixture()
	var many []ipc.Message
	for i := int64(1); i <= 40; i++ {
		many = append(many, msg("dm-dana", i, int(i), "dana", "line "+strconv.FormatInt(i, 10)))
	}
	f.setStored("dm-dana", many...)
	h := started(t, f, 120, 20)
	h.click(5, 2) // dana
	h.key("tab")
	h.key("pgup")
	before := h.m.topKey()
	if before == "" || h.m.openID != "dm-dana" {
		t.Fatalf("setup: top=%q open=%q", before, h.m.openID)
	}
	h.size(50, 20)
	if h.m.openID != "dm-dana" || h.m.topKey() != before {
		t.Errorf("at 50 cols: open=%q top=%q, want dm-dana %q", h.m.openID, h.m.topKey(), before)
	}
	mustContain(t, h.view(), "line ")
	h.size(120, 20)
	if h.m.openID != "dm-dana" || h.m.topKey() != before {
		t.Errorf("back at 120: open=%q top=%q, want dm-dana %q", h.m.openID, h.m.topKey(), before)
	}
	if h.m.cursorID() != "dm-dana" {
		t.Errorf("sidebar selection = %q, want dm-dana", h.m.cursorID())
	}
}

func TestFrameFillsWindowExactly(t *testing.T) {
	for _, s := range [][2]int{{120, 30}, {110, 14}, {109, 14}, {70, 12}, {69, 12}, {36, 10}, {35, 10}, {20, 5}} {
		h := started(t, fixture(), s[0], s[1])
		lines := h.lines()
		if len(lines) != s[1] {
			t.Errorf("%dx%d: %d rows", s[0], s[1], len(lines))
		}
		for i, l := range lines {
			if w := lipgloss.Width(l); w != s[0] {
				t.Errorf("%dx%d row %d is %d cells: %q", s[0], s[1], i, w, l)
			}
		}
	}
}
