package tui

import (
	"slices"
	"testing"
	"time"
)

// listNames is the command list's rows by name, nil while it is hidden.
func listNames(h *tester) []string {
	var out []string
	for _, s := range h.m.slashMatches(h.m.drafts[h.m.openID]) {
		out = append(out, s.name)
	}
	return out
}

func picked(h *tester) int {
	if c := h.m.drafts[h.m.openID]; c != nil {
		return c.pick
	}
	return -1
}

const (
	notifyRow = "/notify all|mentions|muted  set this room's notifications"
	helpRow   = "/help  keys and commands"
)

func TestSlashListFilters(t *testing.T) {
	_, h := slashOpened(t)
	typeSlash(h)
	if got := listNames(h); !slices.Equal(got, []string{"notify", "help"}) {
		t.Fatalf("rows after / = %q", got)
	}
	mustContain(t, h.view(), "› "+notifyRow, "  "+helpRow)
	h.typeText("n")
	if got := listNames(h); !slices.Equal(got, []string{"notify"}) {
		t.Fatalf("rows after /n = %q", got)
	}
	mustContain(t, h.view(), "› "+notifyRow)
	mustNotContain(t, h.view(), helpRow)
	h.typeText("x")
	if got := listNames(h); got != nil {
		t.Fatalf("rows after /nx = %q, want none", got)
	}
	mustNotContain(t, h.view(), notifyRow, helpRow)
	if !cmdMode(h) || h.draft() != "/nx" {
		t.Errorf("cmd %v draft %q", cmdMode(h), h.draft())
	}
	// Once the name is followed by a space, the list is done.
	h.key("backspace")
	h.key("backspace")
	h.typeText("notify ")
	if got := listNames(h); got != nil {
		t.Errorf("rows after %q = %q, want none", h.draft(), got)
	}
}

func TestSlashListKeys(t *testing.T) {
	_, h := slashOpened(t)
	typeSlash(h)
	h.key("up")
	if picked(h) != 0 {
		t.Errorf("up on the first row: pick %d", picked(h))
	}
	h.key("down")
	h.key("down")
	if picked(h) != 1 {
		t.Fatalf("down down: pick %d, want 1", picked(h))
	}
	mustContain(t, h.view(), "› "+helpRow, "  "+notifyRow)
	if h.m.thread.sel != "" || h.draft() != "/" {
		t.Errorf("down moved more than the highlight: sel %q draft %q", h.m.thread.sel, h.draft())
	}
	h.key("tab")
	if h.draft() != "/help " || !cmdMode(h) {
		t.Errorf("lone tab: draft %q cmd %v, want \"/help \" in command mode", h.draft(), cmdMode(h))
	}
	if got := listNames(h); got != nil {
		t.Errorf("rows after completing = %q", got)
	}

	_, h = slashOpened(t)
	typeSlash(h)
	soon(h, func() { h.typeText("n"); h.key("tab") })
	pause(h, time.Second)
	if h.draft() != "/n\t" {
		t.Errorf("tab in a burst: draft %q, want \"/n\\t\"", h.draft())
	}
}

func TestSlashListTypingResetsHighlight(t *testing.T) {
	_, h := slashOpened(t)
	typeSlash(h)
	h.key("down")
	h.typeText("n")
	if picked(h) != 0 {
		t.Errorf("pick %d after typing, want 0", picked(h))
	}
	h.key("tab")
	if h.draft() != "/notify " {
		t.Errorf("draft %q", h.draft())
	}
}

func TestSlashListEnter(t *testing.T) {
	f, h := slashOpened(t)
	typeSlash(h)
	h.typeText("no")
	h.enter()
	if h.draft() != "/notify " || !cmdMode(h) {
		t.Errorf("enter on a partial name: draft %q cmd %v", h.draft(), cmdMode(h))
	}
	if w := f.writes(slashWrites...); w != nil {
		t.Errorf("enter on a partial name wrote %q", w)
	}

	_, h = slashOpened(t)
	typeSlash(h)
	h.typeText("help")
	h.enter()
	if !h.m.help || h.draft() != "" {
		t.Errorf("enter on a full name: help %v draft %q", h.m.help, h.draft())
	}

	_, h = slashOpened(t)
	typeSlash(h)
	h.key("down")
	h.enter()
	if h.draft() != "/" || h.m.help {
		t.Errorf("enter on a bare /: draft %q help %v", h.draft(), h.m.help)
	}
}

func TestSlashListEsc(t *testing.T) {
	_, h := slashOpened(t)
	typeSlash(h)
	h.typeText("no")
	soon(h, func() { h.typeText("t"); h.key("esc") })
	pause(h, time.Second)
	if got := listNames(h); !slices.Equal(got, []string{"notify"}) || h.m.focus != focusInput {
		t.Fatalf("esc in a burst: rows %q focus %v, want the list kept", got, h.m.focus)
	}
	h.key("backspace")

	h.key("esc")
	if got := listNames(h); got != nil {
		t.Fatalf("esc: rows %q, want the list hidden", got)
	}
	mustNotContain(t, h.view(), notifyRow)
	if h.draft() != "/no" || !cmdMode(h) || h.m.focus != focusInput {
		t.Fatalf("esc: draft %q cmd %v focus %v", h.draft(), cmdMode(h), h.m.focus)
	}
	h.enter()
	if want := "unknown command /no — commands: /notify /help; // sends a leading /"; noteText(h) != want {
		t.Errorf("enter after esc: notice %q, want %q", noteText(h), want)
	}
	h.key("esc")
	if h.m.focus == focusInput {
		t.Error("a second esc left the keyboard in the input")
	}
	if h.draft() != "/no" {
		t.Errorf("draft %q after leaving", h.draft())
	}
}

func TestSlashListHints(t *testing.T) {
	_, h := slashOpened(t)
	typeSlash(h)
	mustContain(t, h.view(), "↑↓ pick  tab complete  ⏎ run  esc hide")
	h.key("esc")
	mustNotContain(t, h.view(), "tab complete")
}

func TestSlashListClickKeepsTheInput(t *testing.T) {
	_, h := slashOpened(t)
	typeSlash(h)
	y := h.row(helpRow)
	h.click(40, y)
	if h.m.focus != focusInput || !cmdMode(h) || h.draft() != "/" || listNames(h) == nil {
		t.Errorf("click on the list: focus %v cmd %v draft %q rows %q", h.m.focus, cmdMode(h), h.draft(), listNames(h))
	}
}
