package server_test

import (
	"testing"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
	"github.com/jpe0824/termchat/internal/testutil"
)

// roomWith creates a private room owned by owner and invites the others,
// draining the pushes the invitations cause on the supplied devices.
func roomWith(owner *device, name string, others ...*device) model.Conversation {
	owner.t.Helper()
	conv := owner.room(name, false)
	for i, o := range others {
		owner.invite(conv.ID, o.handle)
		o.convPush()
		o.eventPush()
		for _, earlier := range others[:i] {
			earlier.eventPush()
		}
	}
	return conv
}

func TestSendDeliversToOtherMemberLive(t *testing.T) {
	ch := newChat(t, "alice", "bob")
	a, b := ch.dial("alice"), ch.dial("bob")
	conv := roomWith(a, "general", b)

	sent := a.say(conv.ID, "hi bob")
	got := b.eventPush()
	if got.Kind != model.MessagePosted || got.Actor != "alice" || string(got.Body.Data) != "hi bob" || got.Seq != sent.Seq || got.ConvID != conv.ID {
		t.Fatalf("pushed %+v, sent %+v", got, sent)
	}
	a.quiet()
}

func TestSendDeliversToSendersOtherDevice(t *testing.T) {
	ch := newChat(t, "alice")
	a1, a2 := ch.dial("alice"), ch.dial("alice")
	conv := a1.room("notes", false)
	if got := a2.convPush(); got.ID != conv.ID {
		t.Fatalf("conv.updated for %q, want %q", got.ID, conv.ID)
	}

	sent := a1.say(conv.ID, "from laptop")
	got := a2.eventPush()
	if got.Seq != sent.Seq || string(got.Body.Data) != "from laptop" {
		t.Fatalf("other device got %+v, sent %+v", got, sent)
	}
	a1.quiet()
}

func TestSendTwiceSameMsgIDPostsOnce(t *testing.T) {
	ch := newChat(t, "alice", "bob")
	a, b := ch.dial("alice"), ch.dial("bob")
	conv := roomWith(a, "general", b)

	req := proto.MsgSendReq{Conv: conv.ID, MsgID: "same-id", Body: text("once")}
	var first, second proto.MsgSendRes
	a.do(proto.TypeMsgSend, req, &first)
	a.do(proto.TypeMsgSend, req, &second)
	if first.Duplicate || !second.Duplicate || first.Event.Seq != second.Event.Seq {
		t.Fatalf("first %+v second %+v", first, second)
	}
	if got := b.eventPush(); got.Seq != first.Event.Seq {
		t.Fatalf("pushed seq %d, want %d", got.Seq, first.Event.Seq)
	}
	b.quiet()
}

func TestSyncReturnsMissedEvents(t *testing.T) {
	ch := newChat(t, "alice", "bob")
	a, b := ch.dial("alice"), ch.dial("bob")
	conv := roomWith(a, "general", b)
	cursor := a.say(conv.ID, "before").Seq

	for _, s := range []string{"one", "two", "three"} {
		a.say(conv.ID, s)
	}
	b2 := ch.dial("bob")
	res := b2.sync(map[string]int64{conv.ID: cursor})
	if len(res.Events) != 3 || res.More || len(res.Gone) != 0 {
		t.Fatalf("sync = %+v", res)
	}
	for i, ev := range res.Events {
		if ev.Seq != cursor+int64(i)+1 {
			t.Fatalf("event %d has seq %d, cursor %d", i, ev.Seq, cursor)
		}
	}
}

func TestNonMemberCannotReadPrivateRoom(t *testing.T) {
	ch := newChat(t, "alice", "carol")
	a, c := ch.dial("alice"), ch.dial("carol")
	conv := a.room("secret", false)
	a.say(conv.ID, "classified")

	c.refuse(proto.TypeConvHistory, proto.ConvHistoryReq{Conv: conv.ID}, proto.CodeNotFound)
	res := c.sync(map[string]int64{conv.ID: 0})
	if len(res.Events) != 0 || len(res.Gone) != 1 || res.Gone[0] != conv.ID {
		t.Fatalf("sync for non-member = %+v", res)
	}
}

func TestNewMemberSeesFullHistory(t *testing.T) {
	ch := newChat(t, "alice", "bob")
	a, b := ch.dial("alice"), ch.dial("bob")
	conv := a.room("general", false)
	for _, s := range []string{"one", "two", "three"} {
		a.say(conv.ID, s)
	}
	a.invite(conv.ID, "bob")

	evs := b.history(conv.ID)
	want := []int64{1, 2, 3, 4, 5}
	got := seqs(evs)
	if len(got) != len(want) {
		t.Fatalf("seqs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("seqs = %v, want %v", got, want)
		}
	}
	if string(evs[1].Body.Data) != "one" {
		t.Fatalf("first message body %q", evs[1].Body.Data)
	}
}

func TestRemovedMemberGetsNothingFurther(t *testing.T) {
	ch := newChat(t, "alice", "bob")
	a, b := ch.dial("alice"), ch.dial("bob")
	conv := roomWith(a, "general", b)

	a.do(proto.TypeRoomRemove, proto.RoomRemoveReq{Conv: conv.ID, Handle: "bob"}, nil)
	left := b.eventPush()
	if left.Kind != model.MemberLeft || left.Target != "bob" || left.Actor != "alice" {
		t.Fatalf("last push = %+v", left)
	}
	a.say(conv.ID, "after removal")

	b.refuse(proto.TypeConvHistory, proto.ConvHistoryReq{Conv: conv.ID}, proto.CodeNotFound)
	res := b.sync(map[string]int64{conv.ID: left.Seq})
	if len(res.Events) != 0 || len(res.Gone) != 1 || res.Gone[0] != conv.ID {
		t.Fatalf("sync after removal = %+v", res)
	}
	b.quiet()
}

func TestOnlyCreatorOrAdminManagesRoom(t *testing.T) {
	ch := newChat(t, "alice", "bob", "carol", "dave")
	if err := ch.h.Store.SetAdmin("dave", true); err != nil {
		t.Fatal(err)
	}
	a, b, c, d := ch.dial("alice"), ch.dial("bob"), ch.dial("carol"), ch.dial("dave")
	conv := roomWith(a, "general", b)

	b.refuse(proto.TypeRoomInvite, proto.RoomInviteReq{Conv: conv.ID, Handle: "carol"}, proto.CodeForbidden)
	b.refuse(proto.TypeRoomRename, proto.RoomRenameReq{Conv: conv.ID, Name: "mine"}, proto.CodeForbidden)
	b.refuse(proto.TypeRoomRemove, proto.RoomRemoveReq{Conv: conv.ID, Handle: "alice"}, proto.CodeForbidden)
	c.refuse(proto.TypeRoomInvite, proto.RoomInviteReq{Conv: conv.ID, Handle: "carol"}, proto.CodeNotFound)

	var inv proto.RoomInviteRes
	a.do(proto.TypeRoomInvite, proto.RoomInviteReq{Conv: conv.ID, Handle: "carol"}, &inv)
	if len(inv.Conv.Members) != 3 {
		t.Fatalf("members after invite = %v", inv.Conv.Members)
	}
	var ren proto.RoomRenameRes
	d.do(proto.TypeRoomRename, proto.RoomRenameReq{Conv: conv.ID, Name: "lounge"}, &ren)
	if ren.Conv.Name != "lounge" {
		t.Fatalf("renamed to %q", ren.Conv.Name)
	}
	d.do(proto.TypeRoomRemove, proto.RoomRemoveReq{Conv: conv.ID, Handle: "carol"}, nil)
	if ok, _ := ch.h.Store.IsMember(conv.ID, "carol"); ok {
		t.Fatal("carol still a member")
	}
}

func TestReadSyncsAcrossDevices(t *testing.T) {
	ch := newChat(t, "alice")
	a1, a2 := ch.dial("alice"), ch.dial("alice")
	conv := a1.room("notes", false)
	a2.convPush()
	last := a1.say(conv.ID, "one").Seq
	a2.eventPush()

	a1.do(proto.TypeReadSet, proto.ReadSetReq{Conv: conv.ID, Seq: last}, nil)
	var p proto.ReadPush
	unmarshal(t, a2.push(proto.TypeRead).Payload, &p)
	if p.Conv != conv.ID || p.Seq != last {
		t.Fatalf("read push = %+v", p)
	}
	a1.quiet()
}

func TestEditAndDeletePushEvents(t *testing.T) {
	ch := newChat(t, "alice", "bob")
	a, b := ch.dial("alice"), ch.dial("bob")
	conv := roomWith(a, "general", b)
	posted := a.say(conv.ID, "draft")
	b.eventPush()
	b.refuse(proto.TypeMsgEdit, proto.MsgEditReq{Conv: conv.ID, MsgID: posted.MsgID, Body: text("x")}, proto.CodeForbidden)
	b.refuse(proto.TypeMsgDelete, proto.MsgDeleteReq{Conv: conv.ID, MsgID: posted.MsgID}, proto.CodeForbidden)

	a.do(proto.TypeMsgEdit, proto.MsgEditReq{Conv: conv.ID, MsgID: posted.MsgID, Body: text("final")}, nil)
	ed := b.eventPush()
	if ed.Kind != model.MessageEdited || ed.MsgID != posted.MsgID || string(ed.Body.Data) != "final" {
		t.Fatalf("edit push = %+v", ed)
	}
	a.do(proto.TypeMsgDelete, proto.MsgDeleteReq{Conv: conv.ID, MsgID: posted.MsgID}, nil)
	del := b.eventPush()
	if del.Kind != model.MessageDeleted || del.MsgID != posted.MsgID || del.Body != nil {
		t.Fatalf("delete push = %+v", del)
	}
	b.refuse(proto.TypeMsgEdit, proto.MsgEditReq{Conv: conv.ID, MsgID: posted.MsgID, Body: text("x")}, proto.CodeNotFound)
}

func TestMentionsPassThroughUnparsed(t *testing.T) {
	ch := newChat(t, "alice", "bob")
	a, b := ch.dial("alice"), ch.dial("bob")
	conv := roomWith(a, "general", b)

	if ev := a.say(conv.ID, "@bob look"); len(ev.Mentions) != 0 {
		t.Fatalf("response mentions %v", ev.Mentions)
	}
	if ev := b.eventPush(); len(ev.Mentions) != 0 {
		t.Fatalf("pushed mentions %v", ev.Mentions)
	}
	var res proto.MsgSendRes
	a.do(proto.TypeMsgSend, proto.MsgSendReq{Conv: conv.ID, MsgID: newMsgID(), Body: text("hi"), Mentions: []string{"bob"}}, &res)
	if got := b.eventPush(); len(got.Mentions) != 1 || got.Mentions[0] != "bob" {
		t.Fatalf("pushed mentions %v", got.Mentions)
	}
}

func redeemAs(t *testing.T, ch *chat, code string) (*device, model.Person) {
	t.Helper()
	key := testutil.NewKey(t)
	c := ch.h.Dial(t, key)
	testutil.Hello(t, c)
	f := testutil.Request(t, c, proto.TypeInviteRedeem, proto.InviteRedeemReq{Code: code})
	if f.Error != nil {
		t.Fatalf("redeem: %v", f.Error)
	}
	var res proto.InviteRedeemRes
	unmarshal(t, f.Payload, &res)
	return &device{t: t, c: c, handle: res.Person.Handle}, res.Person
}

func TestAgentAccountLabelledAndRestricted(t *testing.T) {
	ch := newChat(t, "alice")
	a := ch.dial("alice")
	var made proto.AgentCreateRes
	a.do(proto.TypeAgentCreate, proto.AgentCreateReq{Handle: "bot", DisplayName: "Bot"}, &made)

	bot, person := redeemAs(t, ch, made.Code)
	if !person.Agent || person.Owner != "alice" || person.Handle != "bot" {
		t.Fatalf("agent person = %+v", person)
	}
	bot.refuse(proto.TypeAgentCreate, proto.AgentCreateReq{Handle: "bot2"}, proto.CodeForbidden)
	bot.refuse(proto.TypeDeviceInvite, nil, proto.CodeForbidden)
}

func TestDeviceInviteAddsSecondKey(t *testing.T) {
	ch := newChat(t, "alice")
	a := ch.dial("alice")
	var inv proto.DeviceInviteRes
	a.do(proto.TypeDeviceInvite, nil, &inv)

	_, person := redeemAs(t, ch, inv.Code)
	if person.Handle != "alice" {
		t.Fatalf("second device joined as %q", person.Handle)
	}
	keys, err := ch.h.Store.Keys("alice")
	if err != nil || len(keys) != 2 {
		t.Fatalf("keys = %v, %v", keys, err)
	}
}
