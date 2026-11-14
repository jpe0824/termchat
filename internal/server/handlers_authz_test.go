package server_test

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/model"
	"github.com/jpe0824/termchat/internal/proto"
	"github.com/jpe0824/termchat/internal/server"
	"github.com/jpe0824/termchat/internal/testutil"
)

func TestMalformedPayloadIsInvalid(t *testing.T) {
	ch := newChat(t, "alice")
	a := ch.dial("alice")
	a.refuse(proto.TypeRoomJoin, "not an object", proto.CodeInvalid)
	a.refuse(proto.TypeConvHistory, []int{1}, proto.CodeInvalid)
}

func TestConvListSeparatesOpenRooms(t *testing.T) {
	ch := newChat(t, "alice", "bob", "carol")
	a, b, c := ch.dial("alice"), ch.dial("bob"), ch.dial("carol")
	lobby := b.room("lobby", true)
	private := a.room("plans", false)

	var res proto.ConvListRes
	c.do(proto.TypeConvList, nil, &res)
	if len(res.Mine) != 0 || len(res.OpenRooms) != 1 || res.OpenRooms[0].ID != lobby.ID {
		t.Fatalf("carol sees %+v", res)
	}
	a.do(proto.TypeRoomJoin, proto.RoomJoinReq{Conv: lobby.ID}, nil)
	a.do(proto.TypeConvList, nil, &res)
	ids := map[string]bool{}
	for _, cv := range res.Mine {
		ids[cv.ID] = true
	}
	if len(res.Mine) != 2 || !ids[lobby.ID] || !ids[private.ID] || len(res.OpenRooms) != 0 {
		t.Fatalf("alice sees %+v", res)
	}
}

func TestPeopleListReturnsEveryone(t *testing.T) {
	ch := newChat(t, "alice", "bob", "carol")
	var res proto.PeopleListRes
	ch.dial("bob").do(proto.TypePeopleList, nil, &res)
	if len(res.People) != 3 {
		t.Fatalf("people = %+v", res.People)
	}
}

func TestHistoryLimitsAndOrder(t *testing.T) {
	ch := newChat(t, "alice")
	a := ch.dial("alice")
	conv := a.room("long", false)
	for i := 0; i < 250; i++ {
		if _, _, err := ch.h.Store.PostMessage(conv.ID, "alice", newMsgID(), text("x"), nil); err != nil {
			t.Fatal(err)
		}
	}
	check := func(req proto.ConvHistoryReq, wantLen int, wantLast int64) {
		t.Helper()
		var res proto.ConvHistoryRes
		a.do(proto.TypeConvHistory, req, &res)
		if len(res.Events) != wantLen || res.Events[len(res.Events)-1].Seq != wantLast {
			t.Fatalf("%+v: %d events ending at %d, want %d ending at %d", req, len(res.Events), res.Events[len(res.Events)-1].Seq, wantLen, wantLast)
		}
		for i := 1; i < len(res.Events); i++ {
			if res.Events[i].Seq != res.Events[i-1].Seq+1 {
				t.Fatalf("not ascending: %v", seqs(res.Events))
			}
		}
	}
	check(proto.ConvHistoryReq{Conv: conv.ID}, 50, 251)
	check(proto.ConvHistoryReq{Conv: conv.ID, Limit: -3}, 50, 251)
	check(proto.ConvHistoryReq{Conv: conv.ID, Limit: 5}, 5, 251)
	check(proto.ConvHistoryReq{Conv: conv.ID, Limit: 100000}, 200, 251)
	check(proto.ConvHistoryReq{Conv: conv.ID, BeforeSeq: 100, Limit: 10}, 10, 99)
}

func fillBig(t *testing.T, ch *chat, conv string, n int) {
	t.Helper()
	big := model.Body{Format: "text/plain", Data: []byte(strings.Repeat("a", 8192))}
	for i := 0; i < n; i++ {
		if _, _, err := ch.h.Store.PostMessage(conv, "alice", newMsgID(), big, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHistoryByteBudget(t *testing.T) {
	ch := newChat(t, "alice")
	a := ch.dial("alice")
	conv := a.room("big", false)
	fillBig(t, ch, conv.ID, 100)

	f := a.req(proto.TypeConvHistory, proto.ConvHistoryReq{Conv: conv.ID, Limit: 200})
	if f.Error != nil {
		t.Fatal(f.Error)
	}
	var res proto.ConvHistoryRes
	unmarshal(t, f.Payload, &res)
	if len(f.Payload) > 512<<10+16<<10 {
		t.Fatalf("payload %d bytes, budget 512 KiB", len(f.Payload))
	}
	if len(res.Events) < 20 || len(res.Events) >= 100 {
		t.Fatalf("%d events returned", len(res.Events))
	}
	if res.Events[len(res.Events)-1].Seq != 101 {
		t.Fatalf("newest event missing: %v", seqs(res.Events))
	}
}

func TestHistoryReturnsOneEventPastBudget(t *testing.T) {
	ch := newChat(t, "alice")
	a := ch.dial("alice")
	conv := a.room("huge", false)
	huge := model.Body{Format: "text/plain", Data: []byte(strings.Repeat("a", 600<<10))}
	if _, _, err := ch.h.Store.PostMessage(conv.ID, "alice", "huge-1", huge, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ch.h.Store.PostMessage(conv.ID, "alice", "huge-2", huge, nil); err != nil {
		t.Fatal(err)
	}
	evs := a.history(conv.ID)
	if len(evs) != 1 || evs[0].MsgID != "huge-2" {
		t.Fatalf("got %d events: %v", len(evs), seqs(evs))
	}
}

func TestSyncMoreResumesWithoutLossOrDuplicates(t *testing.T) {
	ch := newChat(t, "alice")
	a := ch.dial("alice")
	conv := a.room("big", false)
	fillBig(t, ch, conv.ID, 100)

	var got []int64
	cursor := int64(0)
	calls := 0
	for {
		calls++
		res := a.sync(map[string]int64{conv.ID: cursor})
		for _, ev := range res.Events {
			got = append(got, ev.Seq)
			cursor = ev.Seq
		}
		if !res.More {
			break
		}
		if len(res.Events) == 0 {
			t.Fatal("More set with no events")
		}
	}
	if calls < 2 {
		t.Fatal("budget never held anything back")
	}
	if len(got) != 101 {
		t.Fatalf("got %d events", len(got))
	}
	for i, s := range got {
		if s != int64(i+1) {
			t.Fatalf("seq %d at position %d", s, i)
		}
	}
}

func TestSyncMoreSpansConversations(t *testing.T) {
	ch := newChat(t, "alice")
	a := ch.dial("alice")
	c1, c2 := a.room("one", false), a.room("two", false)
	fillBig(t, ch, c1.ID, 60)
	fillBig(t, ch, c2.ID, 60)
	res := a.sync(map[string]int64{c1.ID: 0, c2.ID: 0})
	if !res.More {
		t.Fatal("expected More")
	}
	for i := 1; i < len(res.Events); i++ {
		a, b := res.Events[i-1], res.Events[i]
		if a.ConvID == b.ConvID && b.Seq != a.Seq+1 {
			t.Fatalf("gap inside conversation: %d then %d", a.Seq, b.Seq)
		}
	}
}

func TestSyncGoneForLeftAndUnknownConversations(t *testing.T) {
	ch := newChat(t, "alice", "bob", "carol")
	a, b, c := ch.dial("alice"), ch.dial("bob"), ch.dial("carol")
	kept := roomWith(a, "kept", b)
	left := roomWith(a, "left", b)
	never := c.room("never", false)
	a.say(kept.ID, "still here")
	a.say(left.ID, "after leave")

	b.do(proto.TypeRoomLeave, proto.RoomLeaveReq{Conv: left.ID}, nil)
	res := b.sync(map[string]int64{kept.ID: 0, left.ID: 0, never.ID: 0, "r_doesnotexist": 0})
	if len(res.Gone) != 3 {
		t.Fatalf("gone = %v", res.Gone)
	}
	for _, ev := range res.Events {
		if ev.ConvID != kept.ID {
			t.Fatalf("event from %s leaked", ev.ConvID)
		}
	}
	if len(res.Events) != 3 {
		t.Fatalf("kept events = %v", seqs(res.Events))
	}
}

func TestMessageRequestsNeedMembership(t *testing.T) {
	ch := newChat(t, "alice", "carol")
	a, c := ch.dial("alice"), ch.dial("carol")
	conv := roomWith(a, "general", c)
	mine := c.say(conv.ID, "mine")
	a.do(proto.TypeRoomRemove, proto.RoomRemoveReq{Conv: conv.ID, Handle: "carol"}, nil)
	c.push(proto.TypeEvent)

	c.refuse(proto.TypeMsgSend, proto.MsgSendReq{Conv: conv.ID, MsgID: newMsgID(), Body: text("x")}, proto.CodeNotFound)
	c.refuse(proto.TypeMsgEdit, proto.MsgEditReq{Conv: conv.ID, MsgID: mine.MsgID, Body: text("x")}, proto.CodeNotFound)
	c.refuse(proto.TypeMsgDelete, proto.MsgDeleteReq{Conv: conv.ID, MsgID: mine.MsgID}, proto.CodeNotFound)
	c.refuse(proto.TypeReadSet, proto.ReadSetReq{Conv: conv.ID, Seq: 1}, proto.CodeNotFound)
	for _, ev := range a.history(conv.ID) {
		if ev.Kind == model.MessageEdited || ev.Kind == model.MessageDeleted || string(bodyData(ev)) == "x" {
			t.Fatalf("non-member write landed: %+v", ev)
		}
	}
}

func bodyData(ev model.Event) []byte {
	if ev.Body == nil {
		return nil
	}
	return ev.Body.Data
}

func TestSendValidatesMsgIDAndMentions(t *testing.T) {
	ch := newChat(t, "alice")
	a := ch.dial("alice")
	conv := a.room("general", false)
	send := func(id string, mentions []string) proto.Frame {
		return a.req(proto.TypeMsgSend, proto.MsgSendReq{Conv: conv.ID, MsgID: id, Body: text("x"), Mentions: mentions})
	}
	want := func(f proto.Frame, code string) {
		t.Helper()
		switch {
		case code == "" && f.Error != nil, code != "" && (f.Error == nil || f.Error.Code != code):
			t.Fatalf("got %+v, want code %q", f, code)
		}
	}
	many := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "bob"
		}
		return out
	}
	want(send("", nil), proto.CodeInvalid)
	want(send(strings.Repeat("i", 65), nil), proto.CodeInvalid)
	want(send(strings.Repeat("j", 64), nil), "")
	want(send(newMsgID(), many(51)), proto.CodeInvalid)
	want(send(newMsgID(), many(50)), "")
	want(send(newMsgID(), []string{"bob", "Not A Name!"}), proto.CodeInvalid)
	want(a.req(proto.TypeMsgEdit, proto.MsgEditReq{Conv: conv.ID, MsgID: "nope", Body: text("x"), Mentions: []string{"bad name"}}), proto.CodeInvalid)
}

func TestDuplicateMsgIDFromOtherActorIsForbidden(t *testing.T) {
	ch := newChat(t, "alice", "bob")
	a, b := ch.dial("alice"), ch.dial("bob")
	conv := roomWith(a, "general", b)
	orig := a.say(conv.ID, "alice's words")
	b.eventPush()

	f := b.req(proto.TypeMsgSend, proto.MsgSendReq{Conv: conv.ID, MsgID: orig.MsgID, Body: text("mine now")})
	if f.Error == nil || f.Error.Code != proto.CodeForbidden || len(f.Payload) != 0 {
		t.Fatalf("response = %+v", f)
	}
	a.quiet()
}

func TestReadSetClampsToLastSeq(t *testing.T) {
	ch := newChat(t, "alice", "bob")
	a1, a2, b := ch.dial("alice"), ch.dial("alice"), ch.dial("bob")
	conv := roomWith(a1, "general", b)
	a2.convPush()
	a2.eventPush()
	last := a1.say(conv.ID, "one").Seq
	a2.eventPush()
	b.eventPush()

	a1.do(proto.TypeReadSet, proto.ReadSetReq{Conv: conv.ID, Seq: 9999}, nil)
	var p proto.ReadPush
	unmarshal(t, a2.push(proto.TypeRead).Payload, &p)
	if p.Seq != last {
		t.Fatalf("pushed read seq %d, want %d", p.Seq, last)
	}
	var list proto.ConvListRes
	a1.do(proto.TypeConvList, nil, &list)
	if list.Mine[0].ReadSeq != last {
		t.Fatalf("stored read seq %d, want %d", list.Mine[0].ReadSeq, last)
	}
	b.quiet()
}

func TestDMOpen(t *testing.T) {
	ch := newChat(t, "alice", "bob")
	a, b := ch.dial("alice"), ch.dial("bob")
	var res proto.DMOpenRes
	a.do(proto.TypeDMOpen, proto.DMOpenReq{Handle: "bob"}, &res)
	if res.Conv.Kind != model.ConvDM || len(res.Conv.Members) != 2 {
		t.Fatalf("dm = %+v", res.Conv)
	}
	if got := b.convPush(); got.ID != res.Conv.ID {
		t.Fatalf("bob was told about %q, want %q", got.ID, res.Conv.ID)
	}
	a.quiet()
	a.refuse(proto.TypeDMOpen, proto.DMOpenReq{Handle: "alice"}, proto.CodeInvalid)
	a.refuse(proto.TypeDMOpen, proto.DMOpenReq{Handle: "nobody"}, proto.CodeNotFound)
	a.refuse(proto.TypeDMOpen, proto.DMOpenReq{Handle: "Bad Name"}, proto.CodeInvalid)
	a.say(res.Conv.ID, "hello")
	if got := b.eventPush(); string(got.Body.Data) != "hello" {
		t.Fatalf("dm push = %+v", got)
	}
}

func TestRoomCreateJoinLeave(t *testing.T) {
	ch := newChat(t, "alice", "bob", "carol")
	a, b, c := ch.dial("alice"), ch.dial("bob"), ch.dial("carol")
	lobby := a.room("lobby", true)
	secret := a.room("secret", false)
	a.refuse(proto.TypeRoomCreate, proto.RoomCreateReq{Name: "lobby"}, proto.CodeInvalid)
	a.refuse(proto.TypeRoomCreate, proto.RoomCreateReq{Name: "Bad Name"}, proto.CodeInvalid)

	var joined proto.RoomJoinRes
	b.do(proto.TypeRoomJoin, proto.RoomJoinReq{Conv: lobby.ID}, &joined)
	if len(joined.Conv.Members) != 2 {
		t.Fatalf("members = %v", joined.Conv.Members)
	}
	if ev := a.eventPush(); ev.Kind != model.MemberJoined || ev.Target != "bob" {
		t.Fatalf("creator saw %+v", ev)
	}
	b.refuse(proto.TypeRoomJoin, proto.RoomJoinReq{Conv: lobby.ID}, proto.CodeInvalid)
	c.refuse(proto.TypeRoomJoin, proto.RoomJoinReq{Conv: secret.ID}, proto.CodeNotFound)
	c.refuse(proto.TypeRoomJoin, proto.RoomJoinReq{Conv: "r_doesnotexist"}, proto.CodeNotFound)
	c.refuse(proto.TypeRoomLeave, proto.RoomLeaveReq{Conv: lobby.ID}, proto.CodeForbidden)

	b.do(proto.TypeRoomLeave, proto.RoomLeaveReq{Conv: lobby.ID}, nil)
	if ev := a.eventPush(); ev.Kind != model.MemberLeft || ev.Target != "bob" || ev.Actor != "bob" {
		t.Fatalf("creator saw %+v", ev)
	}
	b.refuse(proto.TypeConvHistory, proto.ConvHistoryReq{Conv: lobby.ID}, proto.CodeForbidden)
	b.refuse(proto.TypeRoomLeave, proto.RoomLeaveReq{Conv: lobby.ID}, proto.CodeForbidden)
}

func TestEveryRoomRequestOnDMIsInvalid(t *testing.T) {
	ch := newChat(t, "alice", "bob", "carol")
	if err := ch.h.Store.SetAdmin("alice", true); err != nil {
		t.Fatal(err)
	}
	a := ch.dial("alice")
	var dm proto.DMOpenRes
	a.do(proto.TypeDMOpen, proto.DMOpenReq{Handle: "bob"}, &dm)
	id := dm.Conv.ID
	a.refuse(proto.TypeRoomJoin, proto.RoomJoinReq{Conv: id}, proto.CodeInvalid)
	a.refuse(proto.TypeRoomLeave, proto.RoomLeaveReq{Conv: id}, proto.CodeInvalid)
	a.refuse(proto.TypeRoomInvite, proto.RoomInviteReq{Conv: id, Handle: "carol"}, proto.CodeInvalid)
	a.refuse(proto.TypeRoomRemove, proto.RoomRemoveReq{Conv: id, Handle: "bob"}, proto.CodeInvalid)
	a.refuse(proto.TypeRoomRemove, proto.RoomRemoveReq{Conv: id, Handle: "alice"}, proto.CodeInvalid)
	a.refuse(proto.TypeRoomRename, proto.RoomRenameReq{Conv: id, Name: "x"}, proto.CodeInvalid)
	if ok, _ := ch.h.Store.IsMember(id, "bob"); !ok {
		t.Fatal("bob lost his DM")
	}
}

func TestAnyMemberMayRemoveThemselves(t *testing.T) {
	ch := newChat(t, "alice", "bob")
	a, b := ch.dial("alice"), ch.dial("bob")
	conv := roomWith(a, "general", b)

	var res proto.RoomRemoveRes
	b.do(proto.TypeRoomRemove, proto.RoomRemoveReq{Conv: conv.ID, Handle: "bob"}, &res)
	if len(res.Conv.Members) != 0 {
		t.Fatalf("leaver was shown members %v", res.Conv.Members)
	}
	if ev := a.eventPush(); ev.Kind != model.MemberLeft || ev.Target != "bob" {
		t.Fatalf("creator saw %+v", ev)
	}
	b.refuse(proto.TypeRoomRemove, proto.RoomRemoveReq{Conv: conv.ID, Handle: "bob"}, proto.CodeNotFound)
}

func TestMembershipPushes(t *testing.T) {
	ch := newChat(t, "alice", "bob", "carol")
	a, b, c1, c2 := ch.dial("alice"), ch.dial("bob"), ch.dial("carol"), ch.dial("carol")
	conv := roomWith(a, "general", b)

	a.invite(conv.ID, "carol")
	for _, c := range []*device{c1, c2} {
		cv := c.convPush()
		if cv.ID != conv.ID || len(cv.Members) != 3 || cv.LastSeq == 0 {
			t.Fatalf("conv.updated = %+v", cv)
		}
		if ev := c.eventPush(); ev.Kind != model.MemberJoined || ev.Target != "carol" {
			t.Fatalf("carol saw %+v", ev)
		}
	}
	if ev := b.eventPush(); ev.Kind != model.MemberJoined || ev.Target != "carol" {
		t.Fatalf("bob saw %+v", ev)
	}
	a.quiet()

	a.do(proto.TypeRoomRemove, proto.RoomRemoveReq{Conv: conv.ID, Handle: "carol"}, nil)
	b.eventPush()
	for _, c := range []*device{c1, c2} {
		if ev := c.eventPush(); ev.Kind != model.MemberLeft || ev.Target != "carol" {
			t.Fatalf("carol saw %+v", ev)
		}
	}
	a.say(conv.ID, "carol must not see this")
	b.eventPush()
	c1.quiet()
	c2.quiet()
}

func TestLeaveReachesLeaversOtherDevice(t *testing.T) {
	ch := newChat(t, "alice", "bob")
	a, b1, b2 := ch.dial("alice"), ch.dial("bob"), ch.dial("bob")
	conv := roomWith(a, "general", b1)
	b2.convPush()
	b2.eventPush()

	b1.do(proto.TypeRoomLeave, proto.RoomLeaveReq{Conv: conv.ID}, nil)
	if ev := b2.eventPush(); ev.Kind != model.MemberLeft {
		t.Fatalf("other device saw %+v", ev)
	}
	a.eventPush()
	a.say(conv.ID, "after")
	b2.quiet()
	b1.quiet()
}

func TestInvitesNeedKnownNonAgentCallers(t *testing.T) {
	ch := newChat(t, "alice", "bob")
	a := ch.dial("alice")
	a.refuse(proto.TypeAgentCreate, proto.AgentCreateReq{Handle: "bob"}, proto.CodeInvalid)
	a.refuse(proto.TypeAgentCreate, proto.AgentCreateReq{Handle: "No Good"}, proto.CodeInvalid)
	var made proto.AgentCreateRes
	a.do(proto.TypeAgentCreate, proto.AgentCreateReq{Handle: "bot"}, &made)
	if made.Code == "" {
		t.Fatal("no code")
	}

	stranger := ch.h.Dial(t, testutil.NewKey(t))
	testutil.Hello(t, stranger)
	for _, typ := range []string{proto.TypeDeviceInvite, proto.TypeAgentCreate, proto.TypeAdminInvite} {
		f := testutil.Request(t, stranger, typ, map[string]string{"handle": "x"})
		if f.Error == nil || f.Error.Code != proto.CodeNotAuthorised {
			t.Fatalf("%s from unknown key = %+v", typ, f)
		}
	}
}

func TestInvitesExpireAfter24Hours(t *testing.T) {
	ch := newChat(t, "alice")
	start := time.Now()
	var offset atomic.Int64
	server.SetStoreClock(ch.h.Store, func() time.Time { return start.Add(time.Duration(offset.Load())) })
	a := ch.dial("alice")
	var inv, late proto.DeviceInviteRes
	a.do(proto.TypeDeviceInvite, nil, &inv)
	a.do(proto.TypeDeviceInvite, nil, &late)

	offset.Store(int64(23*time.Hour + 59*time.Minute))
	redeemAs(t, ch, inv.Code)
	offset.Store(int64(24*time.Hour + time.Minute))
	c := ch.h.Dial(t, testutil.NewKey(t))
	testutil.Hello(t, c)
	f := testutil.Request(t, c, proto.TypeInviteRedeem, proto.InviteRedeemReq{Code: late.Code})
	if f.Error == nil || f.Error.Code != proto.CodeNotAuthorised {
		t.Fatalf("expired invite redeem = %+v", f)
	}
}

func TestAdminRequestsNeedAdmin(t *testing.T) {
	ch := newChat(t, "alice", "bob")
	b := ch.dial("bob")
	b.refuse(proto.TypeAdminInvite, proto.AdminInviteReq{Handle: "carol"}, proto.CodeForbidden)
	b.refuse(proto.TypeAdminRemovePerson, proto.AdminRemovePersonReq{Handle: "alice"}, proto.CodeForbidden)
	b.refuse(proto.TypeAdminRemoveKey, proto.AdminRemoveKeyReq{Fingerprint: fingerprint(ch.keys["alice"])}, proto.CodeForbidden)
	if people, _ := ch.h.Store.People(); len(people) != 2 {
		t.Fatalf("people = %v", people)
	}
	if ok, _ := ch.h.Store.HasKey(fingerprint(ch.keys["alice"])); !ok {
		t.Fatal("alice's key was removed")
	}
}

func TestAdminInvite(t *testing.T) {
	ch := newChat(t, "alice", "bob")
	if err := ch.h.Store.SetAdmin("alice", true); err != nil {
		t.Fatal(err)
	}
	a := ch.dial("alice")
	var inv proto.AdminInviteRes
	a.do(proto.TypeAdminInvite, proto.AdminInviteReq{Handle: "carol", DisplayName: "Carol"}, &inv)
	_, p := redeemAs(t, ch, inv.Code)
	if p.Handle != "carol" || p.DisplayName != "Carol" || p.Agent || p.Admin {
		t.Fatalf("redeemed %+v", p)
	}
	// An invite for an existing handle would add the redeemer's key to that
	// person's account.
	a.refuse(proto.TypeAdminInvite, proto.AdminInviteReq{Handle: "bob"}, proto.CodeInvalid)
	a.refuse(proto.TypeAdminInvite, proto.AdminInviteReq{Handle: "No Good"}, proto.CodeInvalid)
}

func TestAdminRemoveKeyKicksDevice(t *testing.T) {
	ch := newChat(t, "alice", "bob")
	server.SetRecheckInterval(ch.h.Server, time.Hour) // only Kick can close it in time
	if err := ch.h.Store.SetAdmin("alice", true); err != nil {
		t.Fatal(err)
	}
	a, b := ch.dial("alice"), ch.dial("bob")
	fp := fingerprint(ch.keys["bob"])
	a.do(proto.TypeAdminRemoveKey, proto.AdminRemoveKeyReq{Fingerprint: fp}, nil)
	b.closed()
	if ok, _ := ch.h.Store.HasKey(fp); ok {
		t.Fatal("key still present")
	}
	a.refuse(proto.TypeAdminRemoveKey, proto.AdminRemoveKeyReq{Fingerprint: fp}, proto.CodeNotFound)
}

func TestAdminRemovePerson(t *testing.T) {
	ch := newChat(t, "alice", "bob", "carol")
	server.SetRecheckInterval(ch.h.Server, time.Hour) // only Kick can close it in time
	if err := ch.h.Store.SetAdmin("alice", true); err != nil {
		t.Fatal(err)
	}
	a, b1, b2, c := ch.dial("alice"), ch.dial("bob"), ch.dial("bob"), ch.dial("carol")
	r1 := roomWith(c, "one", b1)
	b2.convPush()
	b2.eventPush()
	r2 := roomWith(c, "two", b1)
	b2.convPush()
	b2.eventPush()

	a.refuse(proto.TypeAdminRemovePerson, proto.AdminRemovePersonReq{Handle: "alice"}, proto.CodeInvalid)
	a.refuse(proto.TypeAdminRemovePerson, proto.AdminRemovePersonReq{Handle: "nobody"}, proto.CodeNotFound)
	a.do(proto.TypeAdminRemovePerson, proto.AdminRemovePersonReq{Handle: "bob"}, nil)

	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		ev := c.eventPush()
		if ev.Kind != model.MemberLeft || ev.Target != "bob" || ev.Actor != "alice" {
			t.Fatalf("carol saw %+v", ev)
		}
		seen[ev.ConvID] = true
	}
	if !seen[r1.ID] || !seen[r2.ID] {
		t.Fatalf("member.left seen for %v", seen)
	}
	b1.closed()
	b2.closed()
	if _, err := ch.h.Store.PersonByKey(fingerprint(ch.keys["bob"])); err == nil {
		t.Fatal("bob's key still resolves")
	}
	a.refuse(proto.TypeAdminInvite, proto.AdminInviteReq{Handle: "bob"}, proto.CodeInvalid)
	if _, err := ch.h.Store.CreateInvite("bob", "", time.Hour); err == nil {
		t.Fatal("removed handle can be invited again")
	}
	c.quiet()
}

// A removed creator has no standing: in a private room the room is hidden from
// them, in an open room they are an ordinary non-member.
func TestRemovedCreatorCannotManageRoom(t *testing.T) {
	ch := newChat(t, "alice", "bob", "dave")
	if err := ch.h.Store.SetAdmin("dave", true); err != nil {
		t.Fatal(err)
	}
	a, d := ch.dial("alice"), ch.dial("dave")
	for _, tc := range []struct {
		name string
		open bool
		code string
	}{{"hidden", false, proto.CodeNotFound}, {"lounge", true, proto.CodeForbidden}} {
		conv := a.room(tc.name, tc.open)
		a.invite(conv.ID, "bob")
		d.do(proto.TypeRoomRemove, proto.RoomRemoveReq{Conv: conv.ID, Handle: "alice"}, nil)
		a.refuse(proto.TypeRoomInvite, proto.RoomInviteReq{Conv: conv.ID, Handle: "alice"}, tc.code)
		a.refuse(proto.TypeRoomRename, proto.RoomRenameReq{Conv: conv.ID, Name: "mine"}, tc.code)
		a.refuse(proto.TypeRoomRemove, proto.RoomRemoveReq{Conv: conv.ID, Handle: "bob"}, tc.code)
		if ok, _ := ch.h.Store.IsMember(conv.ID, "alice"); ok {
			t.Fatalf("%s: alice rejoined", tc.name)
		}
		if ok, _ := ch.h.Store.IsMember(conv.ID, "bob"); !ok {
			t.Fatalf("%s: bob was evicted", tc.name)
		}
	}
}

func TestHiddenConversationsAreIndistinguishableFromMissing(t *testing.T) {
	ch := newChat(t, "alice", "bob", "carol", "dave")
	if err := ch.h.Store.SetAdmin("dave", true); err != nil {
		t.Fatal(err)
	}
	a, c, d := ch.dial("alice"), ch.dial("carol"), ch.dial("dave")
	private := a.room("secret", false)
	var dm proto.DMOpenRes
	a.do(proto.TypeDMOpen, proto.DMOpenReq{Handle: "bob"}, &dm)
	ids := []string{private.ID, dm.Conv.ID, "r_doesnotexist"}

	reqs := func(id string) map[string]any {
		return map[string]any{
			proto.TypeConvHistory: proto.ConvHistoryReq{Conv: id},
			proto.TypeMsgSend:     proto.MsgSendReq{Conv: id, MsgID: "m", Body: text("x")},
			proto.TypeMsgEdit:     proto.MsgEditReq{Conv: id, MsgID: "m", Body: text("x")},
			proto.TypeMsgDelete:   proto.MsgDeleteReq{Conv: id, MsgID: "m"},
			proto.TypeReadSet:     proto.ReadSetReq{Conv: id, Seq: 1},
			proto.TypeRoomJoin:    proto.RoomJoinReq{Conv: id},
			proto.TypeRoomLeave:   proto.RoomLeaveReq{Conv: id},
			proto.TypeRoomInvite:  proto.RoomInviteReq{Conv: id, Handle: "carol"},
			proto.TypeRoomRemove:  proto.RoomRemoveReq{Conv: id, Handle: "alice"},
			proto.TypeRoomRename:  proto.RoomRenameReq{Conv: id, Name: "x"},
		}
	}
	for typ := range reqs("") {
		var want *proto.Error
		for i, id := range ids {
			f := c.req(typ, reqs(id)[typ])
			if f.Error == nil || f.Error.Code != proto.CodeNotFound {
				t.Fatalf("%s on %s as carol = %+v", typ, id, f)
			}
			if i == 0 {
				want = f.Error
			} else if *f.Error != *want {
				t.Fatalf("%s: %+v differs from %+v", typ, f.Error, want)
			}
		}
	}
	// An admin may see rooms but not other people's DMs.
	f := d.req(proto.TypeRoomRename, proto.RoomRenameReq{Conv: dm.Conv.ID, Name: "x"})
	if f.Error == nil || f.Error.Code != proto.CodeNotFound {
		t.Fatalf("admin on a DM = %+v", f)
	}
	d.do(proto.TypeRoomRename, proto.RoomRenameReq{Conv: private.ID, Name: "renamed"}, nil)
}
