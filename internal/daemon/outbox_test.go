package daemon

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/store"
)

// outbox reads conv's outbox from the store.
func (e *env) outbox(conv string) []store.OutboxItem {
	e.t.Helper()
	items, err := e.st.Outbox(conv)
	if err != nil {
		e.t.Fatal(err)
	}
	return items
}

func sentTexts(e *env) []string {
	var out []string
	for _, ev := range e.fake.Sent() {
		out = append(out, string(ev.Body.Data))
	}
	return out
}

// goOffline drops the fake connection and waits for the daemon to notice.
func (e *env) goOffline(c *ipc.Client) {
	e.t.Helper()
	e.fake.SetState(connector.StateOffline)
	waitUntil(e.t, "offline", func() bool { st, _ := c.Status(); return st.Conn == connector.StateOffline })
}

func startWithRoom(t *testing.T, e *env) *ipc.Client {
	t.Helper()
	e.fake.AddConversation(room("r1", "platform", "me", "dana"))
	c := e.start()
	waitUntil(t, "the room", func() bool { return len(chatIDs(t, c)) == 1 })
	return c
}

func TestSendShowsSendingThenSent(t *testing.T) {
	e := newEnv(t)
	c := startWithRoom(t, e)
	events := e.subscribe()

	id, err := c.Send("r1", "hello there", "tui")
	if err != nil {
		t.Fatal(err)
	}
	ev := waitEvent(t, events, "outbox sending", func(ev ipc.Event) bool { return ev.Kind == ipc.KindOutbox })
	if ev.Message.MsgID != id || ev.Message.Status != store.StatusSending || ev.Message.Text != "hello there" ||
		ev.Outbox.State != store.StatusSending {
		t.Fatalf("first outbox event = %+v / %+v", ev.Message, ev.Outbox)
	}
	ev = waitEvent(t, events, "the posted message", func(ev ipc.Event) bool { return ev.Kind == ipc.KindMessage })
	if ev.Message.MsgID != id || ev.Message.Status != "" || ev.Message.Seq != 1 || ev.Message.Author != "me" {
		t.Fatalf("message event = %+v", ev.Message)
	}
	if items := e.outbox("r1"); len(items) != 0 {
		t.Fatalf("outbox after send = %+v", items)
	}
	if got := sentTexts(e); !slices.Equal(got, []string{"hello there"}) {
		t.Fatalf("server got %v", got)
	}
	msgs, _ := c.Messages("r1", 0, 10)
	if len(msgs) != 1 || msgs[0].MsgID != id || msgs[0].Status != "" {
		t.Fatalf("Messages = %+v", msgs)
	}
}

func TestSendWhileOfflineWaitsThenSends(t *testing.T) {
	e := newEnv(t)
	c := startWithRoom(t, e)
	e.goOffline(c)

	for _, text := range []string{"one", "two", "three"} {
		if _, err := c.Send("r1", text, "tui"); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(20 * time.Millisecond)
	if n := len(e.fake.Sent()); n != 0 {
		t.Fatalf("%d sent while offline", n)
	}
	msgs, _ := c.Messages("r1", 0, 10)
	if len(msgs) != 3 || msgs[0].Status != store.StatusSending {
		t.Fatalf("offline Messages = %+v", msgs)
	}
	if st, _ := c.Status(); st.Pending != 3 {
		t.Fatalf("Pending = %d, want 3", st.Pending)
	}

	e.fake.SetState(connector.StateConnected)
	waitUntil(t, "all three sent", func() bool { return len(e.fake.Sent()) == 3 })
	if got := sentTexts(e); !slices.Equal(got, []string{"one", "two", "three"}) {
		t.Fatalf("sent in order %v", got)
	}
	waitUntil(t, "an empty outbox", func() bool { return len(e.outbox("r1")) == 0 })
}

func TestRetryAfterAmbiguousFailureDoesNotDuplicate(t *testing.T) {
	e := newEnv(t)
	rec := record(e)
	c := startWithRoom(t, e)

	e.fake.LoseNextSendResponse()
	id, err := c.Send("r1", "only once", "tui")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "an empty outbox", func() bool { return len(e.outbox("r1")) == 0 })
	if n := rec.sends.Load(); n != 2 {
		t.Fatalf("Send attempts = %d, want 2 (lost response, then resend)", n)
	}
	sent := e.fake.Sent()
	if len(sent) != 1 || sent[0].MsgID != id {
		t.Fatalf("server holds %+v, want exactly one message %s", sent, id)
	}
	msgs, _ := c.Messages("r1", 0, 10)
	if len(msgs) != 1 || msgs[0].MsgID != id || msgs[0].Status != "" {
		t.Fatalf("Messages = %+v", msgs)
	}
}

func TestOutboxExpiresAfterTenMinutes(t *testing.T) {
	e := newEnv(t)
	c := startWithRoom(t, e)
	e.goOffline(c)
	events := e.subscribe()

	id, err := c.Send("r1", "too late", "tui")
	if err != nil {
		t.Fatal(err)
	}
	e.clock.tick(t, 10*time.Minute) // exactly ten minutes: not yet
	time.Sleep(20 * time.Millisecond)
	if items := e.outbox("r1"); len(items) != 1 || items[0].State != store.StatusSending {
		t.Fatalf("outbox at 10m = %+v", items)
	}
	e.clock.tick(t, time.Second)
	ev := waitEvent(t, events, "the item failing", func(ev ipc.Event) bool {
		return ev.Kind == ipc.KindOutbox && ev.Outbox.State == store.StatusFailed
	})
	if ev.Outbox.MsgID != id || ev.Outbox.Err != "expired" || ev.Message.Status != store.StatusFailed {
		t.Fatalf("failed event = %+v / %+v", ev.Outbox, ev.Message)
	}
	if items := e.outbox("r1"); len(items) != 1 || items[0].State != store.StatusFailed || items[0].Err != "expired" {
		t.Fatalf("outbox = %+v", items)
	}

	e.fake.SetState(connector.StateConnected)
	waitUntil(t, "online", func() bool { st, _ := c.Status(); return st.Conn == connector.StateConnected })
	time.Sleep(20 * time.Millisecond)
	if n := len(e.fake.Sent()); n != 0 {
		t.Fatalf("expired item was sent (%d)", n)
	}
}

func TestExpiryCheckedAfterRestart(t *testing.T) {
	e := newEnv(t)
	c := startWithRoom(t, e)
	e.goOffline(c)
	if _, err := c.Send("r1", "queued before restart", "tui"); err != nil {
		t.Fatal(err)
	}
	e.stop()

	e.clock.Advance(10*time.Minute + time.Second)
	e.fake.SetState(connector.StateConnected)
	e.start()
	waitUntil(t, "the item to expire", func() bool {
		items := e.outbox("r1")
		return len(items) == 1 && items[0].State == store.StatusFailed && items[0].Err == "expired"
	})
	time.Sleep(20 * time.Millisecond)
	if n := len(e.fake.Sent()); n != 0 {
		t.Fatalf("expired item was sent after restart (%d)", n)
	}
}

func TestPendingSentAfterRestart(t *testing.T) {
	e := newEnv(t)
	c := startWithRoom(t, e)
	e.goOffline(c)
	if _, err := c.Send("r1", "survives a restart", "tui"); err != nil {
		t.Fatal(err)
	}
	e.stop()
	e.clock.Advance(9 * time.Minute)
	e.fake.SetState(connector.StateConnected)
	e.start()
	waitUntil(t, "the item to be sent", func() bool { return len(e.fake.Sent()) == 1 })
	waitUntil(t, "an empty outbox", func() bool { return len(e.outbox("r1")) == 0 })
}

func TestPermanentFailureMarkedFailedThenRetried(t *testing.T) {
	e := newEnv(t)
	c := startWithRoom(t, e)

	e.fake.FailNextSend(fmt.Errorf("%w: you may not post here", connector.ErrForbidden))
	id, err := c.Send("r1", "denied", "tui")
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the item to fail", func() bool {
		items := e.outbox("r1")
		return len(items) == 1 && items[0].State == store.StatusFailed
	})
	if it := e.outbox("r1")[0]; it.Err != "forbidden: you may not post here" {
		t.Fatalf("reason = %q", it.Err)
	}
	msgs, _ := c.Messages("r1", 0, 10)
	if len(msgs) != 1 || msgs[0].MsgID != id || msgs[0].Status != store.StatusFailed || msgs[0].Text != "denied" {
		t.Fatalf("Messages = %+v", msgs)
	}
	if n := len(e.fake.Sent()); n != 0 {
		t.Fatalf("failed send recorded %d messages", n)
	}

	if err := c.Retry(id); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the retried send", func() bool { return len(e.fake.Sent()) == 1 })
	waitUntil(t, "an empty outbox", func() bool { return len(e.outbox("r1")) == 0 })
	if err := c.Retry(id); !errors.Is(err, ipc.ErrNotFound) {
		t.Fatalf("Retry of a sent item = %v, want ErrNotFound", err)
	}
}

func TestDiscardRemovesFailedItem(t *testing.T) {
	e := newEnv(t)
	c := startWithRoom(t, e)
	e.fake.FailNextSend(fmt.Errorf("%w: gone", connector.ErrNotFound))
	id, _ := c.Send("r1", "never mind", "tui")
	waitUntil(t, "the item to fail", func() bool {
		items := e.outbox("r1")
		return len(items) == 1 && items[0].State == store.StatusFailed
	})
	if err := c.Discard(id); err != nil {
		t.Fatal(err)
	}
	if items := e.outbox("r1"); len(items) != 0 {
		t.Fatalf("outbox = %+v", items)
	}
	if msgs, _ := c.Messages("r1", 0, 10); len(msgs) != 0 {
		t.Fatalf("Messages = %+v", msgs)
	}
	if err := c.Discard(id); !errors.Is(err, ipc.ErrNotFound) {
		t.Fatalf("second Discard = %v, want ErrNotFound", err)
	}
}

// stuckConn rate-limits every send to one conversation.
type stuckConn struct {
	*recorder
	stuck    atomic.Value // conversation id
	attempts atomic.Int32
}

func (s *stuckConn) Send(ctx context.Context, conv, msgID string, body model.Body, mentions []string) (model.Event, error) {
	if id, _ := s.stuck.Load().(string); id == conv {
		s.attempts.Add(1)
		return model.Event{}, connector.ErrRateLimited
	}
	return s.recorder.Send(ctx, conv, msgID, body, mentions)
}

func TestStuckConversationDoesNotBlockAnother(t *testing.T) {
	e := newEnv(t)
	sc := &stuckConn{recorder: record(e)}
	sc.stuck.Store("a")
	e.conn = sc
	e.fake.AddConversation(room("a", "a", "me", "dana"))
	e.fake.AddConversation(room("b", "b", "me", "dana"))
	c := e.start()
	waitUntil(t, "both rooms", func() bool { return len(chatIDs(t, c)) == 2 })

	if _, err := c.Send("a", "stuck", "tui"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the first attempt on a", func() bool { return sc.attempts.Load() == 1 })
	if _, err := c.Send("b", "flows", "tui"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "b's message", func() bool { return slices.Equal(sentTexts(e), []string{"flows"}) })
	if items := e.outbox("a"); len(items) != 1 || items[0].State != store.StatusSending {
		t.Fatalf("a's outbox = %+v", items)
	}

	sc.stuck.Store("")
	e.clock.tick(t, time.Second) // the first backoff
	waitUntil(t, "a's message", func() bool { return slices.Equal(sentTexts(e), []string{"flows", "stuck"}) })
}

func TestSendValidation(t *testing.T) {
	e := newEnv(t)
	c := startWithRoom(t, e)
	cases := []struct {
		conv, text, source string
		want               error
	}{
		{"r1", "", "tui", ipc.ErrInvalid},
		{"r1", " \n\t ", "tui", ipc.ErrInvalid},
		{"r1", strings.Repeat("x", 8193), "tui", ipc.ErrInvalid},
		{"r1", "hi", "bot", ipc.ErrInvalid},
		{"nope", "hi", "tui", ipc.ErrNotFound},
	}
	for _, tc := range cases {
		if _, err := c.Send(tc.conv, tc.text, tc.source); !errors.Is(err, tc.want) {
			t.Errorf("Send(%q, %d bytes, %q) = %v, want %v", tc.conv, len(tc.text), tc.source, err, tc.want)
		}
	}
	if _, err := c.Send("r1", strings.Repeat("x", 8192), "tui"); err != nil {
		t.Fatalf("8192 bytes refused: %v", err)
	}
	waitUntil(t, "the long message", func() bool { return len(e.fake.Sent()) == 1 })
}

func TestMentionsExtracted(t *testing.T) {
	e := newEnv(t)
	e.fake.AddConversation(room("r1", "platform", "me", "dana", "bob"))
	c := e.start()
	waitUntil(t, "the room", func() bool { return len(chatIDs(t, c)) == 1 })
	text := "@Bob and @dana, again @DANA; @eve is not here; mail x@dana.com"
	if _, err := c.Send("r1", text, "tui"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the message", func() bool { return len(e.fake.Sent()) == 1 })
	if got := e.fake.Sent()[0].Mentions; !slices.Equal(got, []string{"bob", "dana"}) {
		t.Fatalf("mentions = %v, want [bob dana]", got)
	}
}
