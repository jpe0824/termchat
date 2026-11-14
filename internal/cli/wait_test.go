package cli

import (
	"context"
	"errors"
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
