package daemon

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
)

// listGate counts Conversations calls and can hold them.
type listGate struct {
	connector.Connector
	calls   atomic.Int32
	entered chan struct{}
	hold    atomic.Pointer[chan struct{}]
}

func (g *listGate) Conversations(ctx context.Context) ([]model.Conversation, []model.Conversation, error) {
	g.calls.Add(1)
	if p := g.hold.Load(); p != nil {
		g.entered <- struct{}{}
		select {
		case <-*p:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	return g.Connector.Conversations(ctx)
}

func TestResolveMissRefreshesOpenRoomsOnce(t *testing.T) {
	e := newEnv(t)
	g := &listGate{Connector: e.fake, entered: make(chan struct{}, 8)}
	e.conn = g
	c := e.start()
	waitUntil(t, "connected", func() bool {
		s, err := c.Status()
		return err == nil && s.Conn == connector.StateConnected && !s.Syncing
	})
	base := g.calls.Load()

	// An open room appears after the daemon connected.
	open := room("r9", "later", "dana")
	open.Open = true
	e.fake.AddConversation(open)

	release := make(chan struct{})
	g.hold.Store(&release)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := c.Resolve("#later")
			if err == nil && got.ID != "r9" {
				err = errors.New("resolved to " + got.ID)
			}
			errs <- err
		}()
	}
	select {
	case <-g.entered:
	case <-time.After(3 * time.Second):
		close(release)
		wg.Wait()
		t.Fatal("a Resolve miss did not refresh the open rooms")
	}
	time.Sleep(100 * time.Millisecond) // let the second miss arrive while the first refresh runs
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Resolve after the room appeared: %v", err)
		}
	}
	if n := g.calls.Load() - base; n != 1 {
		t.Fatalf("%d Conversations calls for two concurrent misses, want 1", n)
	}
	if _, err := c.Resolve("#later"); err != nil {
		t.Fatal(err)
	}
	if n := g.calls.Load() - base; n != 1 {
		t.Fatalf("a resolved name refreshed again: %d calls", n)
	}
	// A genuine miss refreshes once and stays not found.
	g.hold.Store(nil)
	if _, err := c.Resolve("#nowhere"); !errors.Is(err, ipc.ErrNotFound) {
		t.Fatalf("Resolve(#nowhere) = %v, want ErrNotFound", err)
	}
	if n := g.calls.Load() - base; n != 2 {
		t.Fatalf("%d calls after a genuine miss, want 2", n)
	}
}

func TestChatsRefreshesOpenRooms(t *testing.T) {
	e := newEnv(t)
	c := e.start()
	waitUntil(t, "connected", func() bool {
		s, err := c.Status()
		return err == nil && s.Conn == connector.StateConnected && !s.Syncing
	})
	open := room("r9", "later", "dana")
	open.Open = true
	e.fake.AddConversation(open)
	e.clock.Advance(chatsRefreshAfter + time.Second)
	if _, got, err := c.Chats(); err != nil || len(got) != 1 || got[0].ID != "r9" {
		t.Fatalf("Chats open rooms = %+v, %v", got, err)
	}
	// Offline the cached list answers and the request does not fail.
	e.fake.SetState(connector.StateOffline)
	waitUntil(t, "offline", func() bool { s, _ := c.Status(); return s.Conn == connector.StateOffline })
	if _, got, err := c.Chats(); err != nil || len(got) != 1 {
		t.Fatalf("offline Chats open rooms = %+v, %v", got, err)
	}
}

func TestChatsRefreshHonoursStalenessWindow(t *testing.T) {
	e := newEnv(t)
	g := &listGate{Connector: e.fake, entered: make(chan struct{}, 8)}
	e.conn = g
	c := e.start()
	waitUntil(t, "connected", func() bool {
		s, err := c.Status()
		return err == nil && s.Conn == connector.StateConnected && !s.Syncing
	})
	base := g.calls.Load()
	for range 5 {
		if _, _, err := c.Chats(); err != nil {
			t.Fatal(err)
		}
	}
	if n := g.calls.Load() - base; n != 0 {
		t.Fatalf("%d refreshes right after connecting, want 0 (the catch-up just listed them)", n)
	}
	e.clock.Advance(chatsRefreshAfter + time.Second)
	for range 5 {
		if _, _, err := c.Chats(); err != nil {
			t.Fatal(err)
		}
	}
	if n := g.calls.Load() - base; n != 1 {
		t.Fatalf("%d refreshes for five Chats after the window, want 1", n)
	}
	e.clock.Advance(chatsRefreshAfter + time.Second)
	if _, _, err := c.Chats(); err != nil {
		t.Fatal(err)
	}
	if n := g.calls.Load() - base; n != 2 {
		t.Fatalf("%d refreshes after a second window, want 2", n)
	}
}

func TestChatsAnswersFromCacheWhenRefreshIsSlow(t *testing.T) {
	e := newEnv(t)
	g := &listGate{Connector: e.fake, entered: make(chan struct{}, 8)}
	e.conn = g
	c := e.start()
	waitUntil(t, "connected", func() bool {
		s, err := c.Status()
		return err == nil && s.Conn == connector.StateConnected && !s.Syncing
	})
	release := make(chan struct{})
	defer close(release)
	g.hold.Store(&release)
	e.clock.Advance(chatsRefreshAfter + time.Second)
	start := time.Now()
	if _, _, err := c.Chats(); err != nil {
		t.Fatalf("Chats with a stuck server: %v", err)
	}
	if d := time.Since(start); d > 2*chatsRefreshTimeout {
		t.Fatalf("Chats took %s with a stuck server", d)
	}
}
