package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/jpe0824/termchat/internal/ipc"
)

// feed sends the parts as keys 1 ms apart, one part right behind the other,
// after a pause; no timer fires until the test lets time pass.
func feed(h *tester, parts ...string) {
	h.t.Helper()
	step := h.step
	h.now = h.now.Add(time.Second)
	h.step = time.Millisecond
	for _, p := range parts {
		h.typeText(p)
	}
	h.step = step
}

// soon runs fn with the clock stepping 1 ms, so what it sends lands inside
// the paste gap of the key before.
func soon(h *tester, fn func()) {
	h.t.Helper()
	step := h.step
	h.step = time.Millisecond
	fn()
	h.step = step
}

func countStarts(f *fakeBackend, starts *int) func(*Options) {
	return func(o *Options) {
		o.Start = func(context.Context) (Backend, error) { *starts++; return f, nil }
	}
}

// reread tells the interface the open conversation changed.
func reread(h *tester) {
	h.t.Helper()
	h.send(eventMsg{ipc.Event{Kind: ipc.KindMessage, Message: &ipc.Message{ConvID: "r-platform"}}})
}

// --- r in the disabled input ---

func TestPastedRInDisabledInputStartsNothing(t *testing.T) {
	for _, w := range pasteWidths {
		for _, text := range []string{"query := d.exec(x)\n", "jjenterhi\n", "run it\n"} {
			for cut := 0; cut < len(text); cut++ {
				if cut > 0 && (text[:cut] == "r" || text[cut:] == "r") {
					continue // a lone r cannot be told from a deliberate one
				}
				f, starts := fixture(), 0
				h := opened(t, f, w, 20, countStarts(f, &starts))
				h.send(backendDownMsg{err: ipc.ErrClosed})
				feed(h, text[:cut])
				if cut > 0 {
					pause(h, 40*time.Millisecond)
					soon(h, func() { h.typeText(text[cut:]) })
				} else {
					soon(h, func() { h.typeText(text) })
				}
				pause(h, time.Second)
				if starts != 0 || h.m.focus != focusInput {
					t.Fatalf("width %d %q cut at %d: starts %d focus %v", w, text, cut, starts, h.m.focus)
				}
			}
		}
		f, starts := fixture(), 0
		h := opened(t, f, w, 20, countStarts(f, &starts))
		h.send(backendDownMsg{err: ipc.ErrClosed})
		h.key("r")
		if starts != 1 {
			t.Errorf("width %d: a lone r started %d times", w, starts)
		}
	}
}

// --- a pending key and the selection ---

func TestClickCancelsAPendingKey(t *testing.T) {
	for _, w := range []int{90, 44} {
		for _, k := range []string{"d", "e"} {
			t.Run(fmt.Sprint(w, k), func(t *testing.T) {
				f, h := onOwnMessage(t, w)
				pause(h, time.Second)
				keysAt(h, k)
				soon(h, func() { h.click(w/2, h.row("yes, 10 min ago")) })
				pause(h, time.Second)
				mustNotContain(t, h.view(), "delete this message?")
				if c := h.m.drafts["r-platform"]; c != nil && c.edit != nil {
					t.Errorf("edit started on %q", c.edit.msgID)
				}
				if selectedText(h) != "yes, 10 min ago" || h.m.focus != focusThread {
					t.Errorf("selected %q focus %v", selectedText(h), h.m.focus)
				}
				h.key("y")
				noWrites(t, f)
			})
		}
	}
}

func TestPendingKeyDroppedWhenSelectionChanges(t *testing.T) {
	for _, k := range []string{"d", "e"} {
		f, h := onOwnMessage(t, 90)
		pause(h, time.Second)
		keysAt(h, k)
		h.m.thread.sel = "s2" // yes, 10 min ago
		pause(h, time.Second)
		mustNotContain(t, h.view(), "delete this message?")
		if c := h.m.drafts["r-platform"]; c != nil && c.edit != nil {
			t.Errorf("%s: edit started on %q", k, c.edit.msgID)
		}
		noWrites(t, f)
	}
	// The chat list: enter opens the row it was pressed on, or nothing.
	f := fixture()
	h := opened(t, f, 90, 20)
	h.key("esc")
	pause(h, time.Second)
	keysAt(h, "enter")
	h.m.cursor = 0
	pause(h, time.Second)
	if h.m.openID != "r-platform" || h.m.focus != focusList {
		t.Errorf("open %q focus %v", h.m.openID, h.m.focus)
	}
}

// --- the quit warning ---

func TestQuitWarningFitsShortWindows(t *testing.T) {
	for _, w := range []int{30, 20} {
		for _, hgt := range []int{1, 2, 3, 4, 5} {
			t.Run(fmt.Sprint(w, "x", hgt), func(t *testing.T) {
				h := opened(t, fixture(), 90, 20)
				h.typeText("x")
				h.size(w, hgt)
				h.key("ctrl+c")
				if h.quit {
					t.Fatal("quit")
				}
				mustContain(t, flat(h), "ctrl+c again to quit")
				h.size(90, 20)
				h.key("esc")
				h.size(w, hgt)
				h.key("q")
				mustContain(t, flat(h), "q again to quit")

				// The draft is in another conversation.
				h = opened(t, fixture(), 90, 20)
				h.typeText("x")
				h.click(4, 2) // dana
				h.key("esc")
				h.size(w, hgt)
				h.key("q")
				mustContain(t, flat(h), "q again to quit")
				for _, l := range h.lines() {
					if lipgloss.Width(l) > w {
						t.Errorf("row wider than %d: %q", w, l)
					}
				}
				h.key("q")
				if !h.quit {
					t.Error("the second q did not quit")
				}
			})
		}
	}
	// With room, the full warning.
	h := opened(t, fixture(), 90, 20)
	h.typeText("x")
	h.size(20, 5)
	h.key("ctrl+c")
	mustContain(t, flat(h), "ctrl+c again to quit — unsent drafts will be lost")
}

func bannerDelays(h *tester) (delays []time.Duration) {
	for _, tk := range h.ticks {
		if _, ok := tk.msg.(bannerExpiredMsg); ok {
			delays = append(delays, tk.d)
		}
	}
	return delays
}

func TestQuitWarningEndsWithItsWindow(t *testing.T) {
	h := opened(t, fixture(), 90, 20)
	h.typeText("x")
	h.ticks = nil
	h.key("ctrl+c")
	mustContain(t, flat(h), "ctrl+c again to quit")
	if got := bannerDelays(h); !slices.Equal(got, []time.Duration{3 * time.Second}) {
		t.Errorf("warning timers = %v, want one of 3s", got)
	}
	h.fireTicks()
	mustNotContain(t, flat(h), "again to quit")

	h = opened(t, fixture(), 90, 20)
	h.key("esc")
	h.ticks = nil
	h.paste("hello")
	if got := bannerDelays(h); !slices.Equal(got, []time.Duration{noticeFor}) {
		t.Errorf("paste note timers = %v, want one of %v", got, noticeFor)
	}
}

// --- a question whose message goes away ---

func TestQuestionCancelledWhenItsMessageGoes(t *testing.T) {
	gone := func(f *fakeBackend) {
		f.mu.Lock()
		f.stored["r-platform"] = f.stored["r-platform"][:5]
		f.mu.Unlock()
	}
	deleted := func(f *fakeBackend) {
		f.mu.Lock()
		f.stored["r-platform"][5].Deleted = true
		f.mu.Unlock()
	}
	for name, change := range map[string]func(*fakeBackend){"gone": gone, "deleted": deleted} {
		t.Run(name, func(t *testing.T) {
			f, h := onOwnMessage(t, 90)
			h.key("d")
			mustContain(t, h.view(), "delete this message? y/n")
			change(f)
			reread(h)
			mustNotContain(t, h.view(), "delete this message?")
			mustContain(t, h.view(), "message changed — delete cancelled")
			h.key("y")
			noWrites(t, f)

			// A y already waiting out the gap when the re-read lands.
			f, h = onOwnMessage(t, 90)
			h.key("d")
			pause(h, time.Second)
			keysAt(h, "y")
			change(f)
			soon(h, func() { reread(h) })
			pause(h, time.Second)
			noWrites(t, f)
		})
	}
	// A failed item that is no longer failed, or no longer there.
	for name, change := range map[string]func(*fakeBackend){
		"sending": func(f *fakeBackend) { f.outbox["r-platform"][0].Status = ipc.MsgSending },
		"gone":    func(f *fakeBackend) { f.outbox["r-platform"] = nil },
	} {
		t.Run("discard "+name, func(t *testing.T) {
			f := failedFixture()
			h := opened(t, f, 90, 20)
			h.key("up")
			h.key("x")
			mustContain(t, h.view(), "discard this message? y/n")
			f.resetCalls()
			f.mu.Lock()
			change(f)
			f.mu.Unlock()
			reread(h)
			mustNotContain(t, h.view(), "discard this message?")
			mustContain(t, h.view(), "message changed — discard cancelled")
			h.key("y")
			noWrites(t, f)
		})
	}
	// A re-read that keeps the message keeps the question.
	f, h := onOwnMessage(t, 90)
	h.key("d")
	reread(h)
	mustContain(t, h.view(), "delete this message? y/n")
	h.key("y")
	if got := f.writes("delete "); !slices.Equal(got, []string{"delete r-platform r-platform-m6"}) {
		t.Errorf("deletes = %q", got)
	}
}

// --- where i is offered ---

func TestFootersOfferIOnTheThread(t *testing.T) {
	for _, w := range pasteWidths {
		_, h := onOwnMessage(t, w)
		mustContain(t, footerOf(h), "i type")
		h = started(t, fixture(), w, 20)
		if w < 70 { // the previewed thread has the keyboard
			mustContain(t, footerOf(h), "i type")
		}
		h.key("tab")
		if w >= 70 && !strings.Contains(footerOf(h), "i type") {
			t.Errorf("width %d: thread footer %q", w, footerOf(h))
		}
	}
	h := started(t, fixture(), 90, 30)
	h.key("?")
	mustContain(t, h.view(), "letters type only in the input")
}

// The note a letter leaves names a key that works where the keyboard is.
func TestLetterNoteFollowsFocus(t *testing.T) {
	for _, w := range pasteWidths {
		t.Run(fmt.Sprint(w), func(t *testing.T) {
			h := opened(t, fixture(), w, 20)
			h.key("esc") // the chat list
			h.typeText("h")
			mustContain(t, flat(h), "press enter to open a chat")
			mustNotContain(t, flat(h), "press i to type")
			if h.m.focus != focusList || h.draft() != "" {
				t.Errorf("focus %v draft %q", h.m.focus, h.draft())
			}

			_, h = onOwnMessage(t, w)
			h.typeText("h")
			mustContain(t, flat(h), "press i to type")
			mustNotContain(t, flat(h), "press enter to open a chat")
		})
	}
	h := started(t, fixture(), 44, 20) // the previewed thread
	h.typeText("h")
	mustContain(t, flat(h), "press i to type")
}
