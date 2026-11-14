package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/jpe0824/termchat/internal/ipc"
)

// supHarness runs the subscription goroutine against fakes, with a clock
// that only moves when the test says so.
type supHarness struct {
	t     *testing.T
	out   chan tea.Msg
	waits chan chan time.Time
	dials chan dialResult
	stop  context.CancelFunc
	done  chan struct{}
}

type dialResult struct {
	b   Backend
	err error
}

func startSupervisor(t *testing.T, b Backend) *supHarness {
	t.Helper()
	s := &supHarness{t: t, out: make(chan tea.Msg, 64), waits: make(chan chan time.Time, 8),
		dials: make(chan dialResult, 8), done: make(chan struct{})}
	opts := Options{
		Send: func(m tea.Msg) { s.out <- m },
		After: func(d time.Duration) <-chan time.Time {
			if d != 2*time.Second {
				t.Errorf("reconnect wait = %v, want 2s", d)
			}
			c := make(chan time.Time, 1)
			s.waits <- c
			return c
		},
		Dial: func(ctx context.Context) (Backend, error) {
			select {
			case r := <-s.dials:
				return r.b, r.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
	opts.fill()
	ctx, cancel := context.WithCancel(context.Background())
	s.stop = cancel
	sup := &supervisor{b: b, opts: opts}
	go func() { defer close(s.done); sup.run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			t.Error("subscription goroutine did not stop")
		}
	})
	return s
}

func (s *supHarness) next() tea.Msg {
	s.t.Helper()
	select {
	case m := <-s.out:
		return m
	case <-time.After(5 * time.Second):
		s.t.Fatal("no message from the subscription goroutine")
		return nil
	}
}

func (s *supHarness) wait() chan time.Time {
	s.t.Helper()
	select {
	case c := <-s.waits:
		return c
	case <-time.After(5 * time.Second):
		s.t.Fatal("the goroutine did not wait before reconnecting")
		return nil
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSubscriptionForwardsEventsAsUIWithoutNotify(t *testing.T) {
	f := fixture()
	s := startSupervisor(t, f)
	if m, ok := s.next().(subscribedMsg); !ok || m.b != Backend(f) {
		t.Fatalf("first message = %#v, want subscribedMsg", m)
	}
	if f.callCount("subscribe false") != 1 {
		t.Errorf("calls = %v, want one SubscribeUI(ctx, false)", f.calls)
	}
	f.sub() <- ipc.Event{Kind: ipc.KindConv}
	if m, ok := s.next().(eventMsg); !ok || m.ev.Kind != ipc.KindConv {
		t.Errorf("event forwarded as %#v", m)
	}
}

func TestSubscriptionOverflowResubscribes(t *testing.T) {
	f := fixture()
	s := startSupervisor(t, f)
	s.next()
	f.endSub(ipc.ErrSubscriptionOverflow)
	if _, ok := s.next().(subscribedMsg); !ok {
		t.Fatal("overflow should resubscribe and report it so the views are re-read")
	}
	if f.callCount("subscribe") != 2 || f.closed {
		t.Errorf("calls = %v closed=%v, want a second subscribe on the same connection", f.calls, f.closed)
	}
}

func TestBackgroundProcessGoneReconnects(t *testing.T) {
	f := fixture()
	s := startSupervisor(t, f)
	s.next()
	f.endSub(ipc.ErrClosed)
	if m, ok := s.next().(backendDownMsg); !ok || !errors.Is(m.err, ipc.ErrClosed) {
		t.Fatalf("want backendDownMsg(ErrClosed), got %#v", m)
	}
	waitFor(t, func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.closed })
	s.wait() <- time.Now()
	s.dials <- dialResult{err: errors.New("connection refused")}
	if _, ok := s.next().(backendDownMsg); !ok {
		t.Fatal("a failed dial should report the process down again")
	}
	s.wait() <- time.Now()
	g := fixture()
	s.dials <- dialResult{b: g}
	if m, ok := s.next().(subscribedMsg); !ok || m.b != Backend(g) {
		t.Fatalf("want subscribedMsg with the new connection, got %#v", m)
	}
}

func TestNoBackendAtStartDials(t *testing.T) {
	s := startSupervisor(t, nil)
	s.dials <- dialResult{err: ipc.ErrShuttingDown}
	if _, ok := s.next().(backendDownMsg); !ok {
		t.Fatal("want backendDownMsg")
	}
	s.wait() <- time.Now()
	g := fixture()
	s.dials <- dialResult{b: g}
	if _, ok := s.next().(subscribedMsg); !ok {
		t.Fatal("want subscribedMsg")
	}
}

func TestStopClosesBackend(t *testing.T) {
	f := fixture()
	s := startSupervisor(t, f)
	s.next()
	s.stop()
	<-s.done
	if !f.closed {
		t.Error("the backend should be closed when the interface stops")
	}
}

func TestSubscribedMsgRereadsEverything(t *testing.T) {
	f := fixture()
	h := started(t, f, 90, 20)
	st, ch, th := f.callCount("status"), f.callCount("chats"), f.callCount("messages r-platform")
	h.send(subscribedMsg{b: f})
	if f.callCount("status") != st+1 || f.callCount("chats") != ch+1 || f.callCount("messages r-platform") != th+1 {
		t.Errorf("calls after subscribe: %v", f.calls)
	}
}

func TestNotifyEventsIgnored(t *testing.T) {
	f := fixture()
	h := started(t, f, 90, 20)
	n := len(f.calls)
	h.send(eventMsg{ipc.Event{Kind: ipc.KindNotify, Notify: &ipc.NotifyPayload{Text: "x"}}})
	if len(f.calls) != n {
		t.Errorf("notify event caused reads: %v", f.calls[n:])
	}
}

func TestStaleThreadResultDropped(t *testing.T) {
	f := fixture()
	h := started(t, f, 90, 20)
	cmd := h.m.requestThread()
	h.click(4, 2) // open dana while the platform read is out
	h.run(cmd)
	if h.m.openID != "dm-dana" {
		t.Fatalf("open = %q", h.m.openID)
	}
	mustNotContain(t, h.view(), "did the meter import")
	mustContain(t, h.view(), "got a minute?")
}
