package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
)

func TestSidebarOrderAndUnread(t *testing.T) {
	h := started(t, fixture(), 90, 20)
	lines := h.lines()
	var rows []string
	for _, l := range lines[1:12] {
		rows = append(rows, strings.TrimSpace(strings.Split(l, "│")[1]))
	}
	want := []string{"Direct", "● dana           2", "sam", "ci-bot", "Rooms", "● # platform     5",
		"# deploys", "# random", "", "", ""}
	for i := range want {
		if rows[i] != want[i] {
			t.Errorf("sidebar row %d = %q, want %q", i, rows[i], want[i])
		}
	}
}

func TestSidebarTruncatesLongNames(t *testing.T) {
	f := fixture()
	f.convs = append(f.convs, room("r-long", "a-very-long-room-name-indeed", false, "sam", nil, 3, -400))
	h := started(t, f, 90, 20)
	_, l := lineWith(h.lines(), "a-very")
	side := strings.Split(l, "│")[1]
	if !strings.Contains(side, "…") || !strings.HasSuffix(strings.TrimRight(side, " "), "3") {
		t.Errorf("long name row = %q, want it cut with … and the count kept", side)
	}
}

func TestSidebarSelectionVisibleWithoutColour(t *testing.T) {
	h := started(t, fixture(), 90, 20)
	h.key("esc") // from the input to the chat list
	raw := strings.Split(h.raw(), "\n")
	_, row := lineWith(raw, "# platform     5")
	if !strings.Contains(row, "\x1b[7m") && !strings.Contains(row, ";7m") {
		t.Errorf("selected row is not in reverse video: %q", row)
	}
	_, other := lineWith(raw, "# deploys")
	if strings.Contains(other, "7m") {
		t.Errorf("unselected row is highlighted: %q", other)
	}
}

func TestOpenConversationWithEnterAndWithClick(t *testing.T) {
	h := started(t, fixture(), 90, 20)
	if h.m.openID != "r-platform" || h.m.cursorID() != "r-platform" {
		t.Fatalf("first open = %q, cursor %q; want the most recently active", h.m.openID, h.m.cursorID())
	}
	h.key("esc") // from the input to the chat list
	h.key("up")
	h.key("up")
	h.key("k")
	if h.m.cursorID() != "dm-dana" {
		t.Fatalf("cursor after up up k = %q", h.m.cursorID())
	}
	if h.m.openID != "r-platform" {
		t.Errorf("moving must not open")
	}
	h.key("enter")
	if h.m.openID != "dm-dana" {
		t.Fatalf("enter opened %q", h.m.openID)
	}
	mustContain(t, h.view(), "got a minute?")
	h.key("esc")
	h.key("j")
	h.key("down")
	if h.m.cursorID() != "dm-ci" {
		t.Errorf("cursor after j down = %q", h.m.cursorID())
	}
	h.click(4, 6) // "# platform"
	if h.m.openID != "r-platform" || h.m.cursorID() != "r-platform" {
		t.Errorf("click opened %q cursor %q", h.m.openID, h.m.cursorID())
	}
	mustContain(t, h.view(), "did the meter import")
}

func TestIncomingMessageAppearsLive(t *testing.T) {
	f := fixture()
	h := started(t, f, 90, 20)
	f.addStored("r-platform", msg("r-platform", 7, 46, "dana", "thanks!"))
	f.convs[3].Unread = 6
	m7 := msg("r-platform", 7, 46, "dana", "thanks!")
	h.send(eventMsg{ipc.Event{Kind: ipc.KindMessage, Message: &m7}})
	mustContain(t, h.view(), "thanks!", "# platform     6")

	f.addStored("dm-sam", msg("dm-sam", 1, 47, "sam", "ping"))
	f.convs[0].Unread, f.convs[0].LastAt = 1, at(47)
	cv := f.convs[0]
	h.send(eventMsg{ipc.Event{Kind: ipc.KindConv, Conv: &cv}})
	_, row := lineWith(h.lines(), "sam ")
	if !strings.Contains(row, "● sam") || !strings.Contains(row, "1") {
		t.Errorf("sam row = %q, want unread 1", row)
	}
	// sam is now the most recent DM.
	i, _ := lineWith(h.lines(), "● sam")
	j, _ := lineWith(h.lines(), "● dana")
	if i > j {
		t.Errorf("sam should be above dana after the new message")
	}
	mustNotContain(t, h.view(), "ping")
	if n := f.callCount("messages dm-sam"); n != 0 {
		t.Errorf("an event for a closed conversation re-read its thread %d times", n)
	}
}

func TestEventsAreHintsAndReadsCoalesce(t *testing.T) {
	f := fixture()
	h := newTester(t, f)
	h.size(90, 20)
	chats := f.callCount("chats")
	// Hold the re-read's answer: queue three events while one read is out.
	m := h.m
	cmd1 := m.requestChats()
	if cmd1 == nil {
		t.Fatal("first request should start a read")
	}
	if m.requestChats() != nil || m.requestChats() != nil {
		t.Error("a second request while one is in flight must queue, not start another")
	}
	h.run(cmd1)
	if got := f.callCount("chats") - chats; got != 2 {
		t.Errorf("chats reads = %d, want 2 (the one in flight and one queued)", got)
	}
	// The payload is not trusted: an event carrying stale data still re-reads.
	stale := f.convs[3]
	stale.Unread = 99
	h.send(eventMsg{ipc.Event{Kind: ipc.KindConv, Conv: &stale}})
	mustNotContain(t, h.view(), "99")
}

func TestOutboxEventRereadsOpenThread(t *testing.T) {
	f := fixture()
	h := started(t, f, 90, 20)
	mustContain(t, h.view(), "sending…")
	f.outbox["r-platform"][0].Status = ipc.MsgFailed
	ob := f.outbox["r-platform"][0]
	h.send(eventMsg{ipc.Event{Kind: ipc.KindOutbox, Message: &ob, Outbox: &ipc.OutboxItem{MsgID: "o1", ConvID: "r-platform"}}})
	mustContain(t, h.view(), "failed — retry?")
	mustNotContain(t, h.view(), "sending…")
}

func TestStatusBarStates(t *testing.T) {
	f := fixture()
	h := started(t, f, 100, 20)
	top := func() string { return h.lines()[0] }
	if !strings.Contains(top(), "team.example ● connected ┐") || !strings.HasPrefix(top(), "┌ termchat ") {
		t.Errorf("top bar = %q", top())
	}
	for _, c := range []struct {
		st   ipc.Status
		want string
	}{
		{ipc.Status{Account: "jason", Conn: ipc.StateConnecting}, "○ connecting"},
		{ipc.Status{Account: "jason", Conn: ipc.StateOffline}, "○ offline"},
		{ipc.Status{Account: "jason", Conn: ipc.StateNotAuthorised}, "! not authorised"},
		{ipc.Status{Account: "jason", Conn: ipc.StateConnected, Syncing: true}, "syncing ● connected"},
	} {
		st := c.st
		h.send(eventMsg{ipc.Event{Kind: ipc.KindStatus, Status: &st}})
		if !strings.Contains(top(), c.want) {
			t.Errorf("state %v: top bar = %q, want %q", c.st, top(), c.want)
		}
	}
	h.send(backendDownMsg{err: ipc.ErrClosed})
	if !strings.Contains(top(), "! background process not running") {
		t.Errorf("down: top bar = %q", top())
	}
	h.send(subscribedMsg{b: f})
	if !strings.Contains(top(), "● connected") {
		t.Errorf("back up: top bar = %q", top())
	}
}

func TestReadFailureWithClosedMarksProcessDown(t *testing.T) {
	f := fixture()
	h := started(t, f, 100, 20)
	f.chatsErr = ipc.ErrShuttingDown
	h.send(resyncMsg{})
	if !strings.Contains(h.lines()[0], "! background process not running") {
		t.Errorf("top bar = %q", h.lines()[0])
	}
}

func TestHelpOverlayListsWorkingKeys(t *testing.T) {
	h := started(t, fixture(), 90, 24)
	h.key("esc")
	h.key("?")
	v := h.view()
	mustContain(t, v, "tab", "pgup", "enter", "quit")
	mustNotContain(t, v, "ctrl+k", "ctrl+n", "/ search", "did the meter import")
	h.key("?")
	mustContain(t, h.view(), "did the meter import")
	h.key("?")
	h.key("esc")
	mustContain(t, h.view(), "did the meter import")
}

func TestFooterShowsOnlyWorkingKeys(t *testing.T) {
	h := started(t, fixture(), 120, 20)
	h.key("esc")
	foot := h.lines()[19]
	mustContain(t, foot, "↑↓ move", "⏎ open", "tab pane", "? help")
	mustNotContain(t, foot, "ctrl+k", "ctrl+n", "search")
}

func TestQuitKeys(t *testing.T) {
	h := started(t, fixture(), 90, 20)
	h.key("esc")
	h.key("q")
	if !h.quit {
		t.Error("q did not quit")
	}
	h = started(t, fixture(), 90, 20)
	h.key("ctrl+c")
	if !h.quit {
		t.Error("ctrl+c did not quit")
	}
}

func TestTabSwitchesFocusAndArrowsScrollThread(t *testing.T) {
	h := started(t, fixture(), 90, 20)
	h.key("esc")
	if h.m.focus != focusList {
		t.Fatalf("focus after esc = %v", h.m.focus)
	}
	h.key("tab")
	if h.m.focus != focusThread {
		t.Fatalf("tab focus = %v", h.m.focus)
	}
	h.key("up")
	if h.m.cursorID() != "r-platform" {
		t.Errorf("up with the thread focused moved the chat list to %q", h.m.cursorID())
	}
	h.key("tab")
	h.key("tab")
	if h.m.focus != focusList {
		t.Errorf("third tab focus = %v", h.m.focus)
	}
}

func TestEscapeSequencesInert(t *testing.T) {
	f := fixture()
	evil := msg("r-platform", 7, 46, "mallory", "hi\x1b[2Jthere\x1b]0;pwned\x07\x9b31m")
	evil.AuthorName = "mal\x1b[31mlory"
	f.addStored("r-platform", evil)
	f.convs[3].Name = "plat\x1b[2Jform"
	f.convs = append(f.convs, room("r-x", "x\x1b]52;c;ZXZpbA==\x07", false, "e\x1b[1mvil", []string{"e\x1b[Avil"}, 0, -400))
	f.status.Me.Handle = "jason"
	h := started(t, f, 120, 30)
	raw := h.raw()
	for _, bad := range []string{"\x1b[2J", "\x1b]0;", "\x1b]52", "\x9b", "\x1b[A", "\x1b[31mlory"} {
		if strings.Contains(raw, bad) {
			t.Errorf("frame carries %q from outside text", bad)
		}
	}
	mustContain(t, h.view(), "hithere", "mallory", "platform")
}

func TestNoAccountErrorFromChats(t *testing.T) {
	f := fixture()
	f.status = ipc.Status{}
	f.chatsErr = ipc.ErrNoAccount
	h := started(t, f, 100, 30)
	mustContain(t, h.view(), "termchat join <host:port> --invite <code>")
}

func TestThreadErrorIsShownSanitised(t *testing.T) {
	f := fixture()
	f.msgErr = errors.New("boom\x1b[2J")
	h := started(t, f, 90, 20)
	mustContain(t, h.view(), "could not load messages: boom")
	if strings.Contains(h.raw(), "\x1b[2J") {
		t.Error("error text not sanitised")
	}
}

func TestDMNamedByPeer(t *testing.T) {
	f := fixture()
	f.convs = []ipc.ConvView{{Conversation: model.Conversation{ID: "d", Kind: model.ConvDM,
		Members: []string{"jason", "dana"}}, LastAt: at(1)}}
	f.setStored("d")
	h := started(t, f, 90, 20)
	_, l := lineWith(h.lines(), "dana")
	if !strings.Contains(l, "│ dana") {
		t.Errorf("DM row = %q", l)
	}
	mustContain(t, h.view(), "no messages yet")
}
