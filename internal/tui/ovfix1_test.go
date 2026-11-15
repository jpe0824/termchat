package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jpe0824/termchat/internal/ipc"
)

// noPanic runs fn and fails the test, instead of the binary, on a panic.
func noPanic(t *testing.T, what string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("%s panicked: %v", what, r)
		}
	}()
	fn()
}

// searchWith opens search over results and types query, results shown.
func searchWith(t *testing.T, w int, query string, texts ...string) (*fakeBackend, *tester) {
	t.Helper()
	f := peopleFixture()
	var res []ipc.Message
	for i, text := range texts {
		res = append(res, msg("r-platform", int64(i+1), 2, "dana", text))
	}
	f.ov.results = map[string][]ipc.Message{query: res}
	h := started(t, f, w, 24)
	h.key("/")
	noPanic(t, "typing "+query, func() {
		h.typeText(query)
		h.fireSearch()
		_ = h.view()
	})
	return f, h
}

// checkRows checks every result row drawn at width w: no panic, valid
// UTF-8, no wider than w, and a marked part that is whole characters
// equal, ignoring case, to a word of the query.
func checkRows(t *testing.T, h *tester, query string, w int) {
	t.Helper()
	noPanic(t, fmt.Sprintf("rows for %q at %d", query, w), func() {
		for _, r := range h.m.ovRows() {
			pre, mark, post := clipRow(r, w)
			line := h.m.rowLine(r, false, w+2)
			switch {
			case !utf8.ValidString(pre + mark + post), !utf8.ValidString(line):
				t.Errorf("%q at %d: invalid UTF-8: %q %q %q / %q", query, w, pre, mark, post, line)
			case lipgloss.Width(pre+mark+post) > w:
				t.Errorf("%q at %d: %d cells: %q", query, w, lipgloss.Width(pre+mark+post), pre+mark+post)
			case ansi.Strip(line) != fit("  "+pre+mark+post, w+2) && ansi.Strip(line) != "  "+pre+mark+post:
				t.Errorf("%q at %d: drawn %q, want %q", query, w, ansi.Strip(line), "  "+pre+mark+post)
			}
			if mark == "" {
				continue
			}
			ok := false
			for _, word := range strings.Fields(query) {
				ok = ok || strings.EqualFold(mark, word)
			}
			if !ok {
				t.Errorf("%q at %d: marked %q, which is no word of the query", query, w, mark)
			}
		}
	})
}

func TestSearchHighlightNeverSplitsACharacter(t *testing.T) {
	// The reviewer's case: results for "o" stay up while a Kelvin sign is
	// typed over them.
	_, h := searchWith(t, 90, "o", "ok, back at the desk")
	noPanic(t, "typing a Kelvin sign", func() {
		h.typeText("\u212a")
		_ = h.view()
	})
	checkRows(t, h, "o\u212a", 60)

	for _, c := range []struct{ query, text string }{
		{"\u212a", "ok"},
		{"\u212a", "pack it up, ok"},
		{"k", "\u212aelvin"},
		{"\u0130", "is it in"},
		{"i", "\u0130stanbul"},
		{"\u00df", "strasse, gro\u00df"},
		{"ss", "gro\u00df"},
		{"e", "cafe\u0301 and more"},
		{"\U0001F44D", "ok \U0001F468\u200D\U0001F469\u200D\U0001F467 \U0001F44D"},
		{"\U0001F468", "family \U0001F468\u200D\U0001F469\u200D\U0001F467"},
		{"\u2126", "\u03c9 end"},
		{"\u212b", "\u00e5ngstr\u00f6m"},
		{"a query longer than the text", "short"},
	} {
		_, h := searchWith(t, 90, c.query, c.text)
		for _, w := range []int{4, 9, 20, 58} {
			checkRows(t, h, c.query, w)
		}
	}
	_, h = searchWith(t, 90, "", "anything")
	checkRows(t, h, "", 20)
}

func TestSearchRowClipsByCells(t *testing.T) {
	texts := []string{
		"meter at the start of the text",
		"in the middle the meter sits",
		strings.Repeat("x", 32) + " meter\u2026",
		strings.Repeat("y", 20) + " meter reading",
		strings.Repeat("z", 80) + " meter",
		"\u6f22\u5b57\u6f22\u5b57 meter \u6f22\u5b57\u6f22\u5b57\u6f22\u5b57\u6f22\u5b57",
	}
	_, h := searchWith(t, 90, "meter", texts...)
	if len(h.m.ovRows()) != len(texts) {
		t.Fatalf("rows %q", rowTexts(h))
	}
	for w := 3; w <= 70; w++ {
		checkRows(t, h, "meter", w)
	}
	for _, r := range h.m.ovRows() {
		for w := 3; w <= 70; w++ {
			pre, mark, post := clipRow(r, w)
			plain := pre + mark + post
			if lipgloss.Width(r.text) <= w && plain != r.text {
				t.Errorf("at %d %q fits but is drawn %q", w, r.text, plain)
			}
			if lipgloss.Width(r.text) > w && !strings.Contains(plain, "\u2026") {
				t.Errorf("at %d %q is cut without \u2026: %q", w, r.text, plain)
			}
		}
	}
	// The reviewer's frame at 90 columns.
	f := peopleFixture()
	f.ov.results = map[string][]ipc.Message{"meter": {msg("r-platform", 1, 2, "dana", strings.Repeat("x", 32)+" meter\u2026")}}
	h = started(t, f, 90, 24)
	h.key("/")
	h.typeText("meter")
	h.fireSearch()
	for i, l := range strings.Split(h.raw(), "\n") {
		if !utf8.ValidString(l) {
			t.Errorf("frame row %d is not valid UTF-8: %q", i, l)
		}
	}
	widthsOK(t, h, 90, 24)
}

// --- search results in a conversation the user has left ---

func TestSearchResultInALeftConversation(t *testing.T) {
	f := peopleFixture()
	f.ov.results = map[string][]ipc.Message{"plan": {msg("r-gone", 3, 2, "sam", "the plan for friday")}}
	h := started(t, f, 90, 24)
	h.key("/")
	h.typeText("plan")
	h.fireSearch()
	rows := rowTexts(h)
	if len(rows) != 1 || !strings.Contains(rows[0], "left") || !strings.Contains(rows[0], "the plan for friday") {
		t.Fatalf("rows %q, want the result marked left", rows)
	}
	f.resetCalls()
	h.enter()
	if h.m.ov == nil || h.m.openID != "r-platform" {
		t.Fatalf("enter: overlay %q open %q", ovState(h), h.m.openID)
	}
	mustContain(t, ovFlat(h), "you are no longer in this conversation")
	h.key("esc")
	h.key("i")
	h.typeText("hello")
	h.enter()
	for _, c := range f.writes("messages ", "send ", "markread ", "opendm ", "room ") {
		if strings.Contains(c, "r-gone") {
			t.Errorf("a call for the left conversation: %q", c)
		}
	}
}

// --- the main view's keys come from the key table ---

func TestMainKeysDispatchFromTheTable(t *testing.T) {
	// Each key, with its row taken out of the table, does nothing.
	for _, c := range []struct {
		key   string
		check func(h *tester) bool // reports the key acted
	}{
		{"q", func(h *tester) bool { return h.quit }},
		{"tab", func(h *tester) bool { return h.m.focus != focusList }},
		{"/", func(h *tester) bool { return h.m.ov != nil }},
		{"ctrl+k", func(h *tester) bool { return h.m.ov != nil }},
		{"enter", func(h *tester) bool { return h.m.focus == focusInput }},
		{"?", func(h *tester) bool { return h.m.help }},
		{"j", func(h *tester) bool { return h.m.cursorID() != "r-platform" }},
	} {
		h := started(t, peopleFixture(), 90, 20)
		h.key(c.key)
		if !c.check(h) {
			t.Fatalf("%s did nothing with the table intact", c.key)
		}
		restore := withoutKey(c.key)
		h = started(t, peopleFixture(), 90, 20)
		h.key(c.key)
		acted := c.check(h)
		restore()
		if acted {
			t.Errorf("%s acted with no row for it in the table", c.key)
		}
	}
}

// withoutKey takes key out of every main-view row of keyTable until the
// returned func puts it back.
func withoutKey(key string) func() {
	saved := slices.Clone(keyTable)
	for gi := range keyTable[:3] {
		binds := slices.Clone(keyTable[gi].binds)
		for bi := range binds {
			binds[bi].match = slices.DeleteFunc(slices.Clone(binds[bi].match), func(k string) bool { return k == key })
		}
		keyTable[gi].binds = binds
	}
	return func() { copy(keyTable, saved) }
}

// --- pickers act only on a highlighted person ---

func TestPickersNeedText(t *testing.T) {
	f := peopleFixture()
	h := started(t, f, 90, 24)
	h.key("ctrl+n")
	h.enter()
	h.enter()
	if got := f.writes(roomWrites...); got != nil || h.m.ov == nil {
		t.Errorf("enter in an empty handle field: writes %q overlay %q", got, ovState(h))
	}
	h.typeText("pri")
	mustContain(t, ovFlat(h), "⏎ message priya")

	h = deploysPanel(t, f)
	f.resetCalls()
	h.key("a")
	h.enter()
	if got := f.writes(roomWrites...); got != nil {
		t.Errorf("enter in an empty invite field wrote %q", got)
	}
	mustNotContain(t, ovFlat(h), "⏎ invite ")
	h.typeText("da")
	mustContain(t, ovFlat(h), "⏎ invite dana")
	h.enter()
	if got := f.writes(roomWrites...); !slices.Equal(got, []string{"room invite r-deploys  dana false"}) {
		t.Errorf("writes %q", got)
	}
}

// --- errors are not lost ---

func TestFailureAfterTheOverlayClosedIsShown(t *testing.T) {
	f := peopleFixture()
	f.failWith("room", errors.New("room name \x1b[31mtaken"))
	h := opened(t, f, 90, 24)
	h.key("ctrl+n")
	h.key("down")
	h.enter()
	h.typeText("newroom")
	h.hold = true
	h.enter()
	h.hold = false
	held := h.held
	h.held = nil
	h.key("esc")
	h.key("esc")
	if h.m.ov != nil {
		t.Fatalf("esc did not close: %q", ovState(h))
	}
	for _, c := range held {
		h.deliver(c())
	}
	mustContain(t, flat(h), "can't create the room: room name taken")
	if strings.Contains(h.raw(), "\x1b[31m") {
		t.Error("the error was not sanitised")
	}
}

func TestPeopleRefreshFailureIsShown(t *testing.T) {
	f := peopleFixture()
	h := started(t, f, 90, 24)
	h.key("ctrl+k")
	h.key("esc")
	f.failWith("people", fmt.Errorf("%w: server unreachable", ipc.ErrNotConnected))
	h.key("ctrl+k")
	mustContain(t, ovFlat(h), "people list not refreshed")
	if !slices.Contains(rowTexts(h), "@priya  Priya Patel — new chat") {
		t.Errorf("the cached people are gone: %q", rowTexts(h))
	}
}

func TestManageFormsRecheckOnEnter(t *testing.T) {
	for _, step := range []string{"a", "n"} {
		f := peopleFixture()
		h := deploysPanel(t, f)
		h.key(step)
		h.key("ctrl+u")
		h.typeText("dana")
		// The room's creator changes while the form is open.
		f.mu.Lock()
		f.convs[5].Creator = "sam"
		f.mu.Unlock()
		h.send(eventMsg{ev: ipc.Event{Kind: ipc.KindConv, Conv: &ipc.ConvView{}}})
		f.resetCalls()
		h.enter()
		if got := f.writes(roomWrites...); got != nil {
			t.Errorf("%s: wrote %q", step, got)
		}
		mustContain(t, ovFlat(h), "you can no longer manage this room")
	}
}

func TestPasteUnderHelpOverAnOverlayShowsNothing(t *testing.T) {
	h := deploysPanel(t, peopleFixture())
	h.key("?")
	if !h.m.help {
		t.Fatal("? did not open help")
	}
	h.paste("hello")
	pause(h, time.Millisecond)
	mustNotContain(t, flat(h), "paste ignored", "press i to type", "press enter")
}
