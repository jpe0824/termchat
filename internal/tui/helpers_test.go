package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
)

// fakeBackend serves fixed data from memory and records the calls made.
type fakeBackend struct {
	mu        sync.Mutex
	status    ipc.Status
	statusErr error
	convs     []ipc.ConvView
	chatsErr  error
	stored    map[string][]ipc.Message // ascending seq
	outbox    map[string][]ipc.Message
	msgErr    error
	calls     []string

	subCh  chan ipc.Event
	subErr error
	closed bool
}

func (f *fakeBackend) record(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *fakeBackend) callCount(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeBackend) Status() (ipc.Status, error) {
	f.record("status")
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, f.statusErr
}

func (f *fakeBackend) Chats() ([]ipc.ConvView, []model.Conversation, error) {
	f.record("chats")
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.convs), nil, f.chatsErr
}

func (f *fakeBackend) Messages(conv string, beforeSeq int64, limit int) ([]ipc.Message, error) {
	f.record(fmt.Sprintf("messages %s %d %d", conv, beforeSeq, limit))
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.msgErr != nil {
		return nil, f.msgErr
	}
	var out []ipc.Message
	for _, m := range f.stored[conv] {
		if beforeSeq == 0 || m.Seq < beforeSeq {
			out = append(out, m)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	out = slices.Clone(out)
	if beforeSeq == 0 {
		out = append(out, f.outbox[conv]...)
	}
	return out, nil
}

func (f *fakeBackend) SubscribeUI(_ context.Context, canNotify bool) (<-chan ipc.Event, error) {
	f.record(fmt.Sprintf("subscribe %v", canNotify))
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subCh, f.subErr = make(chan ipc.Event, 16), nil
	return f.subCh, nil
}

// endSub closes the current subscription with why as its reason.
func (f *fakeBackend) endSub(why error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subErr = why
	close(f.subCh)
}

func (f *fakeBackend) sub() chan ipc.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subCh
}

func (f *fakeBackend) SubscriptionErr() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subErr
}

func (f *fakeBackend) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func (f *fakeBackend) setStored(conv string, msgs ...ipc.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stored == nil {
		f.stored = map[string][]ipc.Message{}
	}
	f.stored[conv] = msgs
}

func (f *fakeBackend) addStored(conv string, msgs ...ipc.Message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stored[conv] = append(f.stored[conv], msgs...)
}

func (f *fakeBackend) setConvs(convs ...ipc.ConvView) {
	f.mu.Lock()
	f.convs = convs
	f.mu.Unlock()
}

// base is the fixture's reference time, 2026-11-13 10:00 UTC.
var base = time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)

func at(min int) time.Time { return base.Add(time.Duration(min) * time.Minute) }

func dm(id, peer string, unread, lastMin int) ipc.ConvView {
	return ipc.ConvView{
		Conversation: model.Conversation{ID: id, Kind: model.ConvDM, Members: []string{"jason", peer}},
		Unread:       unread, LastAt: at(lastMin),
	}
}

func room(id, name string, open bool, creator string, members []string, unread, lastMin int) ipc.ConvView {
	return ipc.ConvView{
		Conversation: model.Conversation{ID: id, Kind: model.ConvRoom, Name: name, Open: open,
			Creator: creator, Members: members},
		Unread: unread, LastAt: at(lastMin),
	}
}

func msg(conv string, seq int64, min int, author, text string) ipc.Message {
	return ipc.Message{ConvID: conv, MsgID: fmt.Sprintf("%s-m%d", conv, seq), Author: author,
		AuthorName: author, Seq: seq, At: at(min), Text: text}
}

// fixture is the data set behind the golden frames: the design mockup's
// conversations, with # platform the most recently active.
func fixture() *fakeBackend {
	f := &fakeBackend{status: ipc.Status{Account: "jason", Me: model.Person{Handle: "jason"},
		Conn: ipc.StateConnected}}
	f.convs = []ipc.ConvView{
		dm("dm-sam", "sam", 0, -60),
		room("r-random", "random", true, "sam", []string{"sam", "jason"}, 0, -180),
		dm("dm-dana", "dana", 2, 44),
		room("r-platform", "platform", true, "dana", []string{"dana", "sam", "jason", "ci-bot"}, 5, 45),
		dm("dm-ci", "ci-bot", 0, -120),
		room("r-deploys", "deploys", false, "jason", []string{"jason", "sam"}, 0, -30),
	}
	agent := msg("r-platform", 4, 42, "ci-bot", "build 812 passed")
	agent.Agent = true
	edited := msg("r-platform", 5, 44, "sam", "@jason can you look?")
	edited.Edited = true
	f.setStored("r-platform",
		msg("r-platform", 1, 2, "dana", "did the meter import finish?"),
		msg("r-platform", 2, 3, "jason", "yes, 10 min ago"),
		msg("r-platform", 3, 41, "dana", "great. one row looks off"),
		agent, edited,
		msg("r-platform", 6, 45, "jason", "on it"),
	)
	f.outbox = map[string][]ipc.Message{"r-platform": {{ConvID: "r-platform", MsgID: "o1", Author: "jason",
		AuthorName: "jason", At: at(45), Text: "pushing a fix now", Status: ipc.MsgSending}}}
	f.setStored("dm-dana", msg("dm-dana", 1, 40, "dana", "hey"), msg("dm-dana", 2, 44, "dana", "got a minute?"))
	return f
}

type tick struct {
	d   time.Duration
	msg tea.Msg
}

// tester drives a Model synchronously: every command runs at once and its
// message is fed back, so a fake backend's answers land before send returns.
type tester struct {
	t     *testing.T
	m     *Model
	ticks []tick
	quit  bool
	// hold keeps commands in held instead of running them, so a test can
	// look at the model while a backend call is still out.
	hold bool
	held []tea.Cmd
}

func newTester(t *testing.T, b Backend, mod ...func(*Options)) *tester {
	t.Helper()
	return buildTester(t, false, b, mod...)
}

// heldTester is newTester with every command held, Init's included.
func heldTester(t *testing.T, b Backend, mod ...func(*Options)) *tester {
	t.Helper()
	return buildTester(t, true, b, mod...)
}

func buildTester(t *testing.T, hold bool, b Backend, mod ...func(*Options)) *tester {
	t.Helper()
	h := &tester{t: t, hold: hold}
	opts := Options{
		Version:  "v0.1.0",
		Server:   func() string { return "team.example:2222" },
		Env:      func(k string) string { return map[string]string{"NO_COLOR": "1"}[k] },
		Location: time.UTC,
		Tick: func(d time.Duration, msg tea.Msg) tea.Cmd {
			h.ticks = append(h.ticks, tick{d, msg})
			return nil
		},
	}
	for _, f := range mod {
		f(&opts)
	}
	h.m = New(b, opts)
	t.Cleanup(h.m.Close)
	h.run(h.m.Init())
	return h
}

func (h *tester) run(cmd tea.Cmd) {
	h.t.Helper()
	if cmd == nil {
		return
	}
	if h.hold {
		h.held = append(h.held, cmd)
		return
	}
	h.deliver(cmd())
}

func (h *tester) deliver(msg tea.Msg) {
	h.t.Helper()
	switch msg := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			h.run(c)
		}
	case tea.QuitMsg:
		h.quit = true
	default:
		h.send(msg)
	}
}

func (h *tester) send(msg tea.Msg) {
	h.t.Helper()
	_, cmd := h.m.Update(msg)
	h.run(cmd)
}

// fireTicks delivers every scheduled tick once.
func (h *tester) fireTicks() {
	h.t.Helper()
	ticks := h.ticks
	h.ticks = nil
	for _, tk := range ticks {
		h.send(tk.msg)
	}
}

func (h *tester) size(w, hgt int) { h.t.Helper(); h.send(tea.WindowSizeMsg{Width: w, Height: hgt}) }

func (h *tester) key(s string) { h.t.Helper(); h.send(keyMsg(s)) }

func (h *tester) click(x, y int) {
	h.t.Helper()
	h.send(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
}

func (h *tester) wheel(x, y int, up bool) {
	h.t.Helper()
	b := tea.MouseWheelDown
	if up {
		b = tea.MouseWheelUp
	}
	h.send(tea.MouseWheelMsg{X: x, Y: y, Button: b})
}

// raw is the frame as the terminal receives it; view is the frame with
// styling removed, which is what a colourless terminal shows.
func (h *tester) raw() string     { return h.m.View().Content }
func (h *tester) view() string    { return ansi.Strip(h.raw()) }
func (h *tester) lines() []string { return strings.Split(h.view(), "\n") }

func keyMsg(s string) tea.KeyPressMsg {
	codes := map[string]rune{"up": tea.KeyUp, "down": tea.KeyDown, "enter": tea.KeyEnter,
		"esc": tea.KeyEscape, "tab": tea.KeyTab, "pgup": tea.KeyPgUp, "pgdown": tea.KeyPgDown,
		"home": tea.KeyHome, "end": tea.KeyEnd}
	if c, ok := codes[s]; ok {
		return tea.KeyPressMsg{Code: c}
	}
	if rest, ok := strings.CutPrefix(s, "ctrl+"); ok {
		return tea.KeyPressMsg{Code: []rune(rest)[0], Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}

// started returns a tester already past the opening screen at w×h.
func started(t *testing.T, b Backend, w, hgt int) *tester {
	t.Helper()
	h := newTester(t, b)
	h.size(w, hgt)
	return h
}

func lineWith(lines []string, sub string) (int, string) {
	for i, l := range lines {
		if strings.Contains(l, sub) {
			return i, l
		}
	}
	return -1, ""
}

func mustContain(t *testing.T, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			t.Errorf("frame lacks %q:\n%s", sub, s)
		}
	}
}

func mustNotContain(t *testing.T, s string, subs ...string) {
	t.Helper()
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			t.Errorf("frame has %q:\n%s", sub, s)
		}
	}
}
