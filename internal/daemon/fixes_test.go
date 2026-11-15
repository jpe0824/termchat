package daemon

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/model"
)

func TestRejectedEventIsNotRefetched(t *testing.T) {
	e := newEnv(t)
	rec := record(e)
	e.fake.AddConversation(room("r1", "platform", "me", "dana"), messages("a", 3)...)
	c := e.start()
	waitUntil(t, "first sync", func() bool { return len(chatIDs(t, c)) == 1 && len(rec.syncCalls()) == 1 })

	e.goOffline(c)
	e.fake.AddConversation(room("r1", "platform", "me", "dana"),
		posted("dana", "ok-4", "four"), posted("dana", "", "rejected"), posted("dana", "ok-6", "six"))
	e.fake.SetState(connector.StateConnected)
	waitUntil(t, "the catch-up with the rejected event", func() bool { return len(rec.syncCalls()) == 2 })
	waitUntil(t, "online", func() bool { st, _ := c.Status(); return st.Conn == connector.StateConnected })

	// The next reconnect starts after the rejected event, not at it.
	e.goOffline(c)
	e.fake.SetState(connector.StateConnected)
	waitUntil(t, "the next catch-up", func() bool { return len(rec.syncCalls()) == 3 })
	if got := rec.syncCalls()[2]; !maps1(got, "r1", 6) {
		t.Fatalf("Sync cursors after the rejected event = %v, want r1:6", got)
	}
}

// gatedConn lets a test inject connector updates and hold Sync.
type gatedConn struct {
	*recorder
	updates chan connector.Update
	gate    atomic.Pointer[chan struct{}]
	syncing chan struct{}
}

func gate(e *env) *gatedConn {
	g := &gatedConn{recorder: record(e), updates: make(chan connector.Update, 64), syncing: make(chan struct{}, 8)}
	e.conn = g
	done := make(chan struct{})
	e.t.Cleanup(func() { close(done) }) // runs after the daemon is stopped
	go func() {
		for {
			select {
			case u := <-e.fake.Updates():
				g.updates <- u
			case <-done:
				return
			}
		}
	}()
	return g
}

func (g *gatedConn) Updates() <-chan connector.Update { return g.updates }

func (g *gatedConn) Sync(ctx context.Context, cursors map[string]int64) ([]model.Event, bool, []string, error) {
	if p := g.gate.Load(); p != nil {
		g.syncing <- struct{}{}
		select {
		case <-*p:
		case <-ctx.Done():
			return nil, false, nil, ctx.Err()
		}
	}
	return g.recorder.Sync(ctx, cursors)
}

func TestNoSendDuringCatchUp(t *testing.T) {
	e := newEnv(t)
	g := gate(e)
	c := startWithRoom(t, e)
	waitUntil(t, "first catch-up", func() bool { return len(g.syncCalls()) == 1 })

	hold := make(chan struct{})
	g.gate.Store(&hold)
	// A reconnect whose offline moment the daemon never observed: it sees
	// only that the state may have changed and reads "connected".
	g.updates <- connector.Update{}
	select {
	case <-g.syncing:
	case <-time.After(5 * time.Second):
		t.Fatal("no catch-up started")
	}
	if _, err := c.Send("r1", "during catch-up", "tui"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if n := g.sends.Load(); n != 0 {
		t.Fatalf("sender made %d attempts while the catch-up was running", n)
	}
	g.gate.Store(nil)
	close(hold)
	waitUntil(t, "the message after the catch-up", func() bool { return len(e.fake.Sent()) == 1 })
}
