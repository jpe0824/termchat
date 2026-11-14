package daemon

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/connector/fake"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/store"
)

// seqs lists the Seq of stored messages, oldest first.
func seqs(msgs []store.Message) []int64 {
	var out []int64
	for _, m := range msgs {
		if m.Status == "" {
			out = append(out, m.Seq)
		}
	}
	return out
}

func span(from, to int64) []int64 {
	var out []int64
	for s := from; s <= to; s++ {
		out = append(out, s)
	}
	return out
}

// messages builds n posted events from dana.
func messages(prefix string, n int) []model.Event {
	evs := make([]model.Event, n)
	for i := range evs {
		evs[i] = posted("dana", fmt.Sprintf("%s-%d", prefix, i+1), fmt.Sprintf("%s message %d", prefix, i+1))
	}
	return evs
}

// recorder wraps the fake and records request arguments.
type recorder struct {
	*fake.Fake
	mu      sync.Mutex
	syncs   []map[string]int64
	history []string
	sends   atomic.Int32
}

func (r *recorder) Sync(ctx context.Context, cursors map[string]int64) ([]model.Event, bool, []string, error) {
	r.mu.Lock()
	r.syncs = append(r.syncs, cloneMap(cursors))
	r.mu.Unlock()
	return r.Fake.Sync(ctx, cursors)
}

func (r *recorder) History(ctx context.Context, conv string, before int64, limit int) ([]model.Event, error) {
	r.mu.Lock()
	r.history = append(r.history, fmt.Sprintf("%s<%d", conv, before))
	r.mu.Unlock()
	return r.Fake.History(ctx, conv, before, limit)
}

func (r *recorder) Send(ctx context.Context, conv, msgID string, body model.Body, mentions []string) (model.Event, error) {
	r.sends.Add(1)
	return r.Fake.Send(ctx, conv, msgID, body, mentions)
}

func (r *recorder) syncCalls() []map[string]int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.syncs)
}

func (r *recorder) historyCalls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.history)
}

func cloneMap(m map[string]int64) map[string]int64 {
	out := make(map[string]int64, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func record(e *env) *recorder {
	r := &recorder{Fake: e.fake}
	e.conn = r
	return r
}

func TestFirstSyncFetches50PerConversation(t *testing.T) {
	e := newEnv(t)
	rec := record(e)
	e.fake.AddConversation(room("r1", "platform", "me", "dana"), messages("a", 120)...)
	e.fake.AddConversation(room("r2", "design", "me", "dana"), messages("b", 30)...)
	c := e.start()
	waitUntil(t, "both conversations", func() bool { return len(chatIDs(t, c)) == 2 })
	waitUntil(t, "first sync to finish", func() bool { return len(rec.syncCalls()) > 0 })

	// Read the store directly: Messages would fetch more from the server.
	r1, _ := e.st.Messages("r1", 0, 500)
	r2, _ := e.st.Messages("r2", 0, 500)
	if got := seqs(r1); !slices.Equal(got, span(71, 120)) {
		t.Fatalf("r1 holds %v, want 71..120", got)
	}
	if got := seqs(r2); !slices.Equal(got, span(1, 30)) {
		t.Fatalf("r2 holds %v, want 1..30", got)
	}
	if h := rec.historyCalls(); !slices.Equal(h, []string{"r1<0", "r2<0"}) {
		t.Fatalf("history calls = %v", h)
	}
	if got := rec.syncCalls()[0]; got["r1"] != 120 || got["r2"] != 30 {
		t.Fatalf("first Sync cursors = %v", got)
	}
}

func TestCatchUpUsesCursors(t *testing.T) {
	e := newEnv(t)
	rec := record(e)
	e.fake.AddConversation(room("r1", "platform", "me", "dana"), messages("a", 10)...)
	c := e.start()
	waitUntil(t, "first sync", func() bool { return len(chatIDs(t, c)) == 1 && len(rec.syncCalls()) == 1 })

	e.fake.SetState(connector.StateOffline)
	waitUntil(t, "offline", func() bool { st, _ := c.Status(); return st.Conn == connector.StateOffline })
	// Messages posted while offline reach the store only through Sync.
	e.fake.AddConversation(room("r1", "platform", "me", "dana"), messages("late", 5)...)
	e.fake.SetState(connector.StateConnected)
	waitUntil(t, "catch-up sync", func() bool { return len(rec.syncCalls()) == 2 })
	if got := rec.syncCalls()[1]; !maps1(got, "r1", 10) {
		t.Fatalf("catch-up cursors = %v, want r1:10", got)
	}
	waitUntil(t, "caught-up messages", func() bool {
		msgs, _ := e.st.Messages("r1", 0, 100)
		return slices.Equal(seqs(msgs), span(1, 15))
	})
}

func maps1(m map[string]int64, k string, v int64) bool { return len(m) == 1 && m[k] == v }

func TestSyncLoopsWhileMore(t *testing.T) {
	e := newEnv(t)
	rec := record(e)
	e.fake.AddConversation(room("r1", "platform", "me", "dana"), messages("a", 10)...)
	c := e.start()
	waitUntil(t, "first sync", func() bool { return len(chatIDs(t, c)) == 1 && len(rec.syncCalls()) == 1 })

	e.fake.SetState(connector.StateOffline)
	waitUntil(t, "offline", func() bool { st, _ := c.Status(); return st.Conn == connector.StateOffline })
	e.fake.AddConversation(room("r1", "platform", "me", "dana"), messages("late", 25)...)
	e.fake.SetPageSize(10)
	e.fake.SetState(connector.StateConnected)
	waitUntil(t, "all pages", func() bool {
		msgs, _ := e.st.Messages("r1", 0, 100)
		return slices.Equal(seqs(msgs), span(1, 35))
	})
	calls := rec.syncCalls()[1:]
	var cur []int64
	for _, m := range calls {
		cur = append(cur, m["r1"])
	}
	if !slices.Equal(cur, []int64{10, 20, 30}) {
		t.Fatalf("catch-up Sync cursors = %v, want [10 20 30]", cur)
	}
}

func TestUnlistedConversationDisappears(t *testing.T) {
	e := newEnv(t)
	e.fake.AddConversation(room("r1", "stays", "me", "dana"), posted("dana", "m1", "x"))
	e.fake.AddConversation(room("r3", "unlisted", "me", "dana"), posted("dana", "m3", "z"))
	c := e.start()
	waitUntil(t, "two conversations", func() bool { return len(chatIDs(t, c)) == 2 })
	events := e.subscribe()

	e.fake.SetState(connector.StateOffline)
	waitUntil(t, "offline", func() bool { st, _ := c.Status(); return st.Conn == connector.StateOffline })
	e.fake.AddConversation(room("r3", "unlisted", "dana")) // the user is no longer a member
	e.fake.SetState(connector.StateConnected)
	waitEvent(t, events, "r3 removed", func(ev ipc.Event) bool {
		return ev.Kind == ipc.KindConv && ev.Removed && ev.Conv.ID == "r3"
	})
	if got := chatIDs(t, c); !slices.Equal(got, []string{"r1"}) {
		t.Fatalf("Chats = %v, want [r1]", got)
	}
}

// goneConn reports one conversation as gone from Sync.
type goneConn struct {
	*recorder
	gone atomic.Value // string
}

func (g *goneConn) Sync(ctx context.Context, cursors map[string]int64) ([]model.Event, bool, []string, error) {
	evs, more, gone, err := g.recorder.Sync(ctx, cursors)
	id, _ := g.gone.Load().(string)
	if _, ok := cursors[id]; ok && err == nil {
		gone = append(gone, id)
	}
	return evs, more, gone, err
}

func TestSyncGoneListRemovesConversation(t *testing.T) {
	e := newEnv(t)
	rec := record(e)
	gc := &goneConn{recorder: rec}
	e.conn = gc
	e.fake.AddConversation(room("r1", "stays", "me", "dana"), posted("dana", "m1", "x"))
	e.fake.AddConversation(room("r2", "removed", "me", "dana"), posted("dana", "m2", "y"))
	c := e.start()
	waitUntil(t, "two conversations", func() bool { return len(chatIDs(t, c)) == 2 })

	e.fake.SetState(connector.StateOffline)
	waitUntil(t, "offline", func() bool { st, _ := c.Status(); return st.Conn == connector.StateOffline })
	gc.gone.Store("r2")
	e.fake.SetState(connector.StateConnected)
	waitUntil(t, "r2 gone from Chats", func() bool { return slices.Equal(chatIDs(t, c), []string{"r1"}) })
}

func TestMalformedEventSkippedRestApplied(t *testing.T) {
	e := newEnv(t)
	rec := record(e)
	e.fake.AddConversation(room("r1", "platform", "me", "dana"), messages("a", 3)...)
	c := e.start()
	waitUntil(t, "first sync", func() bool { return len(chatIDs(t, c)) == 1 && len(rec.syncCalls()) == 1 })

	e.fake.SetState(connector.StateOffline)
	waitUntil(t, "offline", func() bool { st, _ := c.Status(); return st.Conn == connector.StateOffline })
	bad := posted("dana", "", "no message id") // the store rejects this
	e.fake.AddConversation(room("r1", "platform", "me", "dana"),
		posted("dana", "ok-4", "four"), bad, posted("dana", "ok-6", "six"))
	e.fake.SetState(connector.StateConnected)
	waitUntil(t, "the good events", func() bool {
		msgs, _ := e.st.Messages("r1", 0, 100)
		return slices.Equal(seqs(msgs), []int64{1, 2, 3, 4, 6})
	})
	// The sync finished rather than looping on the bad event.
	waitUntil(t, "online again", func() bool { st, _ := c.Status(); return st.Conn == connector.StateConnected })
	n := len(rec.syncCalls())
	time.Sleep(50 * time.Millisecond)
	if len(rec.syncCalls()) != n {
		t.Fatalf("Sync keeps being called: %d then %d", n, len(rec.syncCalls()))
	}
}

func TestLiveEventDeliveredDuringBackfill(t *testing.T) {
	e := newEnv(t)
	rec := record(e)
	e.fake.AddConversation(room("big", "big", "me", "dana"), messages("h", 10000)...)
	e.fake.AddConversation(room("r2", "small", "me", "dana"))
	c := e.start()
	waitUntil(t, "first sync", func() bool { return len(chatIDs(t, c)) == 2 && len(rec.syncCalls()) == 1 })
	events := e.subscribe()

	// Keep the backfill running flat out.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				e.clock.Advance(backfillInterval)
				time.Sleep(time.Millisecond)
			}
		}
	}()
	waitUntil(t, "backfill progress", func() bool { return len(rec.historyCalls()) > 5 })
	if st, _ := c.Status(); !st.Syncing {
		t.Fatal("Status.Syncing false while backfilling")
	}

	start := time.Now()
	e.fake.Push(model.Event{ConvID: "r2", Kind: model.MessagePosted, Actor: "dana", MsgID: "live",
		Body: &model.Body{Format: "text/plain", Data: []byte("live one")}})
	waitEvent(t, events, "the live message", func(ev ipc.Event) bool {
		return ev.Kind == ipc.KindMessage && ev.Message.MsgID == "live"
	})
	d := time.Since(start)
	if d > 100*time.Millisecond {
		t.Fatalf("live message took %v during backfill", d)
	}
	t.Logf("live message delivered in %v after %d backfill requests", d, len(rec.historyCalls()))
}

func TestBackfillThrottledRoundRobinAndPausedOffline(t *testing.T) {
	e := newEnv(t)
	rec := record(e)
	e.fake.AddConversation(room("a", "a", "me", "dana"), messages("a", 200)...)
	e.fake.AddConversation(room("b", "b", "me", "dana"), messages("b", 200)...)
	c := e.start()
	waitUntil(t, "first sync", func() bool { return len(chatIDs(t, c)) == 2 && len(rec.syncCalls()) == 1 })
	first := len(rec.historyCalls()) // the two first pages

	time.Sleep(20 * time.Millisecond)
	if n := len(rec.historyCalls()); n != first {
		t.Fatalf("backfill ran without the clock advancing: %v", rec.historyCalls())
	}
	for i := 1; i <= 4; i++ {
		e.clock.tick(t, backfillInterval)
		waitUntil(t, "one backfill request", func() bool { return len(rec.historyCalls()) == first+i })
	}
	if got := rec.historyCalls()[first:]; !slices.Equal(got, []string{"a<151", "b<151", "a<101", "b<101"}) {
		t.Fatalf("backfill order = %v", got)
	}

	e.fake.SetState(connector.StateOffline)
	waitUntil(t, "offline", func() bool { st, _ := c.Status(); return st.Conn == connector.StateOffline })
	n := len(rec.historyCalls())
	for range 5 {
		e.clock.Advance(backfillInterval)
	}
	time.Sleep(20 * time.Millisecond)
	if len(rec.historyCalls()) != n {
		t.Fatalf("backfill ran while offline: %v", rec.historyCalls()[n:])
	}
}

func TestBackfillBacksOffWhenRateLimited(t *testing.T) {
	e := newEnv(t)
	rl := &rateLimited{recorder: record(e)}
	e.conn = rl
	e.fake.AddConversation(room("a", "a", "me", "dana"), messages("a", 200)...)
	c := e.start()
	waitUntil(t, "first sync", func() bool { return len(chatIDs(t, c)) == 1 && len(rl.syncCalls()) == 1 })
	rl.limited.Store(true)
	first := len(rl.historyCalls())
	e.clock.tick(t, backfillInterval)
	waitUntil(t, "the limited request", func() bool { return len(rl.historyCalls()) == first+1 })
	rl.limited.Store(false)
	e.clock.tick(t, backfillInterval) // the backoff doubled the wait: not yet
	time.Sleep(20 * time.Millisecond)
	if n := len(rl.historyCalls()); n != first+1 {
		t.Fatalf("backfill retried without backing off: %v", rl.historyCalls())
	}
	e.clock.tick(t, backfillInterval)
	waitUntil(t, "the retry", func() bool { return len(rl.historyCalls()) == first+2 })
}

type rateLimited struct {
	*recorder
	limited atomic.Bool
}

func (r *rateLimited) History(ctx context.Context, conv string, before int64, limit int) ([]model.Event, error) {
	evs, err := r.recorder.History(ctx, conv, before, limit)
	if r.limited.Load() {
		return nil, connector.ErrRateLimited
	}
	return evs, err
}

func TestMessagesFetchesOlderHistory(t *testing.T) {
	e := newEnv(t)
	e.fake.AddConversation(room("r1", "platform", "me", "dana"), messages("a", 300)...)
	c := e.start()
	waitUntil(t, "first sync", func() bool { return len(chatIDs(t, c)) == 1 })

	msgs, err := c.Messages("r1", 251, 120)
	if err != nil {
		t.Fatal(err)
	}
	if got := seqs(msgs); !slices.Equal(got, span(131, 250)) {
		t.Fatalf("Messages(before 251, 120) = %v, want 131..250", got)
	}

	e.fake.SetState(connector.StateOffline)
	waitUntil(t, "offline", func() bool { st, _ := c.Status(); return st.Conn == connector.StateOffline })
	msgs, err = c.Messages("r1", 131, 500)
	if err != nil {
		t.Fatal(err)
	}
	if got := seqs(msgs); len(got) == 0 || got[len(got)-1] != 130 || got[0] <= 1 {
		t.Fatalf("offline Messages = %d messages ending %v; want only what is held", len(got), got)
	}
}

func TestLocalReadPushedOnReconnect(t *testing.T) {
	e := newEnv(t)
	rec := record(e)
	e.fake.AddConversation(room("r1", "platform", "me", "dana"), messages("a", 5)...)
	c := e.start()
	waitUntil(t, "first sync", func() bool { return len(chatIDs(t, c)) == 1 && len(rec.syncCalls()) == 1 })
	e.fake.SetState(connector.StateOffline)
	waitUntil(t, "offline", func() bool { st, _ := c.Status(); return st.Conn == connector.StateOffline })
	if err := c.MarkRead("r1"); err != nil {
		t.Fatalf("offline MarkRead: %v", err)
	}
	e.fake.SetState(connector.StateConnected)
	waitUntil(t, "read position on the server", func() bool {
		mine, _, err := e.fake.Conversations(context.Background())
		return err == nil && len(mine) == 1 && mine[0].ReadSeq == 5
	})
}

func TestUnknownHandleRefreshesPeople(t *testing.T) {
	e := newEnv(t)
	e.fake.AddConversation(room("r1", "platform", "me", "dana"))
	c := e.start()
	waitUntil(t, "first sync", func() bool { return len(chatIDs(t, c)) == 1 })
	e.fake.AddPeople(model.Person{Handle: "zoe", DisplayName: "Zoe Z"})
	e.fake.Push(model.Event{ConvID: "r1", Kind: model.MessagePosted, Actor: "zoe", MsgID: "z1",
		Body: &model.Body{Format: "text/plain", Data: []byte("hi")}})
	waitUntil(t, "zoe's display name", func() bool {
		msgs, _ := c.Messages("r1", 0, 10)
		return len(msgs) == 1 && msgs[0].AuthorName == "Zoe Z"
	})
}
