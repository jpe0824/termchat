package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
)

// writeErrs are the errors the fake's write calls return, by call name.
type writeErrs map[string]error

func (f *fakeBackend) failWith(op string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errs == nil {
		f.errs = writeErrs{}
	}
	f.errs[op] = err
}

func (f *fakeBackend) errFor(op string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.errs[op]
}

func (f *fakeBackend) Send(conv, text, source string) (string, error) {
	f.record(fmt.Sprintf("send %s %q %s", conv, text, source))
	if err := f.errFor("send"); err != nil {
		return "", err
	}
	return "new-id", nil
}

func (f *fakeBackend) Edit(conv, msgID, text string) error {
	f.record(fmt.Sprintf("edit %s %s %q", conv, msgID, text))
	return f.errFor("edit")
}

func (f *fakeBackend) Delete(conv, msgID string) error {
	f.record(fmt.Sprintf("delete %s %s", conv, msgID))
	return f.errFor("delete")
}

func (f *fakeBackend) Retry(msgID string) error {
	f.record("retry " + msgID)
	return f.errFor("retry")
}

func (f *fakeBackend) Discard(msgID string) error {
	f.record("discard " + msgID)
	return f.errFor("discard")
}

func (f *fakeBackend) Outbox(conv string) ([]ipc.OutboxItem, error) {
	f.record("outbox " + conv)
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reasons[conv]), f.errs["outbox"]
}

func (f *fakeBackend) MarkRead(conv string) error {
	f.record("markread " + conv)
	return f.errFor("markread")
}

func (f *fakeBackend) SetFocus(conv string, focused bool) error {
	f.record(fmt.Sprintf("setfocus %s %v", conv, focused))
	return f.errFor("setfocus")
}

// writes is the recorded calls that start with one of the prefixes.
func (f *fakeBackend) writes(prefixes ...string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		for _, p := range prefixes {
			if strings.HasPrefix(c, p) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// sends is the recorded Send calls.
func (f *fakeBackend) sends() []string { return f.writes("send ") }

func (f *fakeBackend) resetCalls() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

// typeText types s one character at a time, as a terminal sends keys.
func (h *tester) typeText(s string) {
	h.t.Helper()
	graphemes(s, func(g string, _ int) bool {
		switch g {
		case " ":
			h.send(tea.KeyPressMsg{Code: tea.KeySpace, Text: " "})
		case "\n":
			h.send(tea.KeyPressMsg{Code: tea.KeyEnter})
		case "\t":
			h.send(tea.KeyPressMsg{Code: tea.KeyTab})
		case "\x1b":
			h.send(tea.KeyPressMsg{Code: tea.KeyEscape})
		default:
			r := []rune(g)
			code := r[0]
			if len(r) > 1 {
				code = tea.KeyExtended
			}
			h.send(tea.KeyPressMsg{Code: code, Text: g})
		}
		return true
	})
}

// paste delivers s as a bracketed paste.
func (h *tester) paste(s string) { h.t.Helper(); h.send(tea.PasteMsg{Content: s}) }

// settle fires the pending-enter timers, as the clock would once no more
// input follows.
func (h *tester) settle() {
	h.t.Helper()
	var keep []tick
	ticks := h.ticks
	h.ticks = nil
	for _, tk := range ticks {
		if _, ok := tk.msg.(enterMsg); ok {
			h.send(tk.msg)
		} else {
			keep = append(keep, tk)
		}
	}
	h.ticks = append(keep, h.ticks...)
}

// enter presses enter and lets it act.
func (h *tester) enter() { h.t.Helper(); h.key("enter"); h.settle() }

// fireNoticeTicks fires the timers that clear notices, with their delays.
func (h *tester) fireNoticeTicks() (delays []time.Duration) {
	h.t.Helper()
	var keep []tick
	ticks := h.ticks
	h.ticks = nil
	for _, tk := range ticks {
		if _, ok := tk.msg.(noticeExpiredMsg); ok {
			delays = append(delays, tk.d)
			h.send(tk.msg)
		} else {
			keep = append(keep, tk)
		}
	}
	h.ticks = append(keep, h.ticks...)
	return delays
}

// draft is the open conversation's input text.
func (h *tester) draft() string { return h.m.draftText(h.m.openID) }

// row returns the screen row holding sub, failing the test without one.
func (h *tester) row(sub string) int {
	h.t.Helper()
	i, _ := lineWith(h.lines(), sub)
	if i < 0 {
		h.t.Fatalf("no row with %q:\n%s", sub, h.view())
	}
	return i
}

// markReadOn turns on config mark_read.
func markReadOn(o *Options) { o.MarkRead = true }

// opened is started with the conversation shown at launch opened by enter,
// so the input has focus.
func opened(t *testing.T, b Backend, w, hgt int, mod ...func(*Options)) *tester {
	t.Helper()
	h := newTester(t, b, mod...)
	h.size(w, hgt)
	h.key("enter")
	return h
}
