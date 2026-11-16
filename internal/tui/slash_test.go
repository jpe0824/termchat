package tui

import (
	"slices"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/ipc"
)

var slashWrites = []string{"send ", "configset "}

// slashOpened is the fixture with # platform open and its input focused,
// nothing recorded yet.
func slashOpened(t *testing.T, mod ...func(*Options)) (*fakeBackend, *tester) {
	t.Helper()
	f := fixture()
	h := opened(t, f, 90, 20, mod...)
	f.resetCalls()
	return f, h
}

// typeSlash types a lone / and lets the paste gap pass.
func typeSlash(h *tester) {
	h.t.Helper()
	h.key("/")
	pause(h, time.Second)
}

func cmdMode(h *tester) bool {
	c := h.m.drafts[h.m.openID]
	return c != nil && c.cmd
}

func noteText(h *tester) string {
	if h.m.note == nil {
		return ""
	}
	return h.m.note.text
}

func TestSlashTypedEntersCommandMode(t *testing.T) {
	f, h := slashOpened(t)
	feed(h, "/")
	if cmdMode(h) || h.draft() != "/" {
		t.Fatalf("before the gap: cmd %v draft %q", cmdMode(h), h.draft())
	}
	pause(h, time.Second)
	if !cmdMode(h) || h.draft() != "/" {
		t.Fatalf("after the gap: cmd %v draft %q", cmdMode(h), h.draft())
	}
	h.typeText("notify all")
	h.enter()
	if got := f.writes(slashWrites...); !slices.Equal(got, []string{"configset rooms.r-platform all"}) {
		t.Errorf("writes = %q", got)
	}
	if h.draft() != "" || cmdMode(h) {
		t.Errorf("draft %q cmd %v, want both cleared", h.draft(), cmdMode(h))
	}
	if noteText(h) != "#platform notifies: all" || !h.m.note.info {
		t.Errorf("notice = %+v", h.m.note)
	}
}

func TestSlashBurstIsText(t *testing.T) {
	f, h := slashOpened(t)
	soon(h, func() { h.typeText("/n") })
	pause(h, time.Second)
	if cmdMode(h) || h.draft() != "/n" {
		t.Fatalf("cmd %v draft %q, want text", cmdMode(h), h.draft())
	}
	h.typeText("otify all")
	h.enter()
	if got := f.writes(slashWrites...); !slices.Equal(got, []string{`send r-platform "/notify all" tui`}) {
		t.Errorf("writes = %q", got)
	}
}

func TestSlashBracketedPasteIsText(t *testing.T) {
	f, h := slashOpened(t)
	h.paste("/usr/bin")
	pause(h, time.Second)
	if cmdMode(h) {
		t.Fatal("a bracketed paste entered command mode")
	}
	h.enter()
	if got := f.writes(slashWrites...); !slices.Equal(got, []string{`send r-platform "/usr/bin" tui`}) {
		t.Errorf("writes = %q", got)
	}
}

func TestSlashGapOff(t *testing.T) {
	_, h := slashOpened(t, func(o *Options) { o.PasteGap = -1 })
	h.key("/")
	if !cmdMode(h) || h.draft() != "/" {
		t.Errorf("cmd %v draft %q, want command mode at once", cmdMode(h), h.draft())
	}
}

func TestSlashDoubleSlashSends(t *testing.T) {
	f, h := slashOpened(t)
	typeSlash(h)
	h.typeText("/hi")
	h.enter()
	if got := f.writes(slashWrites...); !slices.Equal(got, []string{`send r-platform "/hi" tui`}) {
		t.Errorf("writes = %q", got)
	}
	if h.draft() != "" || cmdMode(h) {
		t.Errorf("draft %q cmd %v, want both cleared", h.draft(), cmdMode(h))
	}
}

func TestSlashUnknown(t *testing.T) {
	f, h := slashOpened(t)
	typeSlash(h)
	h.typeText("frob")
	h.enter()
	if want := "unknown command /frob — commands: /settings /notify /help; // sends a leading /"; noteText(h) != want {
		t.Errorf("notice %q, want %q", noteText(h), want)
	}
	if h.draft() != "/frob" || !cmdMode(h) {
		t.Errorf("draft %q cmd %v, want /frob kept in command mode", h.draft(), cmdMode(h))
	}
	if got := f.writes(slashWrites...); got != nil {
		t.Errorf("writes = %q", got)
	}
}

func TestSlashNotifyRefusals(t *testing.T) {
	f, h := slashOpened(t)
	for _, text := range []string{"notify", "notify loud"} {
		h.send(keyMsg("ctrl+u"))
		typeSlash(h)
		h.typeText(text)
		h.enter()
		if want := "/notify all|mentions|muted"; noteText(h) != want {
			t.Errorf("/%s: notice %q, want %q", text, noteText(h), want)
		}
		if h.draft() != "/"+text {
			t.Errorf("/%s: draft %q", text, h.draft())
		}
	}

	h.key("esc")
	for _, k := range []string{"up", "up", "k", "enter"} {
		h.key(k)
	}
	if h.m.openID != "dm-dana" {
		t.Fatalf("open %q, want dm-dana", h.m.openID)
	}
	typeSlash(h)
	h.typeText("notify all")
	h.enter()
	if want := "notification modes apply to rooms only"; noteText(h) != want {
		t.Errorf("DM: notice %q, want %q", noteText(h), want)
	}
	if got := f.writes("configset "); got != nil {
		t.Errorf("writes = %q", got)
	}
}

func TestSlashNotInEdit(t *testing.T) {
	_, h := slashOpened(t)
	h.key("up")
	h.key("up")
	h.key("e")
	if c := h.m.drafts["r-platform"]; c == nil || c.edit == nil {
		t.Fatal("not editing")
	}
	h.send(keyMsg("ctrl+u"))
	typeSlash(h)
	if cmdMode(h) || h.draft() != "/" {
		t.Errorf("cmd %v draft %q, want plain text while editing", cmdMode(h), h.draft())
	}
}

func TestSlashFailureKeepsDraft(t *testing.T) {
	f, h := slashOpened(t)
	f.failWith("configset", ipc.ErrNotConnected)
	typeSlash(h)
	h.typeText("notify muted")
	h.enter()
	if got := f.writes("configset "); !slices.Equal(got, []string{"configset rooms.r-platform muted"}) {
		t.Errorf("writes = %q", got)
	}
	if h.draft() != "/notify muted" || !cmdMode(h) {
		t.Errorf("draft %q cmd %v, want both kept", h.draft(), cmdMode(h))
	}
	if want := failure("notify", ipc.ErrNotConnected); noteText(h) != want || h.m.note.info {
		t.Errorf("notice %+v, want error %q", h.m.note, want)
	}
}

func TestSlashLeavingLeadingSlashEndsCommandMode(t *testing.T) {
	_, h := slashOpened(t)
	typeSlash(h)
	h.typeText("x")
	h.key("backspace")
	h.key("backspace")
	if cmdMode(h) {
		t.Error("an empty draft is still in command mode")
	}
	h.paste("x")
	h.key("home")
	h.paste("/")
	if cmdMode(h) {
		t.Error("a pasted / made the draft a command")
	}
}

func TestSlashHelpOpensHelp(t *testing.T) {
	f, h := slashOpened(t)
	typeSlash(h)
	h.typeText("help")
	h.enter()
	if !h.m.help || h.draft() != "" || cmdMode(h) {
		t.Errorf("help %v draft %q cmd %v", h.m.help, h.draft(), cmdMode(h))
	}
	if got := f.writes(slashWrites...); got != nil {
		t.Errorf("writes = %q", got)
	}
}

func TestSlashResultKeepsChangedDraft(t *testing.T) {
	f, h := slashOpened(t)
	typeSlash(h)
	h.typeText("notify all")
	h.hold = true
	h.enter()
	held := h.held
	h.hold, h.held = false, nil
	if len(held) != 1 {
		t.Fatalf("%d commands held, want the ConfigSet", len(held))
	}
	// The draft changes while the command is out: its result must not
	// clear what is there now.
	h.key("backspace")
	done := held[0]()
	if _, ok := done.(slashDoneMsg); !ok {
		t.Fatalf("held command gave %T", done)
	}
	h.deliver(done)
	if h.draft() != "/notify al" || !cmdMode(h) {
		t.Errorf("draft %q cmd %v, want the changed draft kept", h.draft(), cmdMode(h))
	}
	if got := f.writes("configset "); !slices.Equal(got, []string{"configset rooms.r-platform all"}) {
		t.Errorf("writes = %q", got)
	}
}

// heldEnter presses enter with commands held and returns what was held.
func heldEnter(h *tester) []func() any {
	h.t.Helper()
	h.hold = true
	h.enter()
	held := h.held
	h.hold, h.held = false, nil
	var out []func() any
	for _, c := range held {
		out = append(out, func() any { return c() })
	}
	return out
}

func TestSlashStaleResultDropped(t *testing.T) {
	_, h := slashOpened(t)
	typeSlash(h)
	h.typeText("notify all")
	first := heldEnter(h)
	second := heldEnter(h)
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("held %d and %d commands, want one each", len(first), len(second))
	}
	h.deliver(second[0]())
	if noteText(h) != "#platform notifies: all" || h.draft() != "" {
		t.Fatalf("second result: notice %q draft %q", noteText(h), h.draft())
	}
	h.m.note = nil
	h.deliver(first[0]())
	if h.m.note != nil {
		t.Errorf("an earlier call's result was taken: notice %q", noteText(h))
	}
}

func TestSlashUncalledCommandKeepsResult(t *testing.T) {
	_, h := slashOpened(t)
	typeSlash(h)
	h.typeText("notify muted")
	out := heldEnter(h)
	if len(out) != 1 {
		t.Fatalf("held %d commands", len(out))
	}
	h.send(keyMsg("ctrl+u"))
	typeSlash(h)
	h.typeText("notify loud")
	h.enter()
	h.send(keyMsg("ctrl+u"))
	typeSlash(h)
	h.typeText("help")
	h.enter()
	if !h.m.help {
		t.Fatal("help not open")
	}
	h.key("esc")
	h.deliver(out[0]())
	if noteText(h) != "#platform notifies: muted" {
		t.Errorf("notice %q, want the outstanding result", noteText(h))
	}
}

func TestSlashBareSlashEnterDoesNothing(t *testing.T) {
	f, h := slashOpened(t)
	typeSlash(h)
	h.enter()
	if h.draft() != "/" || !cmdMode(h) || h.m.note != nil || f.writes(slashWrites...) != nil {
		t.Errorf("draft %q cmd %v notice %q writes %q", h.draft(), cmdMode(h), noteText(h), f.writes(slashWrites...))
	}
}

func TestSlashPasteInsideGapIsText(t *testing.T) {
	_, h := slashOpened(t)
	feed(h, "/")
	soon(h, func() { h.paste("usr") })
	pause(h, time.Second)
	if cmdMode(h) || h.draft() != "/usr" {
		t.Errorf("cmd %v draft %q, want text", cmdMode(h), h.draft())
	}
}

func TestSlashPastedArgsWithTrailingEnterDoesNotRun(t *testing.T) {
	f, h := slashOpened(t)
	typeSlash(h)
	if !cmdMode(h) {
		t.Fatal("not in command mode")
	}
	feed(h, "notify muted\n")
	pause(h, time.Second)
	if got := f.writes(slashWrites...); got != nil {
		t.Errorf("pasted enter ran the command: %q", got)
	}
	if want := "press enter to run /notify"; noteText(h) != want {
		t.Errorf("notice %q, want %q", noteText(h), want)
	}
	if h.draft() != "/notify muted" || !cmdMode(h) {
		t.Errorf("draft %q cmd %v", h.draft(), cmdMode(h))
	}
	h.enter()
	if got := f.writes(slashWrites...); !slices.Equal(got, []string{"configset rooms.r-platform muted"}) {
		t.Errorf("deliberate enter: writes = %q", got)
	}
}

// A // send whose result arrives while an edit has the draft set aside
// still ends command mode with the draft, so a later paste is text.
func TestSlashSentDuringEditEndsCommandMode(t *testing.T) {
	f, h := slashOpened(t)
	typeSlash(h)
	h.typeText("/hi")
	out := heldEnter(h)
	if len(out) != 1 {
		t.Fatalf("held %d commands, want the Send", len(out))
	}
	h.m.selectLast()
	h.key("up")
	h.key("e")
	pause(h, time.Second)
	c := h.m.drafts["r-platform"]
	if c == nil || c.edit == nil || c.edit.saved.text != "//hi" {
		t.Fatalf("not editing with //hi set aside: focus %v sel %q c %+v", h.m.focus, h.m.thread.sel, c)
	}
	h.deliver(out[0]())
	if c.edit.saved.text != "" || c.cmd {
		t.Fatalf("set-aside draft %q cmd %v, want empty and not a command", c.edit.saved.text, c.cmd)
	}
	h.enter()
	if c.edit != nil || h.m.focus != focusInput {
		t.Fatalf("edit %v focus %v, want the unchanged edit ended in the input", c.edit, h.m.focus)
	}
	h.paste("/usr/bin")
	pause(h, time.Second)
	h.enter()
	want := []string{`send r-platform "/hi" tui`, `send r-platform "/usr/bin" tui`}
	if got := f.writes(slashWrites...); !slices.Equal(got, want) {
		t.Errorf("writes = %q, want %q (notice %q)", got, want, noteText(h))
	}
}
