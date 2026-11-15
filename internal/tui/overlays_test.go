package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/notify"
)

// --- jump ---

func TestJumpFiltersAndOpens(t *testing.T) {
	f := peopleFixture()
	h := started(t, f, 90, 24)
	h.key("ctrl+k")
	if ovState(h) != fmt.Sprintf("%d/0", ovJump) {
		t.Fatalf("ctrl+k: overlay %q", ovState(h))
	}
	want := []string{"# platform", "@dana  Dana Scully", "# deploys", "@sam  Sam Lee", "@ci-bot  CI [agent]",
		"# random", "@mo — new chat", "@priya  Priya Patel — new chat"}
	if got := rowTexts(h); !slices.Equal(got, want) {
		t.Errorf("empty field rows:\n got %q\nwant %q", got, want)
	}
	mustContain(t, h.view(), "Jump to", "@priya  Priya Patel — new chat")

	h.typeText("dep")
	if got := rowTexts(h); !slices.Equal(got, []string{"# deploys"}) {
		t.Errorf("dep: %q", got)
	}
	h.key("backspace")
	h.key("backspace")
	h.key("backspace")
	h.typeText("SCUL")
	if got := rowTexts(h); !slices.Equal(got, []string{"@dana  Dana Scully"}) {
		t.Errorf("SCUL matches display names case-insensitively: %q", got)
	}
	h.key("ctrl+u")
	h.typeText("zzz")
	if rowTexts(h) != nil {
		t.Errorf("zzz: %q", rowTexts(h))
	}
	mustContain(t, h.view(), "no matches")

	h.key("ctrl+u")
	h.typeText("dep")
	f.resetCalls()
	h.enter()
	if h.m.ov != nil || h.m.openID != "r-deploys" || h.m.focus != focusInput {
		t.Fatalf("enter: overlay %q open %q focus %v", ovState(h), h.m.openID, h.m.focus)
	}
	if got := f.writes(roomWrites...); got != nil {
		t.Errorf("opening a held conversation called %q", got)
	}

	// A person with no DM yet: enter creates it.
	h.key("ctrl+k")
	h.typeText("pri")
	h.key("down")
	h.key("up")
	h.enter()
	if got := f.writes(roomWrites...); !slices.Equal(got, []string{"opendm priya"}) {
		t.Errorf("writes = %q, want one OpenDM", got)
	}
	if h.m.ov != nil || h.m.openID != "dm-priya" {
		t.Errorf("after OpenDM: overlay %q open %q", ovState(h), h.m.openID)
	}
}

func TestJumpOpenDMErrorStaysInline(t *testing.T) {
	f := peopleFixture()
	f.failWith("opendm", fmt.Errorf("%w: server unreachable", ipc.ErrNotConnected))
	h := started(t, f, 90, 24)
	h.key("ctrl+k")
	h.typeText("mo")
	h.enter()
	if h.m.ov == nil || h.m.ov.field.text != "mo" {
		t.Fatalf("overlay %q closed or lost its text", ovState(h))
	}
	mustContain(t, h.view(), "can't open the chat while offline")
	if h.m.openID != "r-platform" {
		t.Errorf("open %q", h.m.openID)
	}
}

// --- new conversation ---

func TestNewDMUnknownHandleShowsError(t *testing.T) {
	f := peopleFixture()
	h := started(t, f, 90, 24)
	h.key("ctrl+n")
	if got := rowTexts(h); !slices.Equal(got, []string{"direct message", "new room", "join an open room"}) {
		t.Fatalf("choices %q", got)
	}
	h.enter()
	if ovState(h) != fmt.Sprintf("%d/%d", ovNew, stepDM) {
		t.Fatalf("enter on direct message: %q", ovState(h))
	}
	h.typeText("nobody")
	h.enter()
	mustContain(t, h.view(), "no one with handle nobody")
	if got := f.writes(roomWrites...); got != nil {
		t.Errorf("an unknown handle called %q", got)
	}
	if h.m.ov == nil || h.m.ov.field.text != "nobody" {
		t.Fatalf("overlay %q, text kept?", ovState(h))
	}

	h.key("ctrl+u")
	h.typeText("pr")
	if got := rowTexts(h); !slices.Equal(got, []string{"@priya  Priya Patel"}) {
		t.Errorf("matching people: %q", got)
	}
	h.enter()
	if got := f.writes(roomWrites...); !slices.Equal(got, []string{"opendm priya"}) {
		t.Errorf("writes %q", got)
	}
	if h.m.ov != nil || h.m.openID != "dm-priya" {
		t.Errorf("overlay %q open %q", ovState(h), h.m.openID)
	}

	// A person the user already has a DM with: no call.
	f.resetCalls()
	h.key("ctrl+n")
	h.enter()
	h.typeText("@Dana")
	h.enter()
	if got := f.writes(roomWrites...); got != nil || h.m.openID != "dm-dana" {
		t.Errorf("existing DM: writes %q open %q", got, h.m.openID)
	}
}

func TestCreateRoomRejectsInvalidName(t *testing.T) {
	f := peopleFixture()
	h := started(t, f, 90, 24)
	h.key("ctrl+n")
	h.key("down")
	h.enter()
	if ovState(h) != fmt.Sprintf("%d/%d", ovNew, stepRoom) {
		t.Fatalf("new room: %q", ovState(h))
	}
	mustContain(t, ovFlat(h), "private room")
	h.typeText("Has Space")
	mustContain(t, ovFlat(h), "invalid name: "+model.NameRule)
	h.enter()
	if got := f.writes(roomWrites...); got != nil {
		t.Errorf("an invalid name called %q", got)
	}
	h.key("ctrl+u")
	h.typeText("newroom")
	mustNotContain(t, ovFlat(h), model.NameRule)
	h.key("tab")
	mustContain(t, ovFlat(h), "open room")
	f.failWith("room", errors.New("room name taken"))
	h.enter()
	if h.m.ov == nil || h.m.ov.field.text != "newroom" {
		t.Fatalf("a refused create closed the overlay or lost its text: %q", ovState(h))
	}
	mustContain(t, ovFlat(h), "can't create the room: room name taken")
	f.failWith("room", nil)
	f.resetCalls()
	h.enter()
	if got := f.writes(roomWrites...); !slices.Equal(got, []string{"room create  newroom  true"}) {
		t.Errorf("writes %q", got)
	}
	if h.m.ov != nil || h.m.openID != "r-newroom" || h.m.focus != focusInput {
		t.Errorf("after create: overlay %q open %q focus %v", ovState(h), h.m.openID, h.m.focus)
	}
}

func TestJoinOpenRoom(t *testing.T) {
	f := peopleFixture()
	h := started(t, f, 90, 24)
	h.key("ctrl+n")
	h.key("down")
	h.key("down")
	h.enter()
	if got := rowTexts(h); !slices.Equal(got, []string{"# design  2 members"}) {
		t.Fatalf("open rooms %q", got)
	}
	h.enter()
	if got := f.writes(roomWrites...); !slices.Equal(got, []string{"room join r-design   false"}) {
		t.Errorf("writes %q", got)
	}
	if h.m.ov != nil || h.m.openID != "r-design" {
		t.Errorf("overlay %q open %q", ovState(h), h.m.openID)
	}
	h.key("ctrl+n")
	h.key("down")
	h.key("down")
	h.enter()
	mustContain(t, h.view(), "no open rooms to join")
}

// --- search ---

func TestSearchOpensAtMessage(t *testing.T) {
	f := peopleFixture()
	f.ov.results = map[string][]ipc.Message{"meter": {msg("r-platform", 1, 2, "dana", "did the meter import finish?")}}
	h := started(t, f, 90, 24)
	h.key("/")
	if ovState(h) != fmt.Sprintf("%d/0", ovSearch) {
		t.Fatalf("/: %q", ovState(h))
	}
	h.typeText("meter")
	if f.callCount("search ") != 0 {
		t.Errorf("searched before the wait: %q", f.writes("search "))
	}
	h.fireSearch()
	if got := f.writes("search "); !slices.Equal(got, []string{`search "meter" 50`}) {
		t.Errorf("search calls %q", got)
	}
	if got := rowTexts(h); !slices.Equal(got, []string{"# platform · dana · did the meter import finish?"}) {
		t.Errorf("results %q", got)
	}
	h.enter()
	if h.m.ov != nil || h.m.openID != "r-platform" || h.m.thread.flash != "s1" {
		t.Fatalf("overlay %q open %q flash %q", ovState(h), h.m.openID, h.m.thread.flash)
	}
	mustContain(t, h.view(), "did the meter import finish?")

	h.key("esc") // the input takes / as text
	h.key("/")
	h.typeText("nothing")
	h.fireSearch()
	mustContain(t, h.view(), "no results")
	h.key("ctrl+u")
	h.fireSearch()
	if rowTexts(h) != nil {
		t.Errorf("empty query rows %q", rowTexts(h))
	}
	mustNotContain(t, h.view(), "no results")
}

func TestSearchOpensOlderThanLoaded(t *testing.T) {
	f := peopleFixture()
	var msgs []ipc.Message
	for i := int64(1); i <= 700; i++ {
		msgs = append(msgs, msg("r-big", i, int(i)-800, "sam", fmt.Sprintf("line %d", i)))
	}
	msgs[2].Text = "the needle is here"
	f.setStored("r-big", msgs...)
	big := room("r-big", "big", true, "sam", []string{"sam", "jason"}, 0, -100)
	big.LastSeq = 700
	f.convs = append(f.convs, big)
	f.ov.results = map[string][]ipc.Message{"needle": {msgs[2]}}
	h := started(t, f, 90, 24)
	h.key("/")
	h.typeText("needle")
	h.fireSearch()
	h.enter()
	if h.m.openID != "r-big" || h.m.thread.stored[0].Seq > 3 || h.m.thread.flash != "s3" {
		t.Fatalf("open %q oldest held %d flash %q", h.m.openID, h.m.thread.stored[0].Seq, h.m.thread.flash)
	}
	mustContain(t, h.view(), "the needle is here")
	// The flash goes after a while.
	for _, tk := range h.ticks {
		if fm, ok := tk.msg.(timerMsg); ok && fm.id == timerFlash {
			h.send(fm)
		}
	}
	if h.m.thread.flash != "" {
		t.Errorf("flash still %q", h.m.thread.flash)
	}
}

func TestStaleSearchResultsDropped(t *testing.T) {
	f := peopleFixture()
	f.ov.results = map[string][]ipc.Message{
		"dep": {msg("r-deploys", 1, 1, "sam", "deploy at noon")},
		"de":  {msg("dm-dana", 1, 40, "dana", "hey dear")},
	}
	h := started(t, f, 90, 24)
	h.key("/")
	h.typeText("de")
	h.fireSearch()
	h.hold = true
	h.typeText("p")
	h.hold = false
	// The answer for "de" arrives after the query became "dep".
	h.send(searchMsg{gen: h.m.ov.gen, seq: h.m.ov.seq - 1, q: "de", msgs: f.ov.results["de"], b: f})
	if got := rowTexts(h); !slices.Equal(got, []string{"@dana · dana · hey dear"}) {
		t.Errorf("rows %q, want the de results kept until dep answers", got)
	}
	h.fireSearch()
	if got := rowTexts(h); !slices.Equal(got, []string{"# deploys · sam · deploy at noon"}) {
		t.Errorf("rows %q", got)
	}
	// A result for a closed overlay is dropped.
	gen, seq := h.m.ov.gen, h.m.ov.seq
	h.key("esc")
	h.send(searchMsg{gen: gen, seq: seq, q: "dep", msgs: f.ov.results["dep"], b: f})
	if h.m.ov != nil {
		t.Errorf("a late result reopened the overlay")
	}
}

// --- room panel ---

func TestRoomPanelHidesManageForNonCreator(t *testing.T) {
	f := peopleFixture()
	h := opened(t, f, 90, 24) // # platform, created by dana
	h.key("ctrl+o")
	if ovState(h) != fmt.Sprintf("%d/%d", ovRoom, stepMembers) {
		t.Fatalf("ctrl+o: %q", ovState(h))
	}
	want := []string{"ci-bot [agent]", "dana", "jason (you)", "sam", "leave the room"}
	if got := rowTexts(h); !slices.Equal(got, want) {
		t.Errorf("rows %q, want %q", got, want)
	}
	v := ovFlat(h)
	mustContain(t, v, "Open room", "created by dana", "l leave")
	mustNotContain(t, v, "a invite", "x remove", "n rename", "invite someone", "rename the room")
	for _, k := range []string{"a", "x", "n"} {
		h.key(k)
		if ovState(h) != fmt.Sprintf("%d/%d", ovRoom, stepMembers) || h.m.ov.ask != nil {
			t.Errorf("%s: %q ask %v", k, ovState(h), h.m.ov.ask)
		}
	}
	// Clicking a member twice does nothing either.
	x, y := ovAt(h, "dana")
	h.click(x, y)
	h.click(x, y)
	if h.m.ov.ask != nil || f.writes(roomWrites...) != nil {
		t.Errorf("a click acted: ask %v writes %q", h.m.ov.ask, f.writes(roomWrites...))
	}
}

func TestRoomPanelManage(t *testing.T) {
	f := peopleFixture()
	h := started(t, f, 90, 24)
	h.key("down") // # deploys, created by jason
	h.key("enter")
	h.key("ctrl+o")
	mustContain(t, ovFlat(h), "a invite", "x remove", "n rename", "l leave", "Private room", "created by jason")
	if got := rowTexts(h); !slices.Equal(got, []string{"jason (you)", "sam", "invite someone", "rename the room", "leave the room"}) {
		t.Errorf("rows %q", got)
	}
	h.key("a")
	h.typeText("da")
	if got := rowTexts(h); !slices.Equal(got, []string{"@dana  Dana Scully"}) {
		t.Errorf("invite matches %q", got)
	}
	h.enter()
	if got := rowTexts(h); !slices.Equal(got, []string{"dana", "jason (you)", "sam", "invite someone",
		"rename the room", "leave the room"}) {
		t.Errorf("after the invite: %q", got)
	}
	h.key("down")
	h.key("down")
	h.key("x")
	mustContain(t, ovFlat(h), "remove sam from # deploys? y/n")
	h.key("y")
	h.key("n")
	h.key("ctrl+u")
	h.typeText("ops")
	h.enter()
	want := []string{"room invite r-deploys  dana false", "room remove r-deploys  sam false",
		"room rename r-deploys ops  false"}
	if got := f.writes(roomWrites...); !slices.Equal(got, want) {
		t.Errorf("writes %q, want %q", got, want)
	}
	h.key("l")
	mustContain(t, ovFlat(h), "leave # ops? y/n")
	h.key("y")
	if got := f.writes("room leave"); !slices.Equal(got, []string{"room leave r-deploys   false"}) {
		t.Errorf("leave %q", got)
	}
	if h.m.ov != nil || h.m.openID == "r-deploys" {
		t.Errorf("after leaving: overlay %q open %q", ovState(h), h.m.openID)
	}
}

func TestRoomPanelInDMAndWide(t *testing.T) {
	f := peopleFixture()
	h := started(t, f, 90, 24)
	h.key("ctrl+k")
	h.typeText("dana")
	h.enter()
	h.key("esc")
	h.key("ctrl+o")
	if h.m.ov != nil {
		t.Errorf("ctrl+o in a DM opened %q", ovState(h))
	}
	mustContain(t, flat(h), "not a room")

	h = opened(t, peopleFixture(), 120, 20)
	h.key("ctrl+o")
	if h.m.ov == nil || !h.m.docked(h.m.geom()) {
		t.Fatalf("wide ctrl+o: %q, docked in the members panel?", ovState(h))
	}
	if r := h.m.ovRect(); r.x != 99 || r.w != 20 {
		t.Errorf("the room panel is drawn at %+v, not in the members panel", r)
	}
	_, row := lineWith(h.lines(), "jason (you)")
	if i := strings.Index(row, "jason (you)"); i < 0 || utf8.RuneCountInString(row[:i]) < 99 {
		t.Errorf("the members panel is not where the room panel is drawn: %q", row)
	}
	mustContain(t, h.view(), "# platform", "did the meter import") // the thread stays in view
}

// --- notifications ---

func TestNotifyEventWritesOSC(t *testing.T) {
	p := ipc.NotifyPayload{ConvID: "dm-dana", ConvName: "dana", Sender: "Dana", Text: "hi; there\x1b]0;x\x07", DM: true}
	for _, variant := range []string{"99", "9", "777"} {
		h := opened(t, fixture(), 90, 20, func(o *Options) { o.NotifyOSC = variant })
		before := h.view()
		h.send(eventMsg{ev: ipc.Event{Kind: ipc.KindNotify, Notify: &p}})
		if want := []string{notify.OSC(variant, p) + "\a"}; !slices.Equal(h.raws, want) {
			t.Errorf("%s: wrote %q, want %q", variant, h.raws, want)
		}
		if h.view() != before {
			t.Errorf("%s: a notify event changed the view", variant)
		}
	}
	for _, cfg := range []string{"off", "auto", "bogus"} {
		h := opened(t, fixture(), 90, 20, func(o *Options) { o.NotifyOSC = cfg })
		h.send(eventMsg{ev: ipc.Event{Kind: ipc.KindNotify, Notify: &p}})
		h.send(eventMsg{ev: ipc.Event{Kind: ipc.KindMessage, Message: &ipc.Message{ConvID: "dm-dana"}}})
		if h.raws != nil {
			t.Errorf("%s: wrote %q", cfg, h.raws)
		}
	}
	h := opened(t, fixture(), 90, 20, func(o *Options) { o.NotifyOSC = "99" })
	h.send(eventMsg{ev: ipc.Event{Kind: ipc.KindMessage, Message: &ipc.Message{ConvID: "dm-dana"}}})
	h.send(eventMsg{ev: ipc.Event{Kind: ipc.KindNotify}})
	if h.raws != nil {
		t.Errorf("wrote %q for events that are not notifications", h.raws)
	}
}

func TestSubscribesAsNotifierOnlyWithAVariant(t *testing.T) {
	for _, c := range []struct {
		cfg  string
		env  map[string]string
		want string
	}{
		{"99", nil, "subscribe true"},
		{"auto", map[string]string{"TERM_PROGRAM": "WezTerm"}, "subscribe true"},
		{"auto", nil, "subscribe false"},
		{"off", map[string]string{"KITTY_WINDOW_ID": "1"}, "subscribe false"},
	} {
		f := fixture()
		_ = newTester(t, f, func(o *Options) {
			o.NotifyOSC = c.cfg
			o.Env = func(k string) string { return c.env[k] }
			o.Send = func(tea.Msg) {}
		})
		waitFor(t, func() bool { return f.callCount("subscribe ") > 0 })
		if f.callCount(c.want) != 1 {
			t.Errorf("%s %v: calls %q, want %s", c.cfg, c.env, f.writes("subscribe "), c.want)
		}
	}
}

// --- mouse ---

func TestOverlayMouseClose(t *testing.T) {
	for _, w := range []int{120, 90, 44, 30} {
		f := peopleFixture()
		h := started(t, f, w, 24)
		before := snapshot(h)
		for _, k := range []string{"ctrl+k", "ctrl+n", "/"} {
			h.key(k)
			if h.m.ov == nil {
				t.Fatalf("%d: %s opened nothing", w, k)
			}
			h.click(1, 0) // the top bar, outside every overlay
			if h.m.ov != nil {
				t.Errorf("%d: a click outside did not close %s", w, k)
			}
			if after := snapshot(h); after.open != before.open || after.focus != before.focus {
				t.Errorf("%d: %s: state %+v, want %+v", w, k, after, before)
			}
		}
		// A click on a row selects it; a second click acts.
		h.key("ctrl+k")
		x, y := ovAt(h, "# deploys")
		h.click(x, y)
		if h.m.ov == nil || h.m.ovSel().id != "c:r-deploys" {
			t.Fatalf("%d: first click: overlay %q", w, ovState(h))
		}
		h.click(x, y)
		if h.m.ov != nil || h.m.openID != "r-deploys" {
			t.Errorf("%d: second click: overlay %q open %q", w, ovState(h), h.m.openID)
		}
	}
}

var _ = time.Second
