package tui

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
)

// starter is a fake Options.Start: it counts calls and answers from res,
// blocking until release is closed when release is set.
type starter struct {
	calls   int
	b       Backend
	err     error
	release chan struct{}
}

func (s *starter) opt(o *Options) {
	o.Start = func(context.Context) (Backend, error) {
		s.calls++
		if s.release != nil {
			<-s.release
		}
		return s.b, s.err
	}
}

func footerOf(h *tester) string { l := h.lines(); return l[len(l)-1] }

func TestStartKeyWhileDown(t *testing.T) {
	f := fixture()
	st := &starter{err: errors.New("boom")}
	h := newTester(t, f, st.opt)
	h.size(100, 20)
	h.key("r")
	if st.calls != 0 || strings.Contains(footerOf(h), "r start") {
		t.Fatalf("connected: r started %d times, footer %q", st.calls, footerOf(h))
	}
	h.send(backendDownMsg{err: ipc.ErrClosed})
	if !strings.Contains(footerOf(h), "r start") {
		t.Errorf("down: footer %q lacks r start", footerOf(h))
	}
	h.key("r")
	if st.calls != 1 {
		t.Fatalf("r while down started %d times, want 1", st.calls)
	}
	if top := h.lines()[0]; !strings.Contains(top, "! could not start the background process: boom") {
		t.Errorf("failed start: top bar %q", top)
	}
}

func TestStartKeyStartsOnceAndHandsOver(t *testing.T) {
	f := fixture()
	g := fixture()
	st := &starter{b: g}
	h := newTester(t, f, st.opt)
	h.size(100, 20)
	h.send(backendDownMsg{err: ipc.ErrNotRunning})
	h.hold = true
	h.key("r")
	h.key("r")
	if len(h.held) != 1 {
		t.Fatalf("%d start commands while one is out, want 1", len(h.held))
	}
	h.hold = false
	h.run(h.held[0])
	if st.calls != 1 {
		t.Errorf("Start called %d times", st.calls)
	}
	select {
	case b := <-h.m.adopt:
		if b != Backend(g) {
			t.Errorf("handed over %v", b)
		}
	default:
		t.Error("the started connection was not handed to the subscription goroutine")
	}
}

func TestLaunchDialRunsInsideTheProgram(t *testing.T) {
	f := fixture()
	st := &starter{b: f, release: make(chan struct{})}
	h := heldTester(t, nil, withSplash, st.opt)
	h.size(100, 30)
	if len(h.held) != 1 {
		t.Fatalf("Init should start only the launch dial, held %d", len(h.held))
	}
	mustContain(t, h.view(), "chat for the terminal", "○ starting…")
	h.send(splashTick(t, h).msg)
	if !onSplash(h) {
		t.Fatal("the timer must not dismiss the opening screen while the launch dial is out")
	}
	out := make(chan tea.Msg)
	go func() { out <- h.held[0]() }()
	mustContain(t, h.view(), "○ starting…")
	close(st.release)
	msg := <-out
	h.hold, h.held = false, nil
	h.deliver(msg)
	if onSplash(h) || !strings.Contains(h.view(), "# platform") {
		t.Errorf("after the launch dial the main view should show:\n%s", h.view())
	}
}

func TestKeyDuringLaunchShowsConnecting(t *testing.T) {
	st := &starter{b: fixture()}
	h := heldTester(t, nil, withSplash, st.opt)
	h.size(100, 30)
	h.key("x")
	if onSplash(h) {
		t.Fatal("a key should dismiss the opening screen")
	}
	if top := h.lines()[0]; !strings.Contains(top, "○ connecting") {
		t.Errorf("top bar during the launch dial = %q", top)
	}
}

func TestFailedLaunchShowsDown(t *testing.T) {
	st := &starter{err: ipc.ErrNotRunning}
	h := newTester(t, nil, withSplash, st.opt)
	h.size(100, 30)
	h.send(splashTick(t, h).msg)
	if onSplash(h) {
		t.Fatal("after a failed launch dial the timer should dismiss the opening screen")
	}
	if top := h.lines()[0]; !strings.Contains(top, "! background process not running") {
		t.Errorf("top bar = %q", top)
	}
	mustContain(t, footerOf(h), "r start")
}

func TestEndAndHomeAreExplicit(t *testing.T) {
	f := fixture()
	f.outbox = nil
	f.setStored("r-platform", numbered("r-platform", 1, 120)...)
	h := started(t, f, 90, 20)
	h.key("tab")
	h.key("pgup")
	h.run(h.m.scroll(math.MaxInt))
	mustContain(t, h.view(), "line 120.")
	if !h.m.thread.atBottom || f.callCount("messages r-platform 71") != 0 {
		t.Errorf("scrolling by MaxInt: atBottom=%v calls=%v", h.m.thread.atBottom, f.calls)
	}
	h.key("pgup")
	h.key("end")
	if !h.m.thread.atBottom || f.callCount("messages r-platform 71") != 0 {
		t.Errorf("end: atBottom=%v calls=%v", h.m.thread.atBottom, f.calls)
	}
	h.run(h.m.scroll(math.MinInt))
	if f.callCount("messages r-platform 71 50") != 1 {
		t.Errorf("scrolling by MinInt should reach the top once: %v", f.calls)
	}
}

func TestVersionMismatchIsNamed(t *testing.T) {
	f := fixture()
	h := started(t, f, 120, 20)
	h.send(backendDownMsg{err: ipc.ErrVersionMismatch})
	top := h.lines()[0]
	if !strings.Contains(top, "team.example ! background process is a different version — run: termchat daemon stop") {
		t.Errorf("wide top bar = %q", top)
	}
	h.size(80, 20)
	if top := h.lines()[0]; !strings.Contains(top, "! background process is a diff") || !strings.Contains(top, "…") {
		t.Errorf("standard top bar should cut the message with …: %q", top)
	}
	h.size(30, 20)
	if top := h.lines()[0]; !strings.HasSuffix(top, " ! ┐") {
		t.Errorf("tiny top bar = %q", top)
	}
}

func TestServerNameKeptWhileDown(t *testing.T) {
	h := started(t, fixture(), 100, 20)
	h.send(backendDownMsg{err: ipc.ErrClosed})
	if top := h.lines()[0]; !strings.Contains(top, "team.example ! background process not running") {
		t.Errorf("top bar = %q", top)
	}
}
