package ipc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/store"
)

func TestShutdownFinishesStartedRequestsAndRefusesNew(t *testing.T) {
	b := &stubBackend{started: make(chan struct{}, 1), gate: make(chan struct{})}
	srv, sock := startServer(t, b)
	inFlight := dialTest(t, sock)
	late := dialTest(t, sock)

	type result struct {
		id  string
		err error
	}
	got := make(chan result, 1)
	go func() {
		id, err := inFlight.Send("c1", "hi", "tui")
		got <- result{id, err}
	}()
	<-b.started

	closed := make(chan struct{})
	go func() {
		srv.Close()
		close(closed)
	}()
	waitUntil(t, srv.shuttingDown)

	if _, err := late.Send("c1", "late", "tui"); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("Send after shutdown began = %v, want ErrShuttingDown", err)
	}
	if n := b.sends.Load(); n != 1 {
		t.Fatalf("backend Send ran %d times, want 1: the refused request must have no effect", n)
	}

	close(b.gate)
	select {
	case r := <-got:
		if r.err != nil || r.id != "c1/tui" {
			t.Fatalf("in-flight Send = %q, %v; want its real reply", r.id, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight Send got no reply")
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return")
	}
}

func TestShutdownWaitIsBounded(t *testing.T) {
	saved := shutdownWait
	shutdownWait = 200 * time.Millisecond
	t.Cleanup(func() { shutdownWait = saved })

	b := &stubBackend{started: make(chan struct{}, 1), gate: make(chan struct{})}
	t.Cleanup(func() { close(b.gate) })
	srv, sock := startServer(t, b)
	c := dialTest(t, sock)
	go func() { _, _ = c.Send("c1", "stuck", "tui") }()
	<-b.started

	closed := make(chan struct{})
	go func() {
		srv.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("a stuck request held up Close")
	}
}

func TestSubscriberOverflowEndsSubscriptionNotRequests(t *testing.T) {
	srv, sock := startServer(t, &stubBackend{})
	c := dialTest(t, sock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := c.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return len(srv.Subscribers()) == 1 })

	// Nobody reads events while a burst larger than the client's queue arrives.
	for i := range eventsBuffer + 100 {
		srv.Broadcast(Event{Kind: KindMessage, Message: &store.Message{ConvID: "c", Seq: int64(i + 1)}})
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.Messages("c", 0, 1)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Messages = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a request on the subscribed client hung behind undelivered events")
	}

	n := 0
	timeout := time.After(2 * time.Second)
	for open := true; open; {
		select {
		case _, open = <-events:
			if open {
				n++
			}
		case <-timeout:
			t.Fatal("overflowed subscription channel never closed")
		}
	}
	if n > eventsBuffer {
		t.Fatalf("received %d events, more than the queue of %d", n, eventsBuffer)
	}
	if err := c.SubscriptionErr(); !errors.Is(err, ErrSubscriptionOverflow) {
		t.Fatalf("SubscriptionErr = %v, want ErrSubscriptionOverflow", err)
	}

	// The consumer subscribes again and gets new events.
	events, err = c.Subscribe(ctx)
	if err != nil {
		t.Fatalf("Subscribe again: %v", err)
	}
	if err := c.SubscriptionErr(); err != nil {
		t.Fatalf("SubscriptionErr on a live subscription = %v", err)
	}
	waitUntil(t, func() bool { return len(srv.Subscribers()) == 1 })
	srv.Broadcast(Event{Kind: KindStatus, Status: &Status{Pending: 7}})
	select {
	case ev := <-events:
		if ev.Kind != KindStatus || ev.Status.Pending != 7 {
			t.Fatalf("event after resubscribing = %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event after resubscribing")
	}
}

func TestReaderExitsOnCloseWithFullSubscription(t *testing.T) {
	srv, sock := startServer(t, &stubBackend{})
	c := dialTest(t, sock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := c.Subscribe(ctx); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return len(srv.Subscribers()) == 1 })
	for i := range eventsBuffer + 10 {
		srv.Broadcast(Event{Kind: KindMessage, Message: &store.Message{ConvID: "c", Seq: int64(i + 1)}})
	}
	srv.Close()
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		t.Fatal("client reader still running after the connection closed")
	}
	if _, err := c.Status(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Status after close = %v, want ErrClosed", err)
	}
}
