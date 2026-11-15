package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/ipc"
	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/store"
)

// fakeSource is a waitSource whose first subscription ends in an overflow.
type fakeSource struct {
	subscriptions int
	stored        []store.Message
	lastSeq       int64
}

func (f *fakeSource) Subscribe(context.Context) (<-chan ipc.Event, error) {
	f.subscriptions++
	ch := make(chan ipc.Event)
	if f.subscriptions == 1 {
		close(ch)
	}
	return ch, nil
}

// SubscriptionErr is asked when the first subscription has ended. Events
// were lost: the message that arrived meanwhile is only in the store.
func (f *fakeSource) SubscriptionErr() error {
	f.stored = append(f.stored, store.Message{ConvID: "c1", MsgID: "m9", Author: "dana", Seq: 9, Text: "missed"})
	f.lastSeq = 9
	return ipc.ErrSubscriptionOverflow
}

func (f *fakeSource) Messages(conv string, before int64, limit int) ([]store.Message, error) {
	return f.stored, nil
}

func (f *fakeSource) Chats() ([]store.ConvView, []model.Conversation, error) {
	return []store.ConvView{{Conversation: model.Conversation{ID: "c1", LastSeq: f.lastSeq}}}, nil, nil
}

func TestWaitResubscribesAndRechecksAfterOverflow(t *testing.T) {
	f := &fakeSource{lastSeq: 5, stored: []store.Message{{ConvID: "c1", MsgID: "m5", Author: "dana", Seq: 5, Text: "old"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m, err := waitNext(ctx, f, waitFilter{me: "me"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Text != "missed" || f.subscriptions != 2 {
		t.Fatalf("waitNext = %q after %d subscriptions, want the missed message after a resubscribe", m.Text, f.subscriptions)
	}
}

func TestWaitGivesUpOnOtherSubscriptionEnds(t *testing.T) {
	_, err := waitNext(context.Background(), &closedSource{fakeSource: &fakeSource{}}, waitFilter{me: "me"}, nil)
	if !errors.Is(err, ipc.ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
}

// closedSource's subscription ends because the connection closed.
type closedSource struct{ *fakeSource }

func (c *closedSource) Subscribe(context.Context) (<-chan ipc.Event, error) {
	ch := make(chan ipc.Event)
	close(ch)
	return ch, nil
}

func (c *closedSource) SubscriptionErr() error { return ipc.ErrClosed }

// labelBackend serves one stored message and fails every Chats call after the first.
type labelBackend struct {
	ipc.Backend
	chats atomic.Int32
}

func (b *labelBackend) Status(context.Context) (ipc.Status, error) {
	return ipc.Status{Account: "me", Me: model.Person{Handle: "me"}}, nil
}

func (b *labelBackend) Resolve(context.Context, string) (model.Conversation, error) {
	return model.Conversation{ID: "c1", Kind: model.ConvRoom, Name: "lobby"}, nil
}

func (b *labelBackend) Chats(context.Context) (ipc.Chats, error) {
	if b.chats.Add(1) > 1 {
		return ipc.Chats{}, errors.New("the server is gone")
	}
	return ipc.Chats{Mine: []store.ConvView{{Conversation: model.Conversation{ID: "c1", Kind: model.ConvRoom, Name: "lobby", LastSeq: 7}}}}, nil
}

func (b *labelBackend) Messages(context.Context, string, int64, int) ([]store.Message, error) {
	return []store.Message{{ConvID: "c1", MsgID: "m7", Author: "dana", Seq: 7, At: time.Unix(0, 0), Text: "the news"}}, nil
}

func TestWaitPrintsItsMessageWhenTheLabelLookupFails(t *testing.T) {
	b := &labelBackend{}
	serveStub(t, b)
	old := isTerminal
	isTerminal = func(io.Writer) bool { return true }
	defer func() { isTerminal = old }()
	root := Root()
	var out, errOut syncBuf
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"wait", "#lobby", "--since", "0", "--timeout", "5s"})
	if err := root.Execute(); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !strings.Contains(out.String(), "c1  7  ") || !strings.Contains(out.String(), "the news") {
		t.Fatalf("stdout = %q, want the message labelled with the conversation id", out.String())
	}
	if b.chats.Load() < 2 {
		t.Fatalf("Chats called %d times; the label lookup never ran", b.chats.Load())
	}
}
