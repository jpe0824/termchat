package tui

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/jpe0824/termchat/internal/ipc"
)

// threadRows returns the thread pane's text in standard layout: the part of
// each row right of the sidebar.
func threadRows(h *tester) []string {
	var out []string
	for _, l := range h.lines() {
		parts := strings.Split(l, "│")
		if len(parts) >= 3 {
			out = append(out, strings.TrimRight(parts[2], " "))
		}
	}
	return out
}

func TestThreadRendering(t *testing.T) {
	h := started(t, fixture(), 90, 20)
	rows := threadRows(h)
	want := []string{
		" # platform",
		"",
		" 10:02  dana    did the meter import finish?",
		" 10:03  you     yes, 10 min ago",
		" 10:41  dana    great. one row looks off",
		" 10:42  ci-bot  [agent] build 812 passed",
		" 10:44  sam     @jason can you look? (edited)",
		" 10:45  you     on it",
		" 10:45  you     pushing a fix now  sending…",
	}
	if !strings.HasPrefix(rows[0], want[0]) || !strings.HasSuffix(rows[0], "4 members") {
		t.Errorf("header = %q", rows[0])
	}
	if strings.Trim(rows[1], " ─") != "" {
		t.Errorf("row under header = %q, want a rule", rows[1])
	}
	body := strings.Join(rows, "\n")
	for _, w := range want[2:] {
		if !strings.Contains(body, w+"\n") && !strings.HasSuffix(body, w) {
			t.Errorf("thread lacks row %q:\n%s", w, body)
		}
	}
	i, _ := lineWith(rows, "on it")
	j, _ := lineWith(rows, "pushing a fix")
	if j != i+1 {
		t.Errorf("the unsent item should come last, right after the newest message")
	}
	k := slices.IndexFunc(rows, func(r string) bool { return strings.TrimRight(r, " ") == " >" })
	if k < 0 || k+1 >= len(rows) || strings.Trim(rows[k+1], " ─") != "" {
		t.Errorf("want the input line with a rule under it:\n%s", body)
	}
}

func TestThreadMarksFailedDeletedAndDays(t *testing.T) {
	f := fixture()
	del := msg("r-platform", 3, 41, "dana", "secret")
	del.Deleted = true
	day1 := msg("r-platform", 1, -24*60, "dana", "yesterday")
	f.setStored("r-platform", day1, msg("r-platform", 2, 3, "sam", "today"), del)
	f.outbox["r-platform"][0].Status = ipc.MsgFailed
	h := started(t, f, 90, 20)
	v := h.view()
	mustContain(t, v, "message deleted", "failed — retry?")
	mustNotContain(t, v, "secret")
	sep := at(3).Format("Mon 2 Jan 2006")
	i, l := lineWith(h.lines(), sep)
	if i < 0 || !strings.Contains(l, "──") {
		t.Fatalf("no day separator %q:\n%s", sep, v)
	}
	a, _ := lineWith(h.lines(), "yesterday")
	b, _ := lineWith(h.lines(), "today")
	if !(a < i && i < b) {
		t.Errorf("separator at %d should sit between %d and %d", i, a, b)
	}
	if strings.Contains(v, at(-24*60).Format("Mon 2 Jan 2006")) {
		t.Errorf("no separator before the first message")
	}
}

func TestOwnUnsentAndStoredWithSameIDBothShow(t *testing.T) {
	f := fixture()
	stored := msg("r-platform", 7, 46, "jason", "dup text")
	stored.MsgID = "same"
	f.addStored("r-platform", stored)
	f.outbox["r-platform"] = []ipc.Message{{ConvID: "r-platform", MsgID: "same", Author: "jason",
		At: at(46), Text: "dup text", Status: ipc.MsgSending}}
	h := started(t, f, 90, 20)
	if n := strings.Count(h.view(), "dup text"); n != 2 {
		t.Errorf("%d rows of the duplicated id, want 2", n)
	}
}

func TestThreadWrapsUnderText(t *testing.T) {
	f := fixture()
	f.outbox = nil
	f.setStored("r-platform", msg("r-platform", 1, 2, "dana", strings.Repeat("word ", 30)),
		msg("r-platform", 2, 3, "sam", strings.Repeat("x", 120)))
	h := started(t, f, 80, 24)
	rows := threadRows(h)
	i, first := lineWith(rows, "10:02  dana")
	textCol := strings.Index(first, "word")
	cont := rows[i+1]
	if strings.TrimLeft(cont, " ") == "" || len(cont)-len(strings.TrimLeft(cont, " ")) != textCol {
		t.Errorf("continuation %q not aligned to column %d", cont, textCol)
	}
	paneW := 80 - 23
	for _, r := range rows {
		if w := lipgloss.Width(r); w > paneW {
			t.Errorf("row %q is %d cells, pane is %d", r, w, paneW)
		}
	}
	j, _ := lineWith(rows, "10:03  sam")
	if !strings.Contains(rows[j+1], "xxxx") {
		t.Errorf("a long unbroken word should continue on the next row: %q", rows[j+1])
	}
}

func TestEmptyThread(t *testing.T) {
	f := fixture()
	f.setStored("r-platform")
	f.outbox = nil
	h := started(t, f, 90, 20)
	mustContain(t, h.view(), "no messages yet")
}

func numbered(conv string, from, to int64) []ipc.Message {
	var out []ipc.Message
	for i := from; i <= to; i++ {
		out = append(out, msg(conv, i, int(i%60), "dana", "line "+strconv.FormatInt(i, 10)+"."))
	}
	return out
}

func TestOpenScrolledToBottomAndScrollKeys(t *testing.T) {
	f := fixture()
	f.outbox = nil
	f.setStored("r-platform", numbered("r-platform", 1, 40)...)
	h := started(t, f, 90, 20)
	mustContain(t, h.view(), "line 40.")
	mustNotContain(t, h.view(), "line 1.")
	h.key("tab")
	h.key("pgup")
	mustNotContain(t, h.view(), "line 40.")
	h.key("pgdown")
	mustContain(t, h.view(), "line 40.")
	h.key("home")
	mustContain(t, h.view(), "line 1.")
	h.key("end")
	mustContain(t, h.view(), "line 40.")
	// The wheel scrolls the pane under the pointer.
	h.wheel(40, 8, true)
	h.wheel(40, 8, true)
	if h.m.topKey() == "" {
		t.Fatal("no top row")
	}
	mustNotContain(t, h.view(), "line 40.")
	if h.m.cursorID() != "r-platform" {
		t.Errorf("wheel over the thread moved the chat list")
	}
}

func TestScrollToTopLoadsOlderAndKeepsPlace(t *testing.T) {
	f := fixture()
	f.outbox = nil
	f.setStored("r-platform", numbered("r-platform", 1, 120)...)
	h := started(t, f, 90, 20)
	if n := f.callCount("messages r-platform 0 50"); n != 1 {
		t.Fatalf("first read: %v", f.calls)
	}
	h.key("tab")
	for i := 0; i < 20 && f.callCount("messages r-platform 71 50") == 0; i++ {
		h.key("pgup")
	}
	if f.callCount("messages r-platform 71 50") != 1 {
		t.Fatalf("reaching the top should load the 50 before seq 71: %v", f.calls)
	}
	top := threadRows(h)[2]
	if h.m.topKey() != "s71" || !strings.Contains(top, "line 71.") {
		t.Errorf("after loading older the top row is %q (%s), want line 71", top, h.m.topKey())
	}
	h.key("pgup")
	mustContain(t, h.view(), "line 70.")
}

func TestNewMessagesWhileScrolledUpKeepPosition(t *testing.T) {
	f := fixture()
	f.outbox = nil
	f.setStored("r-platform", numbered("r-platform", 1, 40)...)
	h := started(t, f, 90, 20)
	h.key("tab")
	h.key("pgup")
	top := h.m.topKey()
	f.addStored("r-platform", numbered("r-platform", 41, 42)...)
	m := f.stored["r-platform"][41]
	h.send(eventMsg{ipc.Event{Kind: ipc.KindMessage, Message: &m}})
	if h.m.topKey() != top {
		t.Errorf("top moved from %s to %s", top, h.m.topKey())
	}
	mustContain(t, h.view(), "↓ 2 new")
	mustNotContain(t, h.view(), "line 42.")
	h.key("end")
	mustContain(t, h.view(), "line 42.")
	mustNotContain(t, h.view(), "new")

	// At the bottom, new messages scroll into view.
	f.addStored("r-platform", numbered("r-platform", 43, 43)...)
	h.send(eventMsg{ipc.Event{Kind: ipc.KindMessage, Message: &m}})
	mustContain(t, h.view(), "line 43.")
}

func TestLongHistoryRereadKeepsOlderPages(t *testing.T) {
	f := fixture()
	f.outbox = nil
	f.setStored("r-platform", numbered("r-platform", 1, 120)...)
	h := started(t, f, 90, 20)
	h.key("tab")
	h.key("home") // loads 21..70: 21..120 held
	m := f.stored["r-platform"][0]
	h.send(eventMsg{ipc.Event{Kind: ipc.KindMessage, Message: &m}})
	if n := f.callCount("messages r-platform 0 100"); n != 1 {
		t.Errorf("a re-read should cover every held message: %v", f.calls)
	}
	if len(h.m.thread.stored) != 100 {
		t.Errorf("held %d messages after the re-read, want 100", len(h.m.thread.stored))
	}
}
