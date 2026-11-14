package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/ipc"
)

var wantBanner = []string{
	"████████╗███████╗██████╗ ███╗   ███╗ ██████╗██╗  ██╗ █████╗ ████████╗",
	"╚══██╔══╝██╔════╝██╔══██╗████╗ ████║██╔════╝██║  ██║██╔══██╗╚══██╔══╝",
	"   ██║   █████╗  ██████╔╝██╔████╔██║██║     ███████║███████║   ██║",
	"   ██║   ██╔══╝  ██╔══██╗██║╚██╔╝██║██║     ██╔══██║██╔══██║   ██║",
	"   ██║   ███████╗██║  ██║██║ ╚═╝ ██║╚██████╗██║  ██║██║  ██║   ██║",
	"   ╚═╝   ╚══════╝╚═╝  ╚═╝╚═╝     ╚═╝ ╚═════╝╚═╝  ╚═╝╚═╝  ╚═╝   ╚═╝",
}

func withSplash(o *Options) { o.Splash = true }

func splashTick(t *testing.T, h *tester) tick {
	t.Helper()
	for _, tk := range h.ticks {
		if _, ok := tk.msg.(splashDoneMsg); ok {
			if tk.d != 700*time.Millisecond {
				t.Errorf("opening screen lasts %v, want 700ms", tk.d)
			}
			return tk
		}
	}
	t.Fatal("no opening-screen timer scheduled")
	return tick{}
}

func onSplash(h *tester) bool { return strings.Contains(h.view(), "chat for the terminal") }

func TestSplashLayout(t *testing.T) {
	h := newTester(t, fixture(), withSplash)
	h.size(100, 30)
	lines := h.lines()
	i, _ := lineWith(lines, "████████╗███████╗")
	if i < 0 {
		t.Fatalf("no banner:\n%s", h.view())
	}
	pad := strings.Repeat(" ", (100-69)/2)
	for j, b := range wantBanner {
		if strings.TrimRight(lines[i+j], " ") != pad+b {
			t.Errorf("banner row %d = %q, want %q", j, lines[i+j], pad+b)
		}
	}
	if i < 5 {
		t.Errorf("banner starts at row %d, want it centred vertically", i)
	}
	mustContain(t, h.view(), "chat for the terminal", "v0.1.0", "● connected to team.example")
}

func TestSplashDismissedByKeyOrTimeout(t *testing.T) {
	h := newTester(t, fixture(), withSplash)
	h.size(100, 30)
	if !onSplash(h) {
		t.Fatalf("splash not shown:\n%s", h.view())
	}
	h.key("x")
	if onSplash(h) || !strings.Contains(h.view(), "# platform") {
		t.Errorf("a key should dismiss the opening screen:\n%s", h.view())
	}

	h = newTester(t, fixture(), withSplash)
	h.size(100, 30)
	tk := splashTick(t, h)
	h.send(tk.msg)
	if onSplash(h) {
		t.Errorf("the 700ms timer should dismiss the opening screen")
	}

	h = newTester(t, fixture(), withSplash)
	h.size(100, 30)
	h.click(10, 10)
	if onSplash(h) {
		t.Errorf("a click should dismiss the opening screen")
	}
	if h.m.openID != "r-platform" {
		t.Errorf("the dismissing click must not act on the main view: open = %q", h.m.openID)
	}
}

func TestSplashSkippedWhenDisabled(t *testing.T) {
	h := newTester(t, fixture())
	h.size(100, 30)
	if onSplash(h) {
		t.Error("splash shown with Splash off")
	}
	for _, tk := range h.ticks {
		if _, ok := tk.msg.(splashDoneMsg); ok {
			t.Error("splash timer scheduled with Splash off")
		}
	}
}

func TestSplashFirstRunStaysWithJoinHint(t *testing.T) {
	f := fixture()
	f.status = ipc.Status{}
	f.chatsErr = ipc.ErrNoAccount
	h := newTester(t, f, withSplash)
	h.size(100, 30)
	mustContain(t, h.view(), "termchat join <host:port> --invite <code>")
	h.key("x")
	h.key("enter")
	h.click(3, 3)
	h.send(splashTick(t, h).msg)
	if !onSplash(h) || h.quit {
		t.Fatalf("without an account the opening screen must stay:\n%s", h.view())
	}
	// The user joins from another terminal; the next poll proceeds.
	f.mu.Lock()
	f.status = fixture().status
	f.chatsErr = nil
	f.mu.Unlock()
	var polled bool
	for _, tk := range h.ticks {
		if _, ok := tk.msg.(statusPollMsg); ok && tk.d <= 2*time.Second {
			polled = true
		}
	}
	if !polled {
		t.Fatal("no status poll scheduled within 2s")
	}
	h.fireTicks()
	if onSplash(h) || !strings.Contains(h.view(), "# platform") {
		t.Errorf("after the account appears the main view should show:\n%s", h.view())
	}
}

func TestSplashFirstRunQuits(t *testing.T) {
	f := fixture()
	f.status = ipc.Status{}
	h := newTester(t, f, withSplash)
	h.size(100, 30)
	h.key("q")
	if !h.quit {
		t.Error("q should quit the first-run screen")
	}
}

func TestSplashNarrowUsesPlainText(t *testing.T) {
	h := newTester(t, fixture(), withSplash)
	h.size(71, 30)
	mustContain(t, h.view(), "T E R M C H A T", "chat for the terminal")
	mustNotContain(t, h.view(), "█", "╗")
	h.size(72, 30)
	mustContain(t, h.view(), wantBanner[0])
	h.size(60, 20)
	_, l := lineWith(h.lines(), "T E R M C H A T")
	if strings.Index(l, "T") != (60-15)/2 {
		t.Errorf("plain title not centred: %q", l)
	}
}

func TestSplashConnectionLine(t *testing.T) {
	f := fixture()
	f.status.Conn = ipc.StateConnecting
	h := newTester(t, f, withSplash)
	h.size(100, 30)
	mustContain(t, h.view(), "○ connecting to team.example…")
	h.send(backendDownMsg{err: ipc.ErrClosed})
	mustContain(t, h.view(), "! background process not running")
}

func TestFirstRunPollsOnce(t *testing.T) {
	f := fixture()
	f.status = ipc.Status{}
	h := newTester(t, f, withSplash)
	h.size(100, 30)
	st := ipc.Status{}
	h.send(eventMsg{ipc.Event{Kind: ipc.KindStatus, Status: &st}})
	h.send(eventMsg{ipc.Event{Kind: ipc.KindStatus, Status: &st}})
	polls := 0
	for _, tk := range h.ticks {
		if _, ok := tk.msg.(statusPollMsg); ok {
			polls++
		}
	}
	if polls != 1 {
		t.Errorf("%d status polls pending, want 1", polls)
	}
	h.fireTicks()
	if f.callCount("status") != 2 || len(h.ticks) != 1 {
		t.Errorf("after one poll: %d status reads, %d ticks pending; want 2 and 1", f.callCount("status"), len(h.ticks))
	}
}
