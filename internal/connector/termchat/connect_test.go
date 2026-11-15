package termchat

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
	"github.com/jpe0824/termchat/internal/server"
)

// silentAfterHandshake dials through r and then black-holes the relay, so the
// SSH handshake completes and the channel open is never confirmed.
func silentAfterHandshake(r *relay, onDial func(n int)) dialFunc {
	var mu sync.Mutex
	n := 0
	return func(ctx context.Context, _ string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
		mu.Lock()
		n++
		call := n
		mu.Unlock()
		if onDial != nil {
			onDial(call)
		}
		cl, err := defaultDial(ctx, r.ln.Addr().String(), cfg)
		if err == nil && call == 1 {
			r.blocked.Store(true)
		}
		return cl, err
	}
}

func TestRunCancelReturnsWhileChannelOpenHangs(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	c, _ := e.joined(t, "ann")
	r := newRelay(t, e.addr)
	dialled := make(chan struct{}, 1)
	c.dial = silentAfterHandshake(r, func(int) { dialled <- struct{}{} })
	stop := start(t, c) // default 10s connect timeout: only cancel can end this
	<-dialled
	time.Sleep(200 * time.Millisecond) // let Run reach the channel open
	began := time.Now()
	stop()
	if d := time.Since(began); d > 2*time.Second {
		t.Fatalf("Run took %v to return after cancel", d)
	}
}

func TestConnectTimeoutGoesOfflineAndRedials(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	c, _ := e.joined(t, "ann")
	r := newRelay(t, e.addr)
	c.timeout = 300 * time.Millisecond
	var mu sync.Mutex
	var atBackoff []connector.ConnState
	c.after = func(time.Duration) <-chan time.Time {
		mu.Lock()
		atBackoff = append(atBackoff, c.State())
		mu.Unlock()
		return time.After(20 * time.Millisecond)
	}
	c.dial = silentAfterHandshake(r, func(call int) {
		if call == 2 {
			r.blocked.Store(false) // the network is back for the redial
		}
	})
	drain(t, c)
	start(t, c)
	// The server would cut a silent connection after its own 10s handshake
	// window; the client must give up on its connect timeout well before.
	deadline := time.Now().Add(3 * time.Second)
	for c.State() != connector.StateConnected {
		if time.Now().After(deadline) {
			t.Fatalf("state = %s after 3s, want connected on the redial", c.State())
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(atBackoff) == 0 || atBackoff[0] != connector.StateOffline {
		t.Fatalf("states while backing off = %v, want offline first", atBackoff)
	}
}

func TestJoinTimesOutWhenChannelOpenHangs(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	code, _ := e.h.Store.CreateDeviceInvite("ann", server.InviteTTL)
	r := newRelay(t, e.addr)
	errc := make(chan error, 1)
	go func() {
		_, err := join(context.Background(), e.addr, code, "", t.TempDir(), silentAfterHandshake(r, nil), 300*time.Millisecond)
		errc <- err
	}()
	select {
	case err := <-errc:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want a timeout wrapping DeadlineExceeded", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Join did not return within the connect timeout")
	}
}

func TestSendWithLostResponseIsUnknownOutcomeAndRetrySafe(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	c, _ := e.joined(t, "ann")
	r := newRelay(t, e.addr)
	c.timeout = 300 * time.Millisecond
	c.dial = func(ctx context.Context, _ string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
		return defaultDial(ctx, r.ln.Addr().String(), cfg)
	}
	c.after = func(time.Duration) <-chan time.Time { return time.After(20 * time.Millisecond) }
	drain(t, c)
	start(t, c)
	waitState(t, c, connector.StateConnected)
	ctx := context.Background()
	room, err := c.Room(ctx, connector.RoomOp{Kind: connector.RoomCreate, Name: "solo"})
	if err != nil {
		t.Fatal(err)
	}

	r.blockedDown.Store(true)
	_, err = c.Send(ctx, room.ID, "m1", text("hello"), nil)
	if !errors.Is(err, connector.ErrUnknownOutcome) {
		t.Fatalf("err = %v, want ErrUnknownOutcome", err)
	}
	if errors.Is(err, connector.ErrOffline) {
		t.Fatalf("a timeout is not a dropped connection: %v", err)
	}

	stored := posted(t, e, room.ID)
	if len(stored) != 1 || stored[0].MsgID != "m1" {
		t.Fatalf("server stored %+v, want exactly m1", stored)
	}

	// The connection is now useless; drop it and retry on the next one.
	r.blockedDown.Store(false)
	c.current().close()
	deadline := time.Now().Add(15 * time.Second)
	var ev model.Event
	for {
		ev, err = c.Send(ctx, room.ID, "m1", text("hello"), nil)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || ev.Seq != stored[0].Seq {
		t.Fatalf("retry = %+v, %v; want the original event (seq %d)", ev, err, stored[0].Seq)
	}
	if n := len(posted(t, e, room.ID)); n != 1 {
		t.Fatalf("server holds %d messages after the retry, want 1", n)
	}
}

func posted(t *testing.T, e *env, conv string) []model.Event {
	t.Helper()
	evs, err := e.h.Store.History(conv, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var out []model.Event
	for _, ev := range evs {
		if ev.Kind == model.MessagePosted {
			out = append(out, ev)
		}
	}
	return out
}

func TestChangeRequestUnknownOutcomeOnCancelAndDrop(t *testing.T) {
	e := newEnv(t)
	e.person(t, "ann", false)
	// Two devices: a session handles one request at a time, so the blocked
	// first request would hold up the second on the same connection.
	c, c2 := e.client(t, "ann"), e.client(t, "ann")
	drain(t, c)
	drain(t, c2)
	arrived, release := blockOn(e, proto.TypeDeviceInvite)
	defer release()

	// Cancelled after the frame was written.
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := c.DeviceInvite(ctx); errc <- err }()
	<-arrived
	cancel()
	err := <-errc
	if !errors.Is(err, connector.ErrUnknownOutcome) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: err = %v, want ErrUnknownOutcome wrapping Canceled", err)
	}

	// Connection dropped mid-flight.
	go func() { _, err := c2.DeviceInvite(context.Background()); errc <- err }()
	<-arrived
	stopped := make(chan struct{})
	go func() { e.h.StopServer(); close(stopped) }()
	select {
	case err = <-errc:
	case <-time.After(wait):
		t.Fatal("request did not return when the server stopped")
	}
	if !errors.Is(err, connector.ErrUnknownOutcome) || !errors.Is(err, connector.ErrOffline) {
		t.Fatalf("drop: err = %v, want ErrUnknownOutcome and ErrOffline", err)
	}
	release()
	<-stopped
}
