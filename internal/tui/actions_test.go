package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/ipc"
)

// selectedText is the text of the selected message, or "".
func selectedText(h *tester) string {
	if h.m.thread == nil {
		return ""
	}
	for _, it := range h.m.thread.items() {
		if it.key == h.m.thread.sel {
			return it.m.Text
		}
	}
	return ""
}

func TestUpInEmptyInputSelectsOwnMessagesOnly(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.key("up")
	if h.m.focus != focusThread || selectedText(h) != "pushing a fix now" {
		t.Fatalf("up selected %q, focus %v", selectedText(h), h.m.focus)
	}
	var seen []string
	for _, k := range []string{"up", "k", "up", "up"} {
		h.key(k)
		seen = append(seen, selectedText(h))
	}
	if want := []string{"on it", "yes, 10 min ago", "yes, 10 min ago", "yes, 10 min ago"}; !slices.Equal(seen, want) {
		t.Errorf("selection going up = %q, want %q", seen, want)
	}
	h.key("j")
	h.key("down")
	if selectedText(h) != "pushing a fix now" {
		t.Errorf("selection going down = %q", selectedText(h))
	}
	// Visible without colour.
	_, l := lineWith(h.lines(), "pushing a fix now")
	if !strings.Contains(l, "›") {
		t.Errorf("selected row has no marker: %q", l)
	}
	if n := strings.Count(h.view(), "›"); n != 1 {
		t.Errorf("%d markers, want 1", n)
	}
	h.key("esc")
	if h.m.focus != focusInput || h.m.thread.sel != "" {
		t.Errorf("esc: focus %v sel %q", h.m.focus, h.m.thread.sel)
	}
	h.key("up")
	h.typeText("w")
	if h.m.focus != focusThread || h.draft() != "" || selectedText(h) != "pushing a fix now" {
		t.Errorf("a letter on a selection: focus %v draft %q selected %q", h.m.focus, h.draft(), selectedText(h))
	}
	mustContain(t, h.view(), "press i to type")
	h.key("i")
	h.typeText("w")
	if h.m.focus != focusInput || h.draft() != "w" {
		t.Errorf("i then typing on a selection: focus %v draft %q", h.m.focus, h.draft())
	}
	h.key("up")
	if h.m.focus != focusInput {
		t.Error("up in a non-empty input must not select")
	}
}

func TestEditOwnMessageFlow(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.typeText("my draft")
	h.click(30, h.row("on it"))
	if selectedText(h) != "on it" || h.m.focus != focusThread {
		t.Fatalf("click selected %q focus %v", selectedText(h), h.m.focus)
	}
	h.key("e")
	if h.draft() != "on it" || h.m.focus != focusInput {
		t.Fatalf("e: draft %q focus %v", h.draft(), h.m.focus)
	}
	mustContain(t, h.lines()[inputRow(h)-1], "editing — enter saves, esc cancels")
	h.typeText(" now")
	h.enter()
	if got := f.writes("edit "); !slices.Equal(got, []string{`edit r-platform r-platform-m6 "on it now"`}) {
		t.Fatalf("edits = %q", got)
	}
	if f.sends() != nil {
		t.Errorf("saving an edit sent a message: %q", f.sends())
	}
	if h.draft() != "my draft" {
		t.Errorf("draft after the edit = %q, want the one set aside", h.draft())
	}
	mustNotContain(t, h.view(), "editing —")
}

func TestEditCancelledByEscUnchangedOrEmpty(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.typeText("keep")
	h.click(30, h.row("on it"))
	h.key("e")
	h.typeText(" more")
	h.key("esc")
	if h.draft() != "keep" || h.m.focus != focusThread || selectedText(h) != "on it" {
		t.Errorf("esc: draft %q focus %v sel %q", h.draft(), h.m.focus, selectedText(h))
	}
	h.key("e")
	h.enter() // unchanged
	if h.draft() != "keep" {
		t.Errorf("unchanged edit: draft %q", h.draft())
	}
	h.click(30, h.row("on it"))
	h.key("e")
	for range 5 {
		h.key("backspace")
	}
	h.enter() // empty
	if got := f.writes("edit ", "delete ", "send "); got != nil {
		t.Errorf("cancelled edits called %q", got)
	}
}

func TestCannotEditOthersMessage(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.click(30, h.row("great. one row"))
	if h.m.thread.sel != "" {
		t.Fatalf("clicking dana's message selected it")
	}
	h.key("e")
	h.key("d")
	mustNotContain(t, h.view(), "editing —", "delete this message?")
	// No key path reaches someone else's message.
	h.key("esc")
	h.key("tab")
	h.key("tab")
	h.key("up")
	for range 6 {
		if txt := selectedText(h); txt != "" && txt != "pushing a fix now" && txt != "on it" && txt != "yes, 10 min ago" {
			t.Fatalf("selected someone else's message %q", txt)
		}
		h.key("up")
	}
	h.key("e")
	h.typeText("!")
	h.enter()
	for _, c := range f.writes("edit ", "delete ") {
		if !strings.Contains(c, "r-platform-m2") {
			t.Errorf("edit or delete on another person's message: %q", c)
		}
	}
}

func TestDeleteAsksFirst(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.click(30, h.row("on it"))
	h.key("d")
	mustContain(t, h.view(), "delete this message? y/n")
	h.key("n")
	mustNotContain(t, h.view(), "delete this message?")
	if f.writes("delete ") != nil {
		t.Fatal("n deleted")
	}
	h.key("d")
	h.key("y")
	if got := f.writes("delete "); !slices.Equal(got, []string{"delete r-platform r-platform-m6"}) {
		t.Errorf("deletes = %q", got)
	}
}

func TestDeletedMessageIsNotSelectable(t *testing.T) {
	f := fixture()
	gone := msg("r-platform", 7, 46, "jason", "")
	gone.Deleted = true
	f.addStored("r-platform", gone)
	f.outbox = nil
	h := opened(t, f, 90, 20)
	h.key("up")
	if selectedText(h) != "on it" {
		t.Errorf("up selected %q, want the newest message not deleted", selectedText(h))
	}
	h.click(30, h.row("message deleted"))
	h.key("e")
	h.key("d")
	h.key("y")
	if got := f.writes("edit ", "delete "); got != nil {
		t.Errorf("acted on a deleted message: %q", got)
	}
}

func failedFixture() *fakeBackend {
	f := fixture()
	f.outbox["r-platform"][0].Status = ipc.MsgFailed
	f.reasons = map[string][]ipc.OutboxItem{"r-platform": {{MsgID: "o1", ConvID: "r-platform",
		State: "failed", Err: "expired"}}}
	return f
}

func TestRetryFailedByKeyAndByClick(t *testing.T) {
	f := failedFixture()
	h := opened(t, f, 120, 20)
	mustContain(t, h.view(), "failed (expired) — retry?")
	h.key("up")
	mustContain(t, h.view(), "failed (expired) — r retry, x discard")
	h.key("r")
	h.key("esc")
	h.click(30, h.row("pushing a fix now"))
	if got := f.writes("retry "); !slices.Equal(got, []string{"retry o1", "retry o1"}) {
		t.Errorf("retries = %q", got)
	}
}

func TestDiscardAsksFirst(t *testing.T) {
	f := failedFixture()
	h := opened(t, f, 90, 20)
	h.key("up")
	h.key("x")
	mustContain(t, h.view(), "discard this message? y/n")
	h.key("y")
	if got := f.writes("discard "); !slices.Equal(got, []string{"discard o1"}) {
		t.Errorf("discards = %q", got)
	}
}

func TestSendingItemOffersNothing(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.key("up")
	for _, k := range []string{"e", "d", "r", "x"} {
		h.key(k)
	}
	h.click(30, h.row("pushing a fix now"))
	if got := f.writes("edit ", "delete ", "retry ", "discard "); got != nil {
		t.Errorf("a sending item was acted on: %q", got)
	}
	if h.draft() != "" {
		t.Errorf("draft = %q", h.draft())
	}
}

func TestActionErrorsShownSanitisedAndCleared(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	f.failWith("edit", ipc.ErrNotConnected)
	h.click(30, h.row("on it"))
	h.key("e")
	h.typeText("!")
	h.enter()
	mustContain(t, h.lines()[inputRow(h)-1], "can't edit while offline")
	if h.draft() != "on it!" {
		t.Errorf("failed edit lost the text: %q", h.draft())
	}
	h.typeText("?")
	mustNotContain(t, h.view(), "can't edit")

	f.failWith("delete", fmt.Errorf("%w: boom\x1b[2J", ipc.ErrForbidden))
	h.key("esc")
	h.key("d")
	h.key("y")
	mustContain(t, h.view(), "can't delete: not allowed")
	if strings.Contains(h.raw(), "\x1b[2J") {
		t.Error("error not sanitised")
	}
	if d := h.fireNoticeTicks(); !slices.Contains(d, 5*time.Second) {
		t.Errorf("notice timers %v, want 5s", d)
	}
	mustNotContain(t, h.view(), "can't delete")

	f.failWith("retry", errors.New("weird\x07thing"))
	f2 := failedFixture()
	f.setStored("r-platform", f2.stored["r-platform"]...)
	f.outbox, f.reasons = f2.outbox, f2.reasons
	h.send(resyncMsg{})
	h.key("esc")
	h.key("up")
	h.key("r")
	mustContain(t, h.view(), "can't retry: weirdthing")
}

func TestUnsentRowsKeyedByIDKeepAnchor(t *testing.T) {
	f := fixture()
	var stored []ipc.Message
	for i := 1; i <= 30; i++ {
		stored = append(stored, msg("r-platform", int64(i), i, "dana", fmt.Sprintf("line %d.", i)))
	}
	f.setStored("r-platform", stored...)
	mk := func(id, text string) ipc.Message {
		return ipc.Message{ConvID: "r-platform", MsgID: id, Author: "jason", AuthorName: "jason",
			At: at(40), Text: text, Status: ipc.MsgSending}
	}
	// Unsent items come last; the second is tall enough to be scrolled to.
	f.outbox["r-platform"] = []ipc.Message{mk("oA", "first unsent"),
		mk("oB", "second unsent"+strings.Repeat("\nmore", 19))}
	h := opened(t, f, 90, 20)
	for _, it := range h.m.thread.items() {
		if it.m.MsgID == "oB" {
			h.m.thread.atBottom, h.m.thread.anchor, h.m.thread.anchorOff = false, it.key, 0
		}
	}
	top := func() string { return h.lines()[3] }
	if !strings.Contains(top(), "second unsent") {
		t.Fatalf("top row = %q", top())
	}
	delivered := msg("r-platform", 31, 40, "jason", "first unsent")
	delivered.MsgID = "oA"
	f.addStored("r-platform", delivered)
	f.outbox["r-platform"] = f.outbox["r-platform"][1:]
	h.send(resyncMsg{})
	if !strings.Contains(top(), "second unsent") {
		t.Errorf("after the earlier item was delivered the top row = %q", top())
	}
}

func TestDeliveredAtBottomStaysAtBottom(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	before := h.lines()
	ob := f.outbox["r-platform"][0]
	got := msg("r-platform", 7, 45, "jason", ob.Text)
	got.MsgID = ob.MsgID
	f.addStored("r-platform", got)
	f.outbox = nil
	h.send(resyncMsg{})
	after := h.lines()
	if !h.m.thread.atBottom {
		t.Error("delivery moved the view off the bottom")
	}
	i := h.row("pushing a fix now")
	if !strings.Contains(before[i], "pushing a fix now") || strings.Contains(after[i], "sending…") {
		t.Errorf("row %d before %q after %q", i, before[i], after[i])
	}
}

func TestScrollbackKeepsAnchor(t *testing.T) {
	f := fixture()
	var stored []ipc.Message
	for i := 1; i <= 120; i++ {
		stored = append(stored, msg("r-platform", int64(i), i, "dana", fmt.Sprintf("line %d.", i)))
	}
	f.setStored("r-platform", stored...)
	h := opened(t, f, 90, 20)
	h.key("alt+enter") // a taller input must not disturb the anchor
	h.key("alt+enter")
	h.key("esc")
	f.resetCalls()
	h.key("home")
	if got := f.writes("messages "); !slices.Equal(got, []string{"messages r-platform 71 50"}) {
		t.Fatalf("scrolling to the top read %q, want the 50 before seq 71", got)
	}
	if !strings.Contains(h.lines()[3], "line 71.") {
		t.Errorf("top row after loading older = %q, want line 71 kept", h.lines()[3])
	}
}

// --- mouse ---

func TestClickInInputFocusesAndPlacesCursor(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.typeText("hello world")
	h.key("esc")
	if h.m.focus != focusList {
		t.Fatalf("esc focus = %v", h.m.focus)
	}
	h.click(23+2+3, inputRow(h))
	if h.m.focus != focusInput {
		t.Fatalf("click focus = %v", h.m.focus)
	}
	h.typeText("X")
	if h.draft() != "helXlo world" {
		t.Errorf("draft = %q", h.draft())
	}
}

func TestWheelOverInputScrollsInputOnlyWhenItOverflows(t *testing.T) {
	f := fixture()
	var stored []ipc.Message
	for i := 1; i <= 40; i++ {
		stored = append(stored, msg("r-platform", int64(i), i, "dana", fmt.Sprintf("line %d.", i)))
	}
	f.setStored("r-platform", stored...)
	h := opened(t, f, 90, 20)
	h.typeText("short")
	h.wheel(30, inputRow(h), true)
	if h.m.thread.atBottom {
		t.Error("wheel over a one-line input did not scroll the thread")
	}
	h.key("esc")
	h.key("end")
	h.click(23+2+5, inputRow(h))
	for i := range 7 {
		h.key("alt+enter")
		h.typeText(fmt.Sprintf("row%d", i))
	}
	h.wheel(30, inputRow(h), true)
	if !h.m.thread.atBottom {
		t.Error("wheel over an overflowing input scrolled the thread")
	}
	mustContain(t, h.lines()[inputRow(h)], "> short")
}

func TestClickDropsAQuestion(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.click(30, h.row("on it"))
	h.key("d")
	h.click(30, h.row("yes, 10 min"))
	mustNotContain(t, h.view(), "delete this message?")
	h.key("y")
	if got := f.writes("delete "); got != nil {
		t.Errorf("y after a click deleted: %q", got)
	}
}

func TestSelectionLeavesADeletedMessage(t *testing.T) {
	f := fixture()
	h := opened(t, f, 90, 20)
	h.click(30, h.row("on it"))
	h.key("d")
	h.key("y")
	gone := msg("r-platform", 6, 45, "jason", "")
	gone.Deleted = true
	f.stored["r-platform"][5] = gone
	h.send(resyncMsg{})
	if h.m.thread.sel != "" || strings.Contains(h.view(), "›") {
		t.Errorf("a deleted message is still selected: sel %q\n%s", h.m.thread.sel, h.view())
	}
}
