package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/store"
)

// fakeNoteStore holds one DM and counts the reads made of it.
type fakeNoteStore struct {
	mu          sync.Mutex
	convMissing int    // ConvView fails with ErrNotFound this many more times
	msgMissing  int    // ByMsgID fails with ErrNotFound this many more times
	onView      func() // called once, by the next ConvView
	byMsg, view atomic.Int32
	msg         store.Message
	conv        store.ConvView
}

func newFakeNoteStore() *fakeNoteStore {
	return &fakeNoteStore{
		msg:  store.Message{ConvID: "d1", MsgID: "m1", Author: "dana", AuthorName: "Dana", Seq: 3, Text: "SECRET-TEXT"},
		conv: store.ConvView{Conversation: model.Conversation{ID: "d1", Kind: model.ConvDM, Members: []string{"me", "dana"}}},
	}
}

func (f *fakeNoteStore) Me() string { return "me" }

func (f *fakeNoteStore) ByMsgID(conv, msgID string) (store.Message, error) {
	f.byMsg.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.msgMissing > 0 {
		f.msgMissing--
		return store.Message{}, store.ErrNotFound
	}
	return f.msg, nil
}

func (f *fakeNoteStore) ConvView(conv string) (store.ConvView, error) {
	f.view.Add(1)
	f.mu.Lock()
	hook := f.onView
	f.onView = nil
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.convMissing > 0 {
		f.convMissing--
		return store.ConvView{}, store.ErrNotFound
	}
	return f.conv, nil
}

func (f *fakeNoteStore) reads() int32 { return f.byMsg.Load() + f.view.Load() }

type fakeNoteServer struct {
	mu   sync.Mutex
	subs []ipc.Subscriber
	sent []ipc.NotifyPayload
	// gone: the listed UIs detach before a notify event reaches them.
	gone bool
}

func (s *fakeNoteServer) Subscribers() []ipc.Subscriber {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ipc.Subscriber(nil), s.subs...)
}

func (s *fakeNoteServer) BroadcastNotify(ev ipc.Event) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gone {
		return 0
	}
	s.sent = append(s.sent, *ev.Notify)
	n := 0
	for _, sub := range s.subs {
		if sub.CanNotify {
			n++
		}
	}
	return n
}

func unitNotifications(t *testing.T, mut func(*config.Config)) (*notifications, *fakeNoteStore, *noteRec, *fakeNoteServer, *bytes.Buffer) {
	t.Helper()
	cfg := &config.Config{}
	if mut != nil {
		mut(cfg)
	}
	st, rec, srv := newFakeNoteStore(), &noteRec{}, &fakeNoteServer{}
	logs := &bytes.Buffer{}
	n := newNotifications(func() *config.Config { return cfg }, rec, srv, st, slog.New(slog.NewTextHandler(&lockedWriter{w: logs}, nil)), newFakeClock())
	return n, st, rec, srv, logs
}

func (l *lockedWriter) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.String()
}

type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

var unitEvent = model.Event{ConvID: "d1", Seq: 3, Kind: model.MessagePosted, Actor: "dana", MsgID: "m1"}

func osOnly(c *config.Config) { c.Notify.OS = config.OSFallback }

func TestConcurrentConsiderationsNotifyOnce(t *testing.T) {
	n, _, rec, _, _ := unitNotifications(t, osOnly)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n.consider([]model.Event{unitEvent}, livePush)
		}()
	}
	wg.Wait()
	if got := rec.osCalls(); len(got) != 1 {
		t.Fatalf("%d notifications, want exactly one: %+v", len(got), got)
	}
}

func TestNoRouteMeansNoStoreReads(t *testing.T) {
	for name, mut := range map[string]func(*config.Config){
		"everything off":             func(c *config.Config) { c.Notify.Terminal = false },
		"terminal on, no capable UI": func(c *config.Config) { c.Notify.Terminal = true },
	} {
		t.Run(name, func(t *testing.T) {
			n, st, rec, srv, _ := unitNotifications(t, mut)
			srv.subs = []ipc.Subscriber{{UI: true}} // attached, cannot show notifications
			n.consider([]model.Event{unitEvent}, livePush)
			if st.reads() != 0 {
				t.Errorf("%d notification reads; want none", st.reads())
			}
			if len(rec.osCalls())+len(rec.hookCalls())+len(srv.sent) != 0 {
				t.Errorf("something was raised")
			}
		})
	}
}

func TestEachNotifiedMessageIsReadOnce(t *testing.T) {
	n, st, rec, _, _ := unitNotifications(t, osOnly)
	n.consider([]model.Event{unitEvent}, livePush)
	if len(rec.osCalls()) != 1 {
		t.Fatalf("notifications = %+v", rec.osCalls())
	}
	if st.byMsg.Load() != 1 || st.view.Load() != 1 {
		t.Errorf("reads: %d message, %d conversation; want 1 and 1", st.byMsg.Load(), st.view.Load())
	}
}

func TestPayloadFieldsAreBounded(t *testing.T) {
	long := strings.Repeat("é", 300)
	p := payload(store.Message{ConvID: "r1", AuthorName: long, Text: long},
		model.Conversation{ID: "r1", Kind: model.ConvRoom, Name: long}, "me")
	want64 := strings.Repeat("é", 64) + "…"
	if p.Sender != want64 || p.ConvName != want64 {
		t.Errorf("Sender %d runes, ConvName %d runes; want 64 plus an ellipsis", len([]rune(p.Sender)), len([]rune(p.ConvName)))
	}
	if want := strings.Repeat("é", 200) + "…"; p.Text != want {
		t.Errorf("Text is %d runes", len([]rune(p.Text)))
	}
}

func TestBatchClaimsTheOlderMessagesThatLoseToTheNewest(t *testing.T) {
	n, st, rec, _, _ := unitNotifications(t, osOnly)
	older := model.Event{ConvID: "d1", Seq: 2, Kind: model.MessagePosted, Actor: "dana", MsgID: "m0"}
	newer := unitEvent
	batch := newCatchUp()
	n.consider([]model.Event{older, newer}, mode{batch: batch})
	n.endCatchUp(batch)
	if got := rec.osCalls(); len(got) != 1 {
		t.Fatalf("batch raised %d notifications, want 1", len(got))
	}
	reads := st.reads()
	// A live push racing the catch-up for the older message finds it claimed.
	n.consider([]model.Event{older}, livePush)
	if got := rec.osCalls(); len(got) != 1 {
		t.Fatalf("the older message notified on its own: %+v", got)
	}
	if st.reads() != reads {
		t.Errorf("claimed message was read again")
	}
}

func msgEvent(seq int64, msgID string) model.Event {
	return model.Event{ConvID: "d1", Seq: seq, Kind: model.MessagePosted, Actor: "dana", MsgID: msgID}
}

func TestRetriedCatchUpMessageKeepsOneNotificationPerConversation(t *testing.T) {
	for name, failing := range map[string]int{
		"catch-up notified the conversation":       1, // the newest fails, the older one wins
		"catch-up did not notify the conversation": 2, // both fail; the first retry is the one
	} {
		t.Run(name, func(t *testing.T) {
			n, st, rec, _, _ := unitNotifications(t, osOnly)
			st.convMissing = failing
			batch := newCatchUp()
			n.consider([]model.Event{msgEvent(3, "m3"), msgEvent(4, "m4")}, mode{batch: batch})
			n.endCatchUp(batch)
			n.clock.(*fakeClock).Advance(time.Second)
			n.retryDue(context.Background())
			if got := rec.osCalls(); len(got) != 1 {
				t.Fatalf("one catch-up notified its conversation %d times: %+v", len(got), got)
			}
		})
	}
}

func TestRetryQueueStaysBoundedWhenRetriesFail(t *testing.T) {
	n, st, _, _, logs := unitNotifications(t, osOnly)
	st.convMissing = 1 << 20
	for i := range maxPending {
		n.consider([]model.Event{msgEvent(int64(i+1), fmt.Sprintf("m%d", i+1))}, livePush)
	}
	// A new message fails while the due ones are out for their retry.
	st.onView = func() { n.consider([]model.Event{msgEvent(1000, "late")}, livePush) }
	n.clock.(*fakeClock).Advance(time.Second)
	n.retryDue(context.Background())
	if got := strings.Count(logs.String(), "dropped pending"); got != 1 {
		t.Fatalf("%d dropped lines after %d failures wanted a place; want 1", got, maxPending+1)
	}
}

func TestMessageIsNotQueuedTwice(t *testing.T) {
	n, st, _, _, logs := unitNotifications(t, osOnly)
	st.convMissing = 1 << 20
	n.consider([]model.Event{unitEvent}, livePush)
	// The same message fails again while it is out for its first retry.
	st.onView = func() { n.consider([]model.Event{unitEvent}, livePush) }
	clock := n.clock.(*fakeClock)
	for range 10 {
		clock.Advance(30 * time.Second)
		n.retryDue(context.Background())
	}
	if got := strings.Count(logs.String(), "gave up"); got != 1 {
		t.Fatalf("%d gave-up lines for one message, want 1", got)
	}
}

func TestUIThatDetachesBeforeTheSendFallsBackToOS(t *testing.T) {
	n, _, rec, srv, _ := unitNotifications(t, func(c *config.Config) { c.Notify.Terminal, c.Notify.OS = true, config.OSFallback })
	srv.subs = []ipc.Subscriber{{UI: true, CanNotify: true}}
	srv.gone = true
	n.consider([]model.Event{unitEvent}, livePush)
	if got := rec.osCalls(); len(got) != 1 {
		t.Fatalf("OS notifications = %+v, want one when no UI took the event", got)
	}
}

func TestOneCallReadsEachConversationOnceAndRuledOutMessagesNever(t *testing.T) {
	n, st, rec, _, _ := unitNotifications(t, func(c *config.Config) {
		c.Notify.OS = config.OSFallback
		c.Rooms = map[string]string{"d1": "muted"}
	})
	st.conv.Kind = model.ConvRoom
	n.consider([]model.Event{msgEvent(3, "m3"), msgEvent(4, "m4"), msgEvent(5, "m5")}, livePush)
	if st.view.Load() != 1 || st.byMsg.Load() != 0 {
		t.Errorf("reads: %d conversation, %d message; want 1 and 0", st.view.Load(), st.byMsg.Load())
	}
	if got := rec.osCalls(); len(got) != 0 {
		t.Errorf("muted room notified %+v", got)
	}
}

func TestClaimsForgetTheOldestBeyondTheirSize(t *testing.T) {
	c := claims{held: map[msgKey]bool{}}
	key := func(i int) msgKey { return msgKey{"c", fmt.Sprint(i)} }
	for i := range notifiedMemory + 1 {
		if !c.claim(key(i)) {
			t.Fatalf("claim %d refused", i)
		}
	}
	if c.claim(key(notifiedMemory)) || !c.has(key(1)) {
		t.Fatal("a recent claim was forgotten")
	}
	if c.has(key(0)) || len(c.held) != notifiedMemory {
		t.Fatalf("oldest claim kept, or %d held; want %d", len(c.held), notifiedMemory)
	}
}

func TestFirstFillMessageWhoseReadFailsIsRetried(t *testing.T) {
	n, st, rec, _, _ := unitNotifications(t, osOnly)
	st.msgMissing = 1
	batch := newCatchUp()
	n.consider([]model.Event{unitEvent}, mode{batch: batch}) // a new conversation's first page
	n.endCatchUp(batch)
	if got := rec.osCalls(); len(got) != 0 {
		t.Fatalf("notified %+v before the message could be read", got)
	}
	n.clock.(*fakeClock).Advance(time.Second)
	n.retryDue(context.Background())
	if got := rec.osCalls(); len(got) != 1 {
		t.Fatalf("notified %+v after the retry, want one", got)
	}
}
