package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jpe0824/termchat/internal/ipc"
)

// --- the input on its own ---

func TestInputLineEditing(t *testing.T) {
	var in input
	in.insert("hello world")
	in.home()
	if in.pos != 0 {
		t.Fatalf("home: pos %d", in.pos)
	}
	in.right()
	in.right()
	in.del()
	in.backspace()
	if in.text != "hlo world" || in.pos != 1 {
		t.Fatalf("after right right delete backspace: %q at %d", in.text, in.pos)
	}
	in.end()
	in.killWord()
	if in.text != "hlo " {
		t.Fatalf("ctrl+w: %q", in.text)
	}
	in.insert("there\nsecond line")
	in.killLine()
	if in.text != "hlo there\n" {
		t.Fatalf("ctrl+u on the second line: %q", in.text)
	}
	in.insert("x")
	in.home()
	in.left()
	if in.pos != len("hlo there") {
		t.Fatalf("left from the start of line 2 should end line 1: pos %d", in.pos)
	}
}

func TestCleanInputDropsControlsKeepsTabAndNewline(t *testing.T) {
	got := cleanInput("a\x1b[31mb\x07c\td\r\ne\x00f\rg\u009bh")
	if want := "abc\td\nef\ngh"; got != want {
		t.Errorf("cleanInput = %q, want %q", got, want)
	}
}

func TestInputWrapAndCursorWithWideCharacters(t *testing.T) {
	var in input
	in.insert("日本語テキスト") // seven characters, two cells each
	// Width 9 leaves 8 cells for text and one for the cursor: 4 per row.
	rows, r, c := in.render(9, 5)
	if want := []string{"日本語テ", "キスト"}; !slices.Equal(rows, want) {
		t.Fatalf("rows = %q, want %q", rows, want)
	}
	if r != 1 || c != 6 {
		t.Errorf("cursor at end = row %d col %d, want 1 6", r, c)
	}
	for range 3 {
		in.left()
	}
	if _, r, c = in.render(9, 5); r != 1 || c != 0 {
		t.Errorf("after 3 left = row %d col %d, want 1 0", r, c)
	}
	in.left()
	if _, r, c = in.render(9, 5); r != 0 || c != 6 {
		t.Errorf("after 4 left = row %d col %d, want 0 6", r, c)
	}
	in.down(9)
	if _, r, c = in.render(9, 5); r != 1 || c != 6 {
		t.Errorf("down = row %d col %d, want 1 6", r, c)
	}
	in.up(9)
	in.up(9)
	if _, r, c = in.render(9, 5); r != 0 || c != 6 {
		t.Errorf("up up = row %d col %d, want 0 6 (stays on the first row)", r, c)
	}
}

func TestInputEmojiAreSingleSteps(t *testing.T) {
	var in input
	in.insert("a👍🏽👨‍👩‍👧b")
	_, _, c := in.render(20, 5)
	if c != 6 {
		t.Fatalf("cursor col = %d, want 6 (1+2+2+1)", c)
	}
	in.left()
	in.left()
	if _, _, c = in.render(20, 5); c != 3 {
		t.Errorf("after two left col = %d, want 3", c)
	}
	in.backspace()
	if in.text != "a👨‍👩‍👧b" {
		t.Errorf("backspace removed part of a cluster: %q", in.text)
	}
	in.del()
	if in.text != "ab" {
		t.Errorf("delete removed part of a cluster: %q", in.text)
	}
}

func TestInputDrawsTabsAsSpaces(t *testing.T) {
	var in input
	in.insert("a\tb")
	rows, _, c := in.render(20, 5)
	if rows[0] != "a    b" || c != 6 {
		t.Errorf("rows %q cursor %d", rows, c)
	}
	if in.text != "a\tb" {
		t.Errorf("the tab must stay in the text: %q", in.text)
	}
}

// --- typing and sending ---

// inputRow is the screen row of the input's first line.
func inputRow(h *tester) int { return h.row("│ > ") }

func TestEnterSendsAndClearsInput(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.typeText("hello there")
	mustContain(t, h.lines()[inputRow(h)], "> hello there")
	h.enter()
	if got := f.sends(); !slices.Equal(got, []string{`send r-platform "hello there" tui`}) {
		t.Fatalf("sends = %q", got)
	}
	if h.draft() != "" {
		t.Errorf("draft after a successful send = %q", h.draft())
	}
	if l := h.lines()[inputRow(h)]; strings.TrimRight(l, " │") != "│                    │ >" {
		t.Errorf("input row after send = %q", l)
	}
}

func TestMultiLinePasteIsOneMessage(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.paste("a\nb\nc")
	if f.sends() != nil {
		t.Fatalf("a paste sent by itself: %q", f.sends())
	}
	h.enter()
	if got := f.sends(); !slices.Equal(got, []string{`send r-platform "a\nb\nc" tui`}) {
		t.Errorf("sends = %q", got)
	}
}

func TestUnbracketedPasteBurstIsOneMessage(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	// Without bracketed paste a pasted line break arrives as an enter key,
	// with the next characters right behind it.
	h.typeText("a\nb\nc")
	h.settle()
	if f.sends() != nil {
		t.Fatalf("burst sent line by line: %q", f.sends())
	}
	h.enter()
	if got := f.sends(); !slices.Equal(got, []string{`send r-platform "a\nb\nc" tui`}) {
		t.Errorf("sends = %q", got)
	}
}

func TestAltEnterAndCtrlJInsertNewline(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.typeText("a")
	h.key("alt+enter")
	h.typeText("b")
	h.key("ctrl+j")
	h.typeText("c")
	h.settle()
	if f.sends() != nil {
		t.Fatalf("newline keys sent: %q", f.sends())
	}
	h.enter()
	if got := f.sends(); !slices.Equal(got, []string{`send r-platform "a\nb\nc" tui`}) {
		t.Errorf("sends = %q", got)
	}
}

func TestWhitespaceOnlyNotSent(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.enter()
	h.typeText("   ")
	h.key("alt+enter")
	h.paste("\t ")
	h.enter()
	if f.sends() != nil {
		t.Errorf("sent whitespace: %q", f.sends())
	}
	if h.m.focus != focusInput {
		t.Errorf("focus after refusing whitespace = %v", h.m.focus)
	}
}

func TestOversizeRefused(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.paste(strings.Repeat("a", 7000))
	mustNotContain(t, h.view(), "/8192")
	h.typeText("b")
	mustContain(t, h.view(), "7001/8192")
	h.paste(strings.Repeat("c", 1300))
	mustContain(t, h.view(), "8301/8192")
	h.enter()
	if f.sends() != nil {
		t.Fatal("sent a message over the limit")
	}
	mustContain(t, h.view(), "too long to send: 8301/8192 bytes")
	if len(h.draft()) != 8301 {
		t.Errorf("draft lost on refusal: %d bytes", len(h.draft()))
	}
}

func TestPastedControlCharactersDroppedAndDraftInert(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.paste("x\x1b[2Jy\x1b]0;pwned\x07z\x00!")
	if h.draft() != "xyz!" {
		t.Errorf("draft = %q", h.draft())
	}
	for _, bad := range []string{"\x1b[2J", "\x1b]0;", "pwned"} {
		if strings.Contains(h.raw(), bad) {
			t.Errorf("frame carries %q from the draft", bad)
		}
	}
	mustContain(t, h.view(), "> xyz!")
}

func TestInputGrowsToFiveRowsThenScrolls(t *testing.T) {
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
	lines := h.lines()
	first := inputRow(h)
	if want := 18 - 4; first != want {
		t.Fatalf("input starts at row %d, want %d (five rows above the footer)", first, want)
	}
	var got []string
	for _, l := range lines[first:19] {
		got = append(got, strings.TrimSpace(strings.Split(l, "│")[2]))
	}
	if want := []string{"> line3", "line4", "line5", "line6", "line7"}; !slices.Equal(got, want) {
		t.Errorf("input rows = %q, want %q", got, want)
	}
	// The thread shrank and still ends with the newest message.
	if !strings.Contains(lines[first-2], "sending…") {
		t.Errorf("row above the rule = %q, want the newest message", lines[first-2])
	}
	if c := h.m.View().Cursor; c == nil || c.Y != 18 || c.X != 23+2+5 {
		t.Errorf("cursor = %+v, want after line7 on row 18", c)
	}
}

func TestResizeKeepsDraft(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.typeText("keep me")
	for range 2 {
		h.key("left")
	}
	for _, s := range [][2]int{{44, 14}, {30, 14}, {120, 30}, {90, 20}} {
		h.size(s[0], s[1])
		mustContain(t, h.view(), "keep me")
	}
	h.typeText("X")
	if h.draft() != "keep Xme" {
		t.Errorf("draft after resizes = %q, cursor not kept", h.draft())
	}
}

func TestDraftPerConversation(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.typeText("for platform")
	h.click(4, 2) // dana
	if h.m.openID != "dm-dana" || h.draft() != "" {
		t.Fatalf("open %q draft %q", h.m.openID, h.draft())
	}
	if h.m.focus != focusInput {
		t.Errorf("opening did not focus the input: %v", h.m.focus)
	}
	h.typeText("for dana")
	h.click(4, 6) // # platform
	if h.draft() != "for platform" {
		t.Errorf("platform draft = %q", h.draft())
	}
	h.size(44, 14)
	h.key("esc")
	h.key("up")
	h.key("up")
	h.key("up")
	h.key("enter")
	if h.m.openID != "dm-dana" || h.draft() != "for dana" {
		t.Errorf("narrow reopen %q draft %q", h.m.openID, h.draft())
	}
	if f.sends() != nil {
		t.Errorf("switching sent: %q", f.sends())
	}
}

// --- send results ---

func TestSendFailureKeepsDraftAndShowsError(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	f.failWith("send", errors.New("disk full\x1b[2J"))
	h.typeText("important")
	h.enter()
	if h.draft() != "important" {
		t.Fatalf("draft after a failed send = %q", h.draft())
	}
	i := inputRow(h)
	mustContain(t, h.lines()[i-1], "can't send: disk full")
	if strings.Contains(h.raw(), "\x1b[2J") {
		t.Error("error text not sanitised")
	}
	if len(f.sends()) != 1 {
		t.Fatalf("sends after failure = %q", f.sends())
	}
	f.failWith("send", nil)
	h.enter()
	if got := f.sends(); len(got) != 2 || got[1] != `send r-platform "important" tui` {
		t.Errorf("second enter sends = %q", got)
	}
	if h.draft() != "" {
		t.Errorf("draft after the second, successful send = %q", h.draft())
	}
}

func TestEnterTwiceSendsOnce(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.typeText("once")
	// Two enters back to back are one send.
	h.key("enter")
	h.key("enter")
	h.settle()
	if got := f.sends(); !slices.Equal(got, []string{`send r-platform "once" tui`}) {
		t.Fatalf("sends = %q", got)
	}
	// And an enter while the first send is still out sends nothing more.
	h.typeText("twice")
	h.hold = true
	h.enter()
	h.enter()
	h.hold = false
	held := h.held
	h.held = nil
	for _, c := range held {
		h.run(c)
	}
	if got := f.sends(); len(got) != 2 {
		t.Errorf("sends = %q, want one more", got)
	}
}

func TestSendWhileScrolledUpJumpsToBottom(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 12)
	h.key("pgup")
	if h.m.thread.atBottom {
		t.Fatal("pgup did not scroll up")
	}
	h.typeText("hi")
	h.enter()
	if !h.m.thread.atBottom {
		t.Error("sending did not jump to the bottom")
	}
}

func TestSendResultLandsOnItsOwnConversation(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.typeText("to platform")
	h.hold = true
	h.enter()
	h.hold = false
	h.click(4, 2) // dana, while the send is out
	h.typeText("dana draft")
	held := h.held
	h.held = nil
	for _, c := range held {
		h.run(c)
	}
	if h.draft() != "dana draft" {
		t.Errorf("dana draft = %q", h.draft())
	}
	if d := h.m.draftText("r-platform"); d != "" {
		t.Errorf("platform draft after its send succeeded = %q", d)
	}
	_ = ipc.MsgSending
}
