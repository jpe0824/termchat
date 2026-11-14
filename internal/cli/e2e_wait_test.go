package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// waiter is a running "termchat wait".
type waiter struct {
	cmd      *exec.Cmd
	out, err bytes.Buffer
	done     chan struct{}
	code     int
}

func (p *person) startWait(args ...string) *waiter {
	p.t.Helper()
	w := &waiter{done: make(chan struct{})}
	w.cmd = p.command("", append([]string{"wait"}, args...)...)
	w.cmd.Stdout, w.cmd.Stderr = &w.out, &w.err
	if err := w.cmd.Start(); err != nil {
		p.t.Fatal(err)
	}
	go func() {
		err := w.cmd.Wait()
		if ee, ok := err.(*exec.ExitError); ok {
			w.code = ee.ExitCode()
		}
		close(w.done)
	}()
	p.t.Cleanup(func() { _ = w.cmd.Process.Kill() })
	return w
}

func (w *waiter) finished() bool {
	select {
	case <-w.done:
		return true
	default:
		return false
	}
}

// result waits for the command to exit.
func (w *waiter) result(t *testing.T) result {
	t.Helper()
	select {
	case <-w.done:
	case <-time.After(30 * time.Second):
		t.Fatal("wait did not return")
	}
	return result{out: w.out.String(), err: w.err.String(), code: w.code}
}

// pingUntilDone calls send with a fresh text every few hundred milliseconds
// until w exits, so that the test does not depend on when w subscribed.
func pingUntilDone(t *testing.T, w *waiter, send func(text string)) {
	t.Helper()
	for i := 0; !w.finished(); i++ {
		if i > 100 {
			t.Fatal("wait never returned")
		}
		send(fmt.Sprintf("ping %d", i))
		time.Sleep(300 * time.Millisecond)
	}
}

func TestWaitReturnsNextIncoming(t *testing.T) {
	for _, chat := range [][]string{{"@alice"}, {}} {
		t.Run(strings.Join(chat, "")+"all", func(t *testing.T) {
			_, a, b := pair(t)
			a.ok("dm", "bob")
			a.ok("send", "@bob", "before the wait")
			eventually(t, "bob has the dm", func() bool { return b.do("read", "@alice", "--json").code == 0 })
			w := b.startWait(chat...)
			pingUntilDone(t, w, func(text string) { a.ok("send", "@bob", text) })
			r := w.result(t)
			if r.code != 0 || r.err != "" {
				t.Fatalf("exit %d, stderr %q", r.code, r.err)
			}
			var m msgView
			decode(t, r.out, &m)
			if m.Author != "alice" || !strings.HasPrefix(m.Text, "ping ") || m.Seq < 1 || m.MsgID == "" || m.Status != "" {
				t.Fatalf("wait printed %+v", m)
			}
		})
	}
}

func TestWaitIgnoresOwnMessages(t *testing.T) {
	_, a, b := pair(t)
	a.ok("dm", "bob")
	a.ok("send", "@bob", "hello")
	eventually(t, "bob has the dm", func() bool { return b.hasText("@alice", "hello") })
	w := b.startWait("@alice")
	time.Sleep(time.Second)
	for i := range 3 {
		b.ok("send", "@alice", fmt.Sprintf("my own %d", i))
		time.Sleep(200 * time.Millisecond)
	}
	eventually(t, "bob's messages to be stored", func() bool {
		ms := b.messages("read", "@alice")
		return len(ms) == 4 && ms[3].Status == ""
	})
	time.Sleep(time.Second)
	if w.finished() {
		t.Fatalf("wait returned for the user's own message: %q", w.out.String())
	}
	pingUntilDone(t, w, func(text string) { a.ok("send", "@bob", text) })
	var m msgView
	r := w.result(t)
	decode(t, r.out, &m)
	if r.code != 0 || m.Author != "alice" {
		t.Fatalf("exit %d, printed %+v", r.code, m)
	}
}

func TestWaitMentionsOnly(t *testing.T) {
	_, a, b := pair(t)
	a.ok("dm", "bob")
	a.ok("send", "@bob", "hello")
	eventually(t, "bob has the dm", func() bool { return b.hasText("@alice", "hello") })
	w := b.startWait("--mentions")
	pingUntilDone(t, w, func(text string) {
		a.ok("send", "@bob", "plain "+text)
		time.Sleep(100 * time.Millisecond)
		a.ok("send", "@bob", "hey @bob "+text)
	})
	var m msgView
	r := w.result(t)
	decode(t, r.out, &m)
	if r.code != 0 || !strings.HasPrefix(m.Text, "hey @bob") || len(m.Mentions) != 1 || m.Mentions[0] != "bob" {
		t.Fatalf("exit %d, printed %+v, want the message that mentions bob", r.code, m)
	}
}

func TestWaitTimeoutExit5(t *testing.T) {
	_, a, b := pair(t)
	a.ok("dm", "bob")
	a.ok("send", "@bob", "old")
	eventually(t, "bob has the dm", func() bool { return b.hasText("@alice", "old") })
	start := time.Now()
	r := b.do("wait", "@alice", "--timeout", "1s")
	r.fail(t, 5, "timed out waiting for a message")
	if d := time.Since(start); d < time.Second {
		t.Fatalf("returned after %s, before the timeout", d)
	}
	b.do("wait", "--timeout", "500ms").fail(t, 5, "timed out waiting for a message")
	b.do("wait", "--timeout", "-1s").fail(t, 2, "--timeout must not be negative")
}

func TestWaitSinceReturnsMessagesThatAlreadyArrived(t *testing.T) {
	_, a, b := pair(t)
	a.ok("dm", "bob")
	a.ok("send", "@bob", "m1")
	eventually(t, "m1", func() bool { return b.hasText("@alice", "m1") })
	first := b.messages("read", "@alice")[0]

	// Two messages arrive while bob is not waiting.
	a.ok("send", "@bob", "m2")
	a.ok("send", "@bob", "m3")
	eventually(t, "m3", func() bool { return b.hasText("@alice", "m3") })
	seq := fmt.Sprint(first.Seq)

	var m msgView
	decode(t, b.ok("wait", "@alice", "--since", seq, "--timeout", "5s"), &m)
	if m.Text != "m2" {
		t.Fatalf("wait --since %s printed %q, want the earliest later message m2", seq, m.Text)
	}
	decode(t, b.ok("wait", "@alice", "--since", fmt.Sprint(m.Seq), "--timeout", "5s"), &m)
	if m.Text != "m3" {
		t.Fatalf("second wait printed %q, want m3", m.Text)
	}
	// Nothing newer: it waits, and the user's own message does not count.
	b.ok("send", "@alice", "mine")
	eventually(t, "own message stored", func() bool {
		ms := b.messages("read", "@alice")
		return ms[len(ms)-1].Status == "" && ms[len(ms)-1].Author == "bob"
	})
	b.do("wait", "@alice", "--since", fmt.Sprint(m.Seq), "--timeout", "1s").fail(t, 5, "timed out waiting for a message")
	b.do("wait", "--since", seq).fail(t, 2, "--since needs a chat")
	b.do("wait", "@alice", "--since", "x").failContains(t, 2, "invalid argument")
}

func TestWaitInterruptExits130(t *testing.T) {
	_, a, b := pair(t)
	a.ok("dm", "bob")
	a.ok("send", "@bob", "hi")
	eventually(t, "dm", func() bool { return b.hasText("@alice", "hi") })
	w := b.startWait("@alice")
	time.Sleep(time.Second)
	if err := w.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	w.result(t).fail(t, 130, "interrupted")
}
