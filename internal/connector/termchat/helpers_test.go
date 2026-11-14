package termchat

import (
	"context"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/server"
	"github.com/jpe0824/termchat/internal/testutil"
)

const wait = 5 * time.Second

var testLimits = server.Limits{FramesPerSec: 5000, Burst: 5000}

// env is a running server plus helpers to join and run clients against it.
type env struct {
	h       *testutil.Harness
	addr    string
	dataDir string // server data directory
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	h := testutil.StartServerWith(t, func(o *server.Options) {
		o.DataDir = dir
		o.Limits = testLimits
	})
	return &env{h: h, addr: h.Server.Addr().String(), dataDir: dir}
}

func (e *env) person(t *testing.T, handle string, admin bool) {
	t.Helper()
	if err := e.h.Store.CreatePerson(model.Person{Handle: handle, DisplayName: handle, Admin: admin}); err != nil {
		t.Fatalf("CreatePerson(%s): %v", handle, err)
	}
}

// joined registers a new device for handle and returns a client that has not
// started.
func (e *env) joined(t *testing.T, handle string) (*Client, string) {
	t.Helper()
	code, err := e.h.Store.CreateInvite(handle, handle, server.InviteTTL)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	dir := t.TempDir()
	acct, err := Join(context.Background(), e.addr, code, dir)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	return New(acct, dir), dir
}

// start runs c until the test ends and fails the test if Run does not return
// nil promptly after cancel. It returns the cancel function.
func start(t *testing.T, c *Client) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("Run returned %v, want nil", err)
				}
			case <-time.After(wait):
				t.Error("Run did not return after cancel")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

// client joins handle on a new device, runs it and waits until connected.
func (e *env) client(t *testing.T, handle string, tweak ...func(*Client)) *Client {
	t.Helper()
	c, _ := e.joined(t, handle)
	for _, f := range tweak {
		f(c)
	}
	start(t, c)
	waitState(t, c, connector.StateConnected)
	return c
}

func waitState(t *testing.T, c *Client, want connector.ConnState) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if c.State() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("state = %s, want %s", c.State(), want)
}

// waitUpdate reads Updates until pred accepts one.
func waitUpdate(t *testing.T, c *Client, pred func(connector.Update) bool) connector.Update {
	t.Helper()
	timeout := time.After(wait)
	for {
		select {
		case u := <-c.Updates():
			if pred(u) {
				return u
			}
		case <-timeout:
			t.Fatal("timed out waiting for a matching update")
		}
	}
}

func isEvent(kind model.EventKind, msgID string) func(connector.Update) bool {
	return func(u connector.Update) bool {
		return u.Event != nil && u.Event.Kind == kind && u.Event.MsgID == msgID
	}
}

func text(s string) model.Body { return model.Body{Format: "text", Data: []byte(s)} }

// restart stops the harness server and starts another on the same address.
func (e *env) restart(t *testing.T, dataDir string) {
	t.Helper()
	if err := e.h.StopServer(); err != nil {
		t.Fatalf("StopServer: %v", err)
	}
	var srv *server.Server
	var err error
	for range 100 {
		srv, err = server.New(server.Options{Addr: e.addr, DataDir: dataDir, Limits: testLimits})
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("restart server on %s: %v", e.addr, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { srv.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func fingerprint(t *testing.T, dir, addr string) string {
	t.Helper()
	s, err := loadKey(dir, addr)
	if err != nil {
		t.Fatalf("loadKey: %v", err)
	}
	return ssh.FingerprintSHA256(s.PublicKey())
}

// immediate is an `after` that fires at once and records every delay asked.
type immediate struct {
	mu     sync.Mutex
	delays []time.Duration
}

func (i *immediate) after(d time.Duration) <-chan time.Time {
	i.mu.Lock()
	i.delays = append(i.delays, d)
	i.mu.Unlock()
	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return ch
}

func (i *immediate) snapshot() []time.Duration {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]time.Duration(nil), i.delays...)
}

// drain discards Updates until the test ends, for tests that do not read them.
func drain(t *testing.T, c *Client) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		for {
			select {
			case <-c.Updates():
			case <-ctx.Done():
				return
			}
		}
	}()
}
