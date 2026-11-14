package termchat

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jpe0824/termchat/internal/connector"
)

// relay forwards TCP between the client and the server. While blocked it
// reads and discards everything and keeps both sockets open, like a network
// that silently drops packets. A connection that has had data discarded never
// forwards again, because the SSH stream would be corrupt; new connections
// are refused while blocked.
type relay struct {
	ln      net.Listener
	blocked atomic.Bool
	wg      sync.WaitGroup
	mu      sync.Mutex
	conns   []net.Conn
}

func newRelay(t *testing.T, target string) *relay {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &relay{ln: ln}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if r.blocked.Load() {
				c.Close()
				continue
			}
			s, err := net.Dial("tcp", target)
			if err != nil {
				c.Close()
				continue
			}
			r.mu.Lock()
			r.conns = append(r.conns, c, s)
			r.mu.Unlock()
			var poisoned atomic.Bool
			r.wg.Add(2)
			go r.pipe(s, c, &poisoned)
			go r.pipe(c, s, &poisoned)
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		r.mu.Lock()
		for _, c := range r.conns {
			c.Close()
		}
		r.mu.Unlock()
		r.wg.Wait()
	})
	return r
}

func (r *relay) pipe(dst, src net.Conn, poisoned *atomic.Bool) {
	defer r.wg.Done()
	defer dst.Close()
	defer src.Close()
	buf := make([]byte, 32<<10)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if r.blocked.Load() {
				poisoned.Store(true)
			}
			if !poisoned.Load() {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// waitStateViaUpdates reads Updates until State() is want, failing at the
// deadline.
func waitStateViaUpdates(t *testing.T, c *Client, want connector.ConnState, within time.Duration) {
	t.Helper()
	deadline := time.After(within)
	for c.State() != want {
		select {
		case <-c.Updates():
		case <-deadline:
			t.Fatalf("state = %s, want %s", c.State(), want)
		}
	}
}

func TestKeepaliveDetectsSilentConnectionAndRecovers(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	c, _ := e.joined(t, "ann")
	r := newRelay(t, e.addr)
	c.keepaliveEvery, c.keepaliveTimeout = 50*time.Millisecond, 300*time.Millisecond
	c.after = func(time.Duration) <-chan time.Time { return time.After(20 * time.Millisecond) }
	c.dial = func(ctx context.Context, _ string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
		return defaultDial(ctx, r.ln.Addr().String(), cfg)
	}
	start(t, c)
	waitStateViaUpdates(t, c, connector.StateConnected, 10*time.Second)

	// A healthy idle connection survives many keepalive intervals.
	idle := time.After(500 * time.Millisecond)
	for done := false; !done; {
		select {
		case <-c.Updates():
			t.Fatalf("state changed on a healthy idle connection: %s", c.State())
		case <-idle:
			done = true
		}
	}
	if c.State() != connector.StateConnected {
		t.Fatalf("state = %s after idling, want connected", c.State())
	}

	r.blocked.Store(true)
	waitStateViaUpdates(t, c, connector.StateOffline, 15*time.Second)
	if _, err := c.People(context.Background()); err == nil {
		t.Fatal("request succeeded while offline")
	}

	r.blocked.Store(false)
	waitStateViaUpdates(t, c, connector.StateConnected, 15*time.Second)
	if _, err := c.People(context.Background()); err != nil {
		t.Fatalf("People after recovery: %v", err)
	}
}
