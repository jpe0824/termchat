package fake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jpe0824/termchat/internal/connector"
	"github.com/jpe0824/termchat/internal/connector/fake"
	"github.com/jpe0824/termchat/internal/model"
)

var _ connector.Connector = (*fake.Fake)(nil)

func text(s string) model.Body { return model.Body{Format: "text", Data: []byte(s)} }

func newFake() *fake.Fake {
	f := fake.New(model.Person{Handle: "me"})
	f.AddPeople(model.Person{Handle: "ann"})
	f.AddConversation(model.Conversation{ID: "c1", Kind: model.ConvRoom, Name: "general", Members: []string{"me", "ann"}})
	return f
}

func TestSendAssignsDenseSeqAndIsIdempotent(t *testing.T) {
	f := newFake()
	ctx := context.Background()
	e1, err := f.Send(ctx, "c1", "m1", text("a"), nil)
	if err != nil || e1.Seq != 1 || e1.Actor != "me" || e1.Kind != model.MessagePosted {
		t.Fatalf("first send = %+v, %v", e1, err)
	}
	e2, err := f.Send(ctx, "c1", "m2", text("b"), nil)
	if err != nil || e2.Seq != 2 {
		t.Fatalf("second send = %+v, %v", e2, err)
	}
	dup, err := f.Send(ctx, "c1", "m1", text("changed"), nil)
	if err != nil || dup.Seq != 1 || string(dup.Body.Data) != "a" {
		t.Fatalf("duplicate send = %+v, %v; want the original event", dup, err)
	}
	if got := len(f.Sent()); got != 2 {
		t.Fatalf("Sent() has %d events, want 2 (a duplicate is not a new send)", got)
	}
}

func TestSendUnknownConversation(t *testing.T) {
	_, err := newFake().Send(context.Background(), "nope", "m", text("x"), nil)
	if !errors.Is(err, connector.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestFailNextSendFailsOnce(t *testing.T) {
	f := newFake()
	boom := errors.New("boom")
	f.FailNextSend(boom)
	if _, err := f.Send(context.Background(), "c1", "m1", text("x"), nil); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if len(f.Sent()) != 0 {
		t.Fatal("a failed send was recorded")
	}
	if _, err := f.Send(context.Background(), "c1", "m1", text("x"), nil); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

func TestOfflineMethodsReturnErrOffline(t *testing.T) {
	f := newFake()
	f.SetState(connector.StateOffline)
	ctx := context.Background()
	if _, err := f.Send(ctx, "c1", "m", text("x"), nil); !errors.Is(err, connector.ErrOffline) {
		t.Fatalf("Send err = %v", err)
	}
	if _, err := f.People(ctx); !errors.Is(err, connector.ErrOffline) {
		t.Fatalf("People err = %v", err)
	}
	if _, _, err := f.Conversations(ctx); !errors.Is(err, connector.ErrOffline) {
		t.Fatalf("Conversations err = %v", err)
	}
	if err := f.MarkRead(ctx, "c1", 1); !errors.Is(err, connector.ErrOffline) {
		t.Fatalf("MarkRead err = %v", err)
	}
}

func TestSetStateAnnouncesOnUpdates(t *testing.T) {
	f := newFake()
	f.SetState(connector.StateOffline)
	u := <-f.Updates()
	if u.Event != nil || u.Read != nil || u.Conv != nil {
		t.Fatalf("state update carries payload: %+v", u)
	}
	if f.State() != connector.StateOffline {
		t.Fatalf("State() = %s", f.State())
	}
}

func TestPushStoresAndDelivers(t *testing.T) {
	f := newFake()
	f.Push(model.Event{ConvID: "c1", Kind: model.MessagePosted, Actor: "ann", MsgID: "x", Body: &model.Body{Format: "text", Data: []byte("hi")}})
	u := <-f.Updates()
	if u.Event == nil || u.Event.Seq != 1 || u.Event.Actor != "ann" {
		t.Fatalf("update = %+v", u)
	}
	mine, _, err := f.Conversations(context.Background())
	if err != nil || len(mine) != 1 || mine[0].LastSeq != 1 {
		t.Fatalf("conversations = %+v, %v", mine, err)
	}
	evs, err := f.History(context.Background(), "c1", 0, 10)
	if err != nil || len(evs) != 1 {
		t.Fatalf("history = %+v, %v", evs, err)
	}
}

func TestEditAndDelete(t *testing.T) {
	f := newFake()
	ctx := context.Background()
	f.Send(ctx, "c1", "m1", text("a"), nil)
	ed, err := f.Edit(ctx, "c1", "m1", text("b"), nil)
	if err != nil || ed.Kind != model.MessageEdited || ed.Seq != 2 || string(ed.Body.Data) != "b" {
		t.Fatalf("edit = %+v, %v", ed, err)
	}
	del, err := f.Delete(ctx, "c1", "m1")
	if err != nil || del.Kind != model.MessageDeleted || del.Seq != 3 {
		t.Fatalf("delete = %+v, %v", del, err)
	}
	if _, err := f.Edit(ctx, "c1", "missing", text("x"), nil); !errors.Is(err, connector.ErrNotFound) {
		t.Fatalf("edit missing err = %v", err)
	}
	f.Push(model.Event{ConvID: "c1", Kind: model.MessagePosted, Actor: "ann", MsgID: "a1", Body: &model.Body{}})
	<-f.Updates()
	if _, err := f.Edit(ctx, "c1", "a1", text("x"), nil); !errors.Is(err, connector.ErrForbidden) {
		t.Fatalf("edit other's message err = %v, want ErrForbidden", err)
	}
}

func TestHistoryPages(t *testing.T) {
	f := newFake()
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		f.Send(ctx, "c1", id, text(id), nil)
	}
	f.SetPageSize(2)
	evs, err := f.History(ctx, "c1", 0, 10)
	if err != nil || len(evs) != 2 || evs[0].Seq != 4 || evs[1].Seq != 5 {
		t.Fatalf("newest page = %+v, %v", evs, err)
	}
	evs, err = f.History(ctx, "c1", 4, 10)
	if err != nil || len(evs) != 2 || evs[0].Seq != 2 || evs[1].Seq != 3 {
		t.Fatalf("older page = %+v, %v", evs, err)
	}
}

func TestSyncPagesAndReportsGone(t *testing.T) {
	f := newFake()
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		f.Send(ctx, "c1", id, text(id), nil)
	}
	f.SetPageSize(2)
	evs, more, gone, err := f.Sync(ctx, map[string]int64{"c1": 0, "left": 5})
	if err != nil || len(evs) != 2 || !more || len(gone) != 1 || gone[0] != "left" {
		t.Fatalf("first sync = %+v more=%v gone=%v err=%v", evs, more, gone, err)
	}
	evs, more, _, err = f.Sync(ctx, map[string]int64{"c1": 2})
	if err != nil || len(evs) != 1 || evs[0].Seq != 3 || more {
		t.Fatalf("second sync = %+v more=%v err=%v", evs, more, err)
	}
}

func TestMarkReadNeverMovesBackwards(t *testing.T) {
	f := newFake()
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		f.Send(ctx, "c1", id, text(id), nil)
	}
	f.MarkRead(ctx, "c1", 5)
	f.MarkRead(ctx, "c1", 3)
	mine, _, _ := f.Conversations(ctx)
	if mine[0].ReadSeq != 5 {
		t.Fatalf("ReadSeq = %d, want 5", mine[0].ReadSeq)
	}
}

func TestRoomOperations(t *testing.T) {
	f := newFake()
	ctx := context.Background()
	c, err := f.Room(ctx, connector.RoomOp{Kind: connector.RoomCreate, Name: "ops", Open: true})
	if err != nil || c.Name != "ops" || !c.Open || c.Creator != "me" || len(c.Members) != 1 {
		t.Fatalf("create = %+v, %v", c, err)
	}
	c, err = f.Room(ctx, connector.RoomOp{Kind: connector.RoomInvite, Conv: c.ID, Handle: "ann"})
	if err != nil || len(c.Members) != 2 {
		t.Fatalf("invite = %+v, %v", c, err)
	}
	c, err = f.Room(ctx, connector.RoomOp{Kind: connector.RoomRename, Conv: c.ID, Name: "ops2"})
	if err != nil || c.Name != "ops2" {
		t.Fatalf("rename = %+v, %v", c, err)
	}
	c, err = f.Room(ctx, connector.RoomOp{Kind: connector.RoomRemove, Conv: c.ID, Handle: "ann"})
	if err != nil || len(c.Members) != 1 {
		t.Fatalf("remove = %+v, %v", c, err)
	}
	if _, err = f.Room(ctx, connector.RoomOp{Kind: connector.RoomLeave, Conv: c.ID}); err != nil {
		t.Fatalf("leave: %v", err)
	}
	mine, open, _ := f.Conversations(ctx)
	if len(mine) != 1 || len(open) != 1 || open[0].ID != c.ID {
		t.Fatalf("after leave mine=%+v open=%+v; the open room should be listed as joinable", mine, open)
	}
	if _, err := f.Room(ctx, connector.RoomOp{Kind: connector.RoomJoin, Conv: c.ID}); err != nil {
		t.Fatalf("join: %v", err)
	}
	if _, err := f.Room(ctx, connector.RoomOp{Kind: "bogus"}); !errors.Is(err, connector.ErrInvalid) {
		t.Fatalf("bogus err = %v", err)
	}
}

func TestOpenDMReusesConversation(t *testing.T) {
	f := newFake()
	a, err := f.OpenDM(context.Background(), "ann")
	if err != nil || a.Kind != model.ConvDM {
		t.Fatalf("OpenDM = %+v, %v", a, err)
	}
	b, _ := f.OpenDM(context.Background(), "ann")
	if a.ID != b.ID {
		t.Fatalf("second OpenDM gave %s, want %s", b.ID, a.ID)
	}
}

func TestInvitesRespectRole(t *testing.T) {
	f := newFake()
	ctx := context.Background()
	if _, err := f.AdminInvite(ctx, "bob", "Bob"); !errors.Is(err, connector.ErrForbidden) {
		t.Fatalf("AdminInvite by non-admin err = %v", err)
	}
	if code, err := f.DeviceInvite(ctx); err != nil || code == "" {
		t.Fatalf("DeviceInvite = %q, %v", code, err)
	}
	if code, err := f.CreateAgent(ctx, "bot", "Bot"); err != nil || code == "" {
		t.Fatalf("CreateAgent = %q, %v", code, err)
	}
	admin := fake.New(model.Person{Handle: "root", Admin: true})
	if code, err := admin.AdminInvite(ctx, "bob", "Bob"); err != nil || code == "" {
		t.Fatalf("AdminInvite by admin = %q, %v", code, err)
	}
}

func TestRunBlocksUntilContextEnds(t *testing.T) {
	f := newFake()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()
	select {
	case err := <-done:
		t.Fatalf("Run returned early: %v", err)
	default:
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}
