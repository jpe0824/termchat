package daemon

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jpe0824/termchat/internal/config"
	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/store"
)

// fakeNoteStore holds one DM and counts the reads made of it.
type fakeNoteStore struct {
	mu          sync.Mutex
	convMissing int // ConvView fails with ErrNotFound this many more times
	byMsg, view atomic.Int32
	held        map[int64]bool
	msg         store.Message
	conv        store.ConvView
}

func newFakeNoteStore() *fakeNoteStore {
	return &fakeNoteStore{
		held: map[int64]bool{},
		msg:  store.Message{ConvID: "d1", MsgID: "m1", Author: "dana", AuthorName: "Dana", Seq: 3, Text: "SECRET-TEXT"},
		conv: store.ConvView{Conversation: model.Conversation{ID: "d1", Kind: model.ConvDM, Members: []string{"me", "dana"}}},
	}
}

func (f *fakeNoteStore) Me() string { return "me" }

func (f *fakeNoteStore) HasEvent(conv string, seq int64) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.held[seq]
}

func (f *fakeNoteStore) ByMsgID(conv, msgID string) (store.Message, error) {
	f.byMsg.Add(1)
	return f.msg, nil
}

func (f *fakeNoteStore) ConvView(conv string) (store.ConvView, error) {
	f.view.Add(1)
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
}

func (s *fakeNoteServer) Subscribers() []ipc.Subscriber {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ipc.Subscriber(nil), s.subs...)
}

func (s *fakeNoteServer) BroadcastNotify(ev ipc.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, *ev.Notify)
}

func unitNotifications(t *testing.T, mut func(*config.Config)) (*notifications, *fakeNoteStore, *noteRec, *fakeNoteServer, *bytes.Buffer) {
	t.Helper()
	cfg := &config.Config{}
	if mut != nil {
		mut(cfg)
	}
	st, rec, srv := newFakeNoteStore(), &noteRec{}, &fakeNoteServer{}
	logs := &bytes.Buffer{}
	n := newNotifications(cfg, rec, srv, st, slog.New(slog.NewTextHandler(&lockedWriter{w: logs}, nil)))
	return n, st, rec, srv, logs
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

func osOnly(c *config.Config) { c.Notify.OS = true }

func TestUnevaluableMessageIsEligibleAgain(t *testing.T) {
	n, st, rec, _, logs := unitNotifications(t, osOnly)
	st.convMissing = 1 // the conversation row is not stored yet
	n.consider([]model.Event{unitEvent}, nil)
	if got := rec.osCalls(); len(got) != 0 {
		t.Fatalf("notified %+v before the conversation existed", got)
	}
	if !strings.Contains(logs.String(), "d1") || strings.Contains(logs.String(), "SECRET-TEXT") {
		t.Errorf("log = %q, want the conversation id and no text", logs.String())
	}
	n.consider([]model.Event{unitEvent}, nil)
	n.consider([]model.Event{unitEvent}, nil)
	if got := rec.osCalls(); len(got) != 1 || got[0].ConvID != "d1" {
		t.Fatalf("notifications = %+v, want exactly one", got)
	}
}

func TestConcurrentConsiderationsNotifyOnce(t *testing.T) {
	n, _, rec, _, _ := unitNotifications(t, osOnly)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n.consider([]model.Event{unitEvent}, nil)
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
			fresh := n.unseen([]model.Event{unitEvent})
			n.consider(fresh, nil)
			if st.reads() != 0 || len(fresh) != 0 {
				t.Errorf("%d notification reads, %d candidates; want none", st.reads(), len(fresh))
			}
			if len(rec.osCalls())+len(rec.hookCalls())+len(srv.sent) != 0 {
				t.Errorf("something was raised")
			}
		})
	}
}

func TestEachNotifiedMessageIsReadOnce(t *testing.T) {
	n, st, rec, _, _ := unitNotifications(t, osOnly)
	fresh := n.unseen([]model.Event{unitEvent})
	n.consider(fresh, nil)
	if len(rec.osCalls()) != 1 {
		t.Fatalf("notifications = %+v", rec.osCalls())
	}
	if st.byMsg.Load() != 1 || st.view.Load() != 1 {
		t.Errorf("reads: %d message, %d conversation; want 1 and 1", st.byMsg.Load(), st.view.Load())
	}
}

func TestHeldEventsAreNotCandidates(t *testing.T) {
	n, st, _, _, _ := unitNotifications(t, osOnly)
	st.held[3] = true
	if got := n.unseen([]model.Event{unitEvent}); len(got) != 0 {
		t.Fatalf("already stored event is a candidate: %+v", got)
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
