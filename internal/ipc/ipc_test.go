package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/store"
)

// stubBackend answers every operation with err when set, else a fixed value.
type stubBackend struct {
	err     error
	stopped chan struct{}
	// When set, Send reports on started and then waits for gate.
	started chan struct{}
	gate    chan struct{}
	sends   atomic.Int32 // Sends that ran: the stand-in for queued rows
}

func (b *stubBackend) Status(context.Context) (Status, error) {
	return Status{Account: "acct", Conn: connector.StateConnected, Pending: 2}, b.err
}
func (b *stubBackend) Chats(context.Context) (Chats, error) { return Chats{}, b.err }
func (b *stubBackend) Messages(_ context.Context, conv string, before int64, limit int) ([]store.Message, error) {
	return []store.Message{{ConvID: conv, Seq: before - 1, Text: fmt.Sprint(limit)}}, b.err
}
func (b *stubBackend) Send(_ context.Context, conv, text, source string) (string, error) {
	b.sends.Add(1)
	if b.started != nil {
		b.started <- struct{}{}
		<-b.gate
	}
	return conv + "/" + source, b.err
}
func (b *stubBackend) Edit(context.Context, string, string, string) error { return b.err }
func (b *stubBackend) Delete(context.Context, string, string) error       { return b.err }
func (b *stubBackend) Retry(context.Context, string) error                { return b.err }
func (b *stubBackend) Discard(context.Context, string) error              { return b.err }
func (b *stubBackend) MarkRead(context.Context, string) error             { return b.err }
func (b *stubBackend) Outbox(_ context.Context, conv string) ([]store.OutboxItem, error) {
	return []store.OutboxItem{{MsgID: "m1", ConvID: conv, State: store.StatusFailed, Err: "refused"}}, b.err
}
func (b *stubBackend) Search(context.Context, string, int) ([]store.Message, error) {
	return nil, b.err
}
func (b *stubBackend) Resolve(_ context.Context, ref string) (model.Conversation, error) {
	return model.Conversation{ID: ref}, b.err
}
func (b *stubBackend) OpenDM(_ context.Context, h string) (model.Conversation, error) {
	return model.Conversation{ID: "dm:" + h}, b.err
}
func (b *stubBackend) Room(_ context.Context, op connector.RoomOp) (model.Conversation, error) {
	return model.Conversation{ID: op.Conv, Name: op.Name}, b.err
}
func (b *stubBackend) DeviceInvite(context.Context) (string, error) { return "code", b.err }
func (b *stubBackend) CreateAgent(context.Context, string, string) (string, error) {
	return "code", b.err
}
func (b *stubBackend) AdminInvite(context.Context, string, string) (string, error) {
	return "code", b.err
}
func (b *stubBackend) Join(context.Context, string, string) error { return b.err }
func (b *stubBackend) Stop() {
	if b.stopped != nil {
		close(b.stopped)
	}
}

// shortDir returns a directory whose socket path fits the platform limit;
// the default temp directory on macOS is too deep.
func shortDir(t *testing.T) string {
	t.Helper()
	base := ""
	if runtime.GOOS == "darwin" {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "tcipc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func startServer(t *testing.T, b Backend) (*Server, string) {
	t.Helper()
	sock := filepath.Join(shortDir(t), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(b, slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan struct{})
	go func() {
		_ = srv.Serve(ln)
		close(done)
	}()
	t.Cleanup(func() {
		srv.Close()
		<-done
	})
	return srv, sock
}

func dialTest(t *testing.T, sock string) *Client {
	t.Helper()
	c, err := dialPath(context.Background(), sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestRequestsRoundTrip(t *testing.T) {
	_, sock := startServer(t, &stubBackend{})
	c := dialTest(t, sock)

	st, err := c.Status()
	if err != nil || st.Account != "acct" || st.Conn != connector.StateConnected || st.Pending != 2 {
		t.Fatalf("Status = %+v, %v", st, err)
	}
	id, err := c.Send("c1", "hi", "tui")
	if err != nil || id != "c1/tui" {
		t.Fatalf("Send = %q, %v", id, err)
	}
	msgs, err := c.Messages("c1", 10, 7)
	if err != nil || len(msgs) != 1 || msgs[0].Seq != 9 || msgs[0].Text != "7" {
		t.Fatalf("Messages = %+v, %v", msgs, err)
	}
	items, err := c.Outbox("c1")
	if err != nil || len(items) != 1 || items[0].ConvID != "c1" || items[0].State != store.StatusFailed || items[0].Err != "refused" {
		t.Fatalf("Outbox = %+v, %v", items, err)
	}
	conv, err := c.Room(connector.RoomOp{Kind: connector.RoomRename, Conv: "r1", Name: "new"})
	if err != nil || conv.ID != "r1" || conv.Name != "new" {
		t.Fatalf("Room = %+v, %v", conv, err)
	}
}

func TestEverySentinelSurvivesTheSocket(t *testing.T) {
	sentinels := []error{
		ErrNotFound, ErrAmbiguous, ErrSendDenied, ErrNotConnected, ErrNotAuthorised,
		ErrForbidden, ErrInvalid, ErrTimeout, ErrVersionMismatch, ErrNoAccount, ErrShuttingDown,
	}
	for _, s := range sentinels {
		t.Run(s.Error(), func(t *testing.T) {
			b := &stubBackend{err: fmt.Errorf("%w: detail for %s", s, s)}
			_, sock := startServer(t, b)
			c := dialTest(t, sock)
			err := c.Edit("c", "m", "x")
			if !errors.Is(err, s) {
				t.Fatalf("err = %v, want errors.Is %v", err, s)
			}
			if want := b.err.Error(); err.Error() != want {
				t.Fatalf("message = %q, want %q", err.Error(), want)
			}
			for _, other := range sentinels {
				if other != s && errors.Is(err, other) {
					t.Fatalf("err %v also matches %v", err, other)
				}
			}
		})
	}
}

func TestUnknownErrorKeepsMessage(t *testing.T) {
	_, sock := startServer(t, &stubBackend{err: errors.New("disk on fire")})
	c := dialTest(t, sock)
	err := c.Delete("c", "m")
	if err == nil || err.Error() != "disk on fire" {
		t.Fatalf("err = %v", err)
	}
}

func TestHandshakeVersionMismatch(t *testing.T) {
	_, sock := startServer(t, &stubBackend{})

	saved := clientVersion
	clientVersion = Version + 1
	t.Cleanup(func() { clientVersion = saved })
	_, err := dialPath(context.Background(), sock)
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("dial with a newer client: %v, want ErrVersionMismatch", err)
	}

	// A request before the handshake is refused too.
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintln(conn, `{"id":1,"op":"status"}`)
	var res struct {
		ID    uint64     `json:"id"`
		Error *wireError `json:"error"`
	}
	if err := json.NewDecoder(conn).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res.Error == nil || res.Error.Code != codeVersionMismatch {
		t.Fatalf("status before hello: %+v", res.Error)
	}
}

func TestSubscribeAndStop(t *testing.T) {
	b := &stubBackend{stopped: make(chan struct{})}
	srv, sock := startServer(t, b)
	c := dialTest(t, sock)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := c.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetFocus("c1", true); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool {
		subs := srv.Subscribers()
		return len(subs) == 1 && subs[0].Conv == "c1" && subs[0].Focused
	})
	srv.Broadcast(Event{Kind: KindMessage, Message: &store.Message{ConvID: "c1", Seq: 5}})
	ev := <-events
	if ev.Kind != KindMessage || ev.Message == nil || ev.Message.Seq != 5 {
		t.Fatalf("event = %+v", ev)
	}

	cancel()
	waitUntil(t, func() bool { return len(srv.Subscribers()) == 0 })
	if _, ok := <-events; ok {
		t.Fatal("channel still open after cancel")
	}
	if err := c.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-b.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("backend Stop not called")
	}
}

func TestSlowSubscriberIsClosedOthersKeepUp(t *testing.T) {
	srv, sock := startServer(t, &stubBackend{})

	// The slow subscriber subscribes and then never reads again.
	slow, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	r := bufio.NewReader(slow)
	fmt.Fprintf(slow, `{"id":1,"op":"hello","args":{"Version":%d}}`+"\n", Version)
	fmt.Fprintln(slow, `{"id":2,"op":"subscribe"}`)
	for range 2 {
		if _, err := r.ReadBytes('\n'); err != nil {
			t.Fatal(err)
		}
	}

	fast := dialTest(t, sock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, err := fast.Subscribe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return len(srv.Subscribers()) == 2 })

	const n = 5000
	var count atomic.Int64
	bad := make(chan int64, 1)
	go func() {
		for ev := range events {
			if ev.Kind != KindMessage {
				continue
			}
			if ev.Message.Seq != count.Load()+1 {
				bad <- ev.Message.Seq
				return
			}
			count.Add(1)
		}
	}()
	text := strings.Repeat("x", 512)
	for i := 1; i <= n; i++ {
		// Keep within the fast subscriber's queue; the slow one never reads.
		for int64(i)-count.Load() > 512 {
			select {
			case s := <-bad:
				t.Fatalf("fast subscriber got seq %d out of order", s)
			default:
			}
			time.Sleep(time.Millisecond)
		}
		srv.Broadcast(Event{Kind: KindMessage, Message: &store.Message{ConvID: "c", Seq: int64(i), Text: text}})
	}
	waitUntil(t, func() bool { return count.Load() == n })
	waitUntil(t, func() bool { return len(srv.Subscribers()) == 1 })

	// The slow one's connection was closed: draining it ends in EOF.
	_ = slow.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatalf("slow subscriber not closed: %v", err)
	}
	// The daemon still answers.
	if _, err := fast.Status(); err != nil {
		t.Fatal(err)
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestHasSubscribers(t *testing.T) {
	srv, sock := startServer(t, &stubBackend{})
	if srv.HasSubscribers() {
		t.Fatal("HasSubscribers with no connection")
	}
	plain := dialTest(t, sock)
	if _, err := plain.Status(); err != nil {
		t.Fatal(err)
	}
	if srv.HasSubscribers() {
		t.Fatal("HasSubscribers with only an unsubscribed connection")
	}
	c := dialTest(t, sock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := c.Subscribe(ctx); err != nil {
		t.Fatal(err)
	}
	if !srv.HasSubscribers() {
		t.Fatal("HasSubscribers = false after Subscribe returned")
	}
	cancel()
	waitUntil(t, func() bool { return !srv.HasSubscribers() })
	if len(srv.Subscribers()) != 0 {
		t.Fatalf("Subscribers = %v after the subscription ended", srv.Subscribers())
	}
}
