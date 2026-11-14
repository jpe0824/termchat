package server

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jpe0824/termchat/internal/model"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func people(t *testing.T, s *Store, handles ...string) {
	t.Helper()
	for _, h := range handles {
		if err := s.CreatePerson(model.Person{Handle: h}); err != nil {
			t.Fatalf("CreatePerson(%s): %v", h, err)
		}
	}
}

func room(t *testing.T, s *Store, name, creator string) model.Conversation {
	t.Helper()
	c, err := s.CreateRoom(name, true, creator)
	if err != nil {
		t.Fatalf("CreateRoom(%s): %v", name, err)
	}
	return c
}

func text(s string) model.Body { return model.Body{Format: "text", Data: []byte(s)} }

func post(t *testing.T, s *Store, conv, actor, msgID, body string) model.Event {
	t.Helper()
	ev, dup, err := s.PostMessage(conv, actor, msgID, text(body), nil)
	if err != nil || dup {
		t.Fatalf("PostMessage(%s): dup=%v err=%v", msgID, dup, err)
	}
	return ev
}

func history(t *testing.T, s *Store, conv string) []model.Event {
	t.Helper()
	evs, err := s.After(conv, 0)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	return evs
}

func TestPostAssignsIncreasingSeqPerConversation(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann")
	a, b := room(t, s, "a", "ann"), room(t, s, "b", "ann")
	// CreateRoom already appended member.joined at seq 1.
	for i, want := range []int64{2, 3, 4} {
		if ev := post(t, s, a.ID, "ann", fmt.Sprintf("a%d", i), "x"); ev.Seq != want {
			t.Fatalf("conv a post %d: seq %d, want %d", i, ev.Seq, want)
		}
	}
	if ev := post(t, s, b.ID, "ann", "b0", "x"); ev.Seq != 2 {
		t.Fatalf("conv b first post: seq %d, want 2", ev.Seq)
	}
}

func TestSeqStartsAtOne(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann", "bob")
	dm, err := s.OpenDM("ann", "bob")
	if err != nil {
		t.Fatal(err)
	}
	if ev := post(t, s, dm.ID, "ann", "m1", "hi"); ev.Seq != 1 {
		t.Fatalf("first event in a DM has seq %d, want 1", ev.Seq)
	}
}

func TestPostIsIdempotentOnMsgID(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann")
	r := room(t, s, "gen", "ann")
	first := post(t, s, r.ID, "ann", "m1", "hello")
	again, dup, err := s.PostMessage(r.ID, "ann", "m1", text("hello"), nil)
	if err != nil || !dup {
		t.Fatalf("second post: dup=%v err=%v", dup, err)
	}
	if again.Seq != first.Seq {
		t.Fatalf("duplicate got seq %d, want %d", again.Seq, first.Seq)
	}
	n := 0
	for _, ev := range history(t, s, r.ID) {
		if ev.Kind == model.MessagePosted {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d message rows, want 1", n)
	}
}

func TestPostStoresBodyAndMentionsOpaquely(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann")
	r := room(t, s, "gen", "ann")
	raw := model.Body{Format: "weird/x-unknown", Data: []byte{0, 1, 0xff, '{', '"'}}
	if _, _, err := s.PostMessage(r.ID, "ann", "m1", raw, []string{"bob", "cy"}); err != nil {
		t.Fatal(err)
	}
	evs := history(t, s, r.ID)
	got := evs[len(evs)-1]
	if got.Body == nil || got.Body.Format != raw.Format || string(got.Body.Data) != string(raw.Data) {
		t.Fatalf("body round trip: %+v", got.Body)
	}
	if len(got.Mentions) != 2 || got.Mentions[0] != "bob" || got.Mentions[1] != "cy" {
		t.Fatalf("mentions: %v", got.Mentions)
	}
	if got.Actor != "ann" || got.MsgID != "m1" || got.ConvID != r.ID {
		t.Fatalf("event fields: %+v", got)
	}
}

func TestEditBlanksOriginalBody(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann")
	r := room(t, s, "gen", "ann")
	orig := post(t, s, r.ID, "ann", "m1", "first")
	e1, err := s.EditMessage(r.ID, "ann", "m1", text("second"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if e1.Kind != model.MessageEdited || e1.MsgID != "m1" || string(e1.Body.Data) != "second" {
		t.Fatalf("edit event: %+v", e1)
	}
	if _, err := s.EditMessage(r.ID, "ann", "m1", text("third"), nil); err != nil {
		t.Fatal(err)
	}
	var edits []model.Event
	for _, ev := range history(t, s, r.ID) {
		switch {
		case ev.Seq == orig.Seq:
			if ev.Body != nil {
				t.Fatalf("original body survived: %+v", ev.Body)
			}
		case ev.Kind == model.MessageEdited:
			edits = append(edits, ev)
		}
	}
	if len(edits) != 2 || edits[0].Body != nil || string(edits[1].Body.Data) != "third" {
		t.Fatalf("edit chain: %+v", edits)
	}
}

func TestDeleteBlanksOriginalAndEdits(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann")
	r := room(t, s, "gen", "ann")
	post(t, s, r.ID, "ann", "m1", "first")
	if _, err := s.EditMessage(r.ID, "ann", "m1", text("second"), nil); err != nil {
		t.Fatal(err)
	}
	del, err := s.DeleteMessage(r.ID, "ann", "m1")
	if err != nil {
		t.Fatal(err)
	}
	if del.Kind != model.MessageDeleted || del.Body != nil || del.MsgID != "m1" {
		t.Fatalf("delete event: %+v", del)
	}
	for _, ev := range history(t, s, r.ID) {
		if ev.MsgID == "m1" && ev.Body != nil {
			t.Fatalf("body survived on seq %d (%s)", ev.Seq, ev.Kind)
		}
	}
}

func TestEditByNonAuthorForbidden(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann", "bob")
	r := room(t, s, "gen", "ann")
	post(t, s, r.ID, "ann", "m1", "mine")
	if _, err := s.EditMessage(r.ID, "bob", "m1", text("x"), nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("edit: %v, want ErrForbidden", err)
	}
	if _, err := s.DeleteMessage(r.ID, "bob", "m1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("delete: %v, want ErrForbidden", err)
	}
	for _, ev := range history(t, s, r.ID) {
		if ev.MsgID == "m1" && (ev.Body == nil || string(ev.Body.Data) != "mine") {
			t.Fatalf("forbidden call changed the message: %+v", ev)
		}
	}
}

func TestEditOrDeleteMissingOrDeletedIsNotFound(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann")
	r := room(t, s, "gen", "ann")
	if _, err := s.EditMessage(r.ID, "ann", "nope", text("x"), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("edit missing: %v", err)
	}
	post(t, s, r.ID, "ann", "m1", "x")
	if _, err := s.DeleteMessage(r.ID, "ann", "m1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditMessage(r.ID, "ann", "m1", text("y"), nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("edit deleted: %v", err)
	}
	if _, err := s.DeleteMessage(r.ID, "ann", "m1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete deleted: %v", err)
	}
}

func TestReadNeverMovesBack(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann")
	r := room(t, s, "gen", "ann")
	for _, seq := range []int64{10, 4} {
		if err := s.SetRead("ann", r.ID, seq); err != nil {
			t.Fatal(err)
		}
	}
	cs, err := s.ConversationsFor("ann")
	if err != nil || len(cs) != 1 {
		t.Fatalf("ConversationsFor: %v %v", cs, err)
	}
	if cs[0].ReadSeq != 10 {
		t.Fatalf("ReadSeq %d, want 10", cs[0].ReadSeq)
	}
}

func TestInviteSingleUseAndExpiry(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }

	code, err := s.CreateInvite("dana", "Dana D", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.RedeemInvite(code, "fp1", []byte("pub1"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Handle != "dana" || p.DisplayName != "Dana D" || p.Agent {
		t.Fatalf("person: %+v", p)
	}
	if _, err := s.RedeemInvite(code, "fp2", []byte("pub2")); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("second redeem: %v", err)
	}

	old, err := s.CreateInvite("eli", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour + time.Second)
	if _, err := s.RedeemInvite(old, "fp3", []byte("pub3")); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("expired redeem: %v", err)
	}
	if _, err := s.RedeemInvite("NOSUCHCODE", "fp4", nil); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("unknown code: %v", err)
	}
	if _, err := s.Person("eli"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired invite created a person: %v", err)
	}
}

func TestInviteCodeShape(t *testing.T) {
	s := newStore(t)
	re := regexp.MustCompile(`^[A-Z2-7]{10}$`)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		c, err := s.CreateInvite("dana", "", time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if !re.MatchString(c) || seen[c] {
			t.Fatalf("bad or repeated code %q", c)
		}
		seen[c] = true
	}
	if _, err := s.CreateInvite("has space", "", time.Hour); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("invalid handle: %v", err)
	}
}

func TestRedeemForExistingHandleAddsKey(t *testing.T) {
	s := newStore(t)
	code, _ := s.CreateInvite("dana", "Dana", time.Hour)
	if _, err := s.RedeemInvite(code, "fp1", []byte("k1")); err != nil {
		t.Fatal(err)
	}
	code2, err := s.CreateInvite("dana", "Other Name", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.RedeemInvite(code2, "fp2", []byte("k2"))
	if err != nil {
		t.Fatal(err)
	}
	if p.DisplayName != "Dana" {
		t.Fatalf("existing person was changed: %+v", p)
	}
	keys, err := s.Keys("dana")
	if err != nil || len(keys) != 2 {
		t.Fatalf("Keys: %v %v", keys, err)
	}
	if n, _ := s.PersonCount(); n != 1 {
		t.Fatalf("PersonCount %d, want 1", n)
	}
}

func TestRedeemWithKnownFingerprintKeepsInvite(t *testing.T) {
	s := newStore(t)
	code, _ := s.CreateInvite("dana", "", time.Hour)
	if _, err := s.RedeemInvite(code, "fp1", nil); err != nil {
		t.Fatal(err)
	}
	code2, _ := s.CreateInvite("eli", "", time.Hour)
	if _, err := s.RedeemInvite(code2, "fp1", nil); !errors.Is(err, ErrExists) {
		t.Fatalf("reused fingerprint: %v, want ErrExists", err)
	}
	if _, err := s.RedeemInvite(code2, "fp9", nil); err != nil {
		t.Fatalf("invite was burned by the failed redeem: %v", err)
	}
}

func TestAgentInvite(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann")
	code, err := s.CreateAgentInvite("Botty", "Botty", "Ann", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.RedeemInvite(code, "fpa", []byte("k"))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Agent || p.Owner != "ann" || p.Handle != "botty" {
		t.Fatalf("agent person: %+v", p)
	}
	got, err := s.Person("botty")
	if err != nil || got != p {
		t.Fatalf("Person: %+v %v", got, err)
	}
}

func TestPurgeBlanksOldBodies(t *testing.T) {
	s := newStore(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	people(t, s, "ann")
	r := room(t, s, "gen", "ann") // member.joined at seq 1
	old := post(t, s, r.ID, "ann", "old", "old")
	now = now.Add(48 * time.Hour)
	fresh := post(t, s, r.ID, "ann", "new", "new")

	n, err := s.PurgeOlderThan(24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("purged %d, want 1", n)
	}
	evs := history(t, s, r.ID)
	if len(evs) != 3 {
		t.Fatalf("%d events remain, want 3", len(evs))
	}
	for i, ev := range evs {
		if ev.Seq != int64(i+1) {
			t.Fatalf("seq gap: %+v", evs)
		}
		switch ev.Seq {
		case old.Seq:
			if ev.Body != nil {
				t.Fatal("old body survived")
			}
		case fresh.Seq:
			if ev.Body == nil {
				t.Fatal("fresh body purged")
			}
		}
	}
	if evs[0].Kind != model.MemberJoined {
		t.Fatalf("membership event lost: %+v", evs[0])
	}
}

func TestNamesNormalisedAndValidated(t *testing.T) {
	s := newStore(t)
	if err := s.CreatePerson(model.Person{Handle: "Dana"}); err != nil {
		t.Fatal(err)
	}
	if p, err := s.Person("@DANA"); err != nil || p.Handle != "dana" {
		t.Fatalf("Person: %+v %v", p, err)
	}
	if err := s.CreatePerson(model.Person{Handle: "dana"}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if err := s.CreatePerson(model.Person{Handle: "no good"}); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("bad handle: %v", err)
	}
	if _, err := s.CreateRoom("has space", true, "dana"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("bad room: %v", err)
	}
	c, err := s.CreateRoom("#General", true, "Dana")
	if err != nil || c.Name != "general" || c.Creator != "dana" {
		t.Fatalf("CreateRoom: %+v %v", c, err)
	}
	if _, err := s.CreateRoom("general", false, "dana"); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate room: %v", err)
	}
	if _, err := s.CreateRoom("ghost-room", true, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown creator: %v", err)
	}
}

func TestHistoryPaging(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann", "bob")
	dm, _ := s.OpenDM("ann", "bob")
	for i := 1; i <= 120; i++ {
		post(t, s, dm.ID, "ann", fmt.Sprintf("m%d", i), "x")
	}
	latest, err := s.History(dm.ID, 0, 50)
	if err != nil || len(latest) != 50 || latest[0].Seq != 71 || latest[49].Seq != 120 {
		t.Fatalf("latest page: n=%d err=%v", len(latest), err)
	}
	older, err := s.History(dm.ID, 71, 50)
	if err != nil || len(older) != 50 || older[0].Seq != 21 || older[49].Seq != 70 {
		t.Fatalf("older page: n=%d err=%v", len(older), err)
	}
	rest, _ := s.History(dm.ID, 21, 50)
	if len(rest) != 20 || rest[0].Seq != 1 {
		t.Fatalf("last page: n=%d", len(rest))
	}
	tail, err := s.After(dm.ID, 115)
	if err != nil || len(tail) != 5 || tail[0].Seq != 116 {
		t.Fatalf("After: n=%d err=%v", len(tail), err)
	}
}

func TestMembershipEvents(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann", "bob")
	r := room(t, s, "gen", "ann")
	if r.LastSeq != 1 || len(r.Members) != 1 || r.Members[0] != "ann" {
		t.Fatalf("new room: %+v", r)
	}
	evs := history(t, s, r.ID)
	if evs[0].Kind != model.MemberJoined || evs[0].Actor != "ann" || evs[0].Target != "ann" {
		t.Fatalf("creator join event: %+v", evs[0])
	}

	ev, err := s.AddMember(r.ID, "ann", "Bob")
	if err != nil || ev.Kind != model.MemberJoined || ev.Actor != "ann" || ev.Target != "bob" || ev.Seq != 2 {
		t.Fatalf("AddMember: %+v %v", ev, err)
	}
	if _, err := s.AddMember(r.ID, "ann", "bob"); !errors.Is(err, ErrExists) {
		t.Fatalf("re-add: %v", err)
	}
	if ok, _ := s.IsMember(r.ID, "bob"); !ok {
		t.Fatal("bob not a member")
	}
	ev, err = s.RemoveMember(r.ID, "bob", "bob")
	if err != nil || ev.Kind != model.MemberLeft || ev.Target != "bob" || ev.Seq != 3 {
		t.Fatalf("RemoveMember: %+v %v", ev, err)
	}
	if _, err := s.RemoveMember(r.ID, "bob", "bob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove non-member: %v", err)
	}
	if ok, _ := s.IsMember(r.ID, "bob"); ok {
		t.Fatal("bob still a member")
	}
	if _, err := s.AddMember(r.ID, "ann", "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("add unknown person: %v", err)
	}
	if _, err := s.AddMember("nope", "ann", "bob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("add to unknown conv: %v", err)
	}
}

func TestRenameRoom(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann")
	r := room(t, s, "gen", "ann")
	room(t, s, "other", "ann")
	ev, err := s.RenameRoom(r.ID, "ann", "General")
	if err != nil || ev.Kind != model.RoomRenamed || ev.Name != "general" || ev.Actor != "ann" {
		t.Fatalf("RenameRoom: %+v %v", ev, err)
	}
	c, _ := s.Conversation(r.ID)
	if c.Name != "general" || c.LastSeq != ev.Seq {
		t.Fatalf("conversation after rename: %+v", c)
	}
	if _, err := s.RenameRoom(r.ID, "ann", "other"); !errors.Is(err, ErrExists) {
		t.Fatalf("collision: %v", err)
	}
	if _, err := s.RenameRoom(r.ID, "ann", "bad name"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("invalid: %v", err)
	}
}

func TestOpenDM(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann", "bob")
	d1, err := s.OpenDM("ann", "bob")
	if err != nil {
		t.Fatal(err)
	}
	d2, err := s.OpenDM("Bob", "ann")
	if err != nil || d2.ID != d1.ID {
		t.Fatalf("not idempotent: %q vs %q (%v)", d1.ID, d2.ID, err)
	}
	if d1.Kind != model.ConvDM || len(d1.Members) != 2 {
		t.Fatalf("dm: %+v", d1)
	}
	for _, h := range []string{"ann", "bob"} {
		if ok, _ := s.IsMember(d1.ID, h); !ok {
			t.Fatalf("%s not a member", h)
		}
	}
	if _, err := s.OpenDM("ann", "ann"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("self dm: %v", err)
	}
	if _, err := s.OpenDM("ann", "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown dm: %v", err)
	}
}

func TestPeopleKeysAndAdmin(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann", "bob")
	if n, _ := s.PersonCount(); n != 2 {
		t.Fatalf("PersonCount %d", n)
	}
	if err := s.AddKey("ann", "fp1", []byte("k1")); err != nil {
		t.Fatal(err)
	}
	if err := s.AddKey("ann", "fp2", []byte("k2")); err != nil {
		t.Fatal(err)
	}
	if err := s.AddKey("bob", "fp1", nil); !errors.Is(err, ErrExists) {
		t.Fatalf("shared fingerprint: %v", err)
	}
	if err := s.AddKey("ghost", "fp9", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("key for unknown person: %v", err)
	}
	if p, err := s.PersonByKey("fp2"); err != nil || p.Handle != "ann" {
		t.Fatalf("PersonByKey: %+v %v", p, err)
	}
	if _, err := s.PersonByKey("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown key: %v", err)
	}
	if ok, _ := s.HasKey("fp1"); !ok {
		t.Fatal("HasKey fp1")
	}
	if ok, _ := s.HasKey("nope"); ok {
		t.Fatal("HasKey nope")
	}
	keys, err := s.Keys("ann")
	if err != nil || len(keys) != 2 || keys[0].AddedAt.IsZero() {
		t.Fatalf("Keys: %+v %v", keys, err)
	}
	if err := s.RemoveKey("fp1"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveKey("fp1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove twice: %v", err)
	}

	if err := s.SetAdmin("ann", true); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.Person("ann"); !p.Admin {
		t.Fatal("admin not set")
	}
	if err := s.SetAdmin("ghost", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetAdmin unknown: %v", err)
	}
	all, err := s.People()
	if err != nil || len(all) != 2 || all[0].Handle != "ann" || all[1].Handle != "bob" {
		t.Fatalf("People: %+v %v", all, err)
	}
}

func TestRemovePersonDropsKeysAndMemberships(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann", "bob")
	r := room(t, s, "gen", "ann")
	if _, err := s.AddMember(r.ID, "ann", "bob"); err != nil {
		t.Fatal(err)
	}
	dm, _ := s.OpenDM("ann", "bob")
	_ = s.AddKey("bob", "fpb", nil)
	_ = s.SetRead("bob", r.ID, 2)
	if err := s.RemovePerson("bob"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.HasKey("fpb"); ok {
		t.Fatal("key survived")
	}
	for _, id := range []string{r.ID, dm.ID} {
		if ok, _ := s.IsMember(id, "bob"); ok {
			t.Fatalf("membership of %s survived", id)
		}
	}
	if err := s.RemovePerson("bob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove twice: %v", err)
	}
	if cs, _ := s.ConversationsFor("bob"); len(cs) != 0 {
		t.Fatalf("ConversationsFor removed person: %+v", cs)
	}
}

func TestRoomListsAndConversationsFor(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann", "bob")
	pub := room(t, s, "pub", "ann")
	priv, err := s.CreateRoom("priv", false, "ann")
	if err != nil {
		t.Fatal(err)
	}
	dm, _ := s.OpenDM("ann", "bob")
	post(t, s, pub.ID, "ann", "m1", "x")
	post(t, s, dm.ID, "ann", "m2", "x")
	if err := s.SetRead("ann", pub.ID, 2); err != nil {
		t.Fatal(err)
	}

	open, err := s.OpenRooms()
	if err != nil || len(open) != 1 || open[0].ID != pub.ID || len(open[0].Members) != 1 || open[0].LastSeq != 2 {
		t.Fatalf("OpenRooms: %+v %v", open, err)
	}
	all, err := s.Rooms()
	if err != nil || len(all) != 2 {
		t.Fatalf("Rooms: %+v %v", all, err)
	}

	cs, err := s.ConversationsFor("ann")
	if err != nil || len(cs) != 3 {
		t.Fatalf("ConversationsFor(ann): %+v %v", cs, err)
	}
	byID := map[string]model.Conversation{}
	for _, c := range cs {
		byID[c.ID] = c
	}
	if c := byID[pub.ID]; c.LastSeq != 2 || c.ReadSeq != 2 || len(c.Members) != 1 {
		t.Fatalf("pub: %+v", c)
	}
	if c := byID[dm.ID]; c.LastSeq != 1 || c.ReadSeq != 0 || len(c.Members) != 2 {
		t.Fatalf("dm: %+v", c)
	}
	if _, ok := byID[priv.ID]; !ok {
		t.Fatal("private room missing for its member")
	}
	bobs, _ := s.ConversationsFor("bob")
	if len(bobs) != 1 || bobs[0].ID != dm.ID {
		t.Fatalf("ConversationsFor(bob): %+v", bobs)
	}
	got, err := s.Conversation(pub.ID)
	if err != nil || got.LastSeq != 2 || got.Name != "pub" || len(got.Members) != 1 {
		t.Fatalf("Conversation: %+v %v", got, err)
	}
	if _, err := s.Conversation("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown conversation: %v", err)
	}
}

func TestAppendFromManyGoroutinesIsGapFree(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann")
	r := room(t, s, "gen", "ann")
	const workers, each = 8, 25
	var wg sync.WaitGroup
	errc := make(chan error, workers*each)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				_, _, err := s.PostMessage(r.ID, "ann", fmt.Sprintf("w%d-%d", w, i), text("x"), nil)
				errc <- err
			}
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertDense(t, s, r.ID, 1+workers*each)
}

func TestTwoHandlesOnOneFile(t *testing.T) {
	dir := t.TempDir()
	a, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	people(t, a, "ann")
	if _, err := b.Person("ann"); err != nil {
		t.Fatalf("second handle cannot see first handle's write: %v", err)
	}
	r := room(t, b, "gen", "ann")
	var wg sync.WaitGroup
	errc := make(chan error, 80)
	for i, st := range []*Store{a, b} {
		for g := 0; g < 2; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 20; j++ {
					_, _, err := st.PostMessage(r.ID, "ann", fmt.Sprintf("h%d-g%d-%d", i, g, j), text("x"), nil)
					errc <- err
				}
			}()
		}
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertDense(t, a, r.ID, 1+80)
}

func assertDense(t *testing.T, s *Store, conv string, want int) {
	t.Helper()
	evs := history(t, s, conv)
	if len(evs) != want {
		t.Fatalf("%d events, want %d", len(evs), want)
	}
	for i, ev := range evs {
		if ev.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d", i, ev.Seq)
		}
	}
}

func TestMigrationsAreReopenable(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	people(t, s, "ann")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	if _, err := s.Person("ann"); err != nil {
		t.Fatalf("data lost across reopen: %v", err)
	}
}

func TestTimestampsAreUTCWithMilliseconds(t *testing.T) {
	s := newStore(t)
	at := time.Date(2026, 3, 4, 5, 6, 7, 123_000_000, time.FixedZone("x", 3600))
	s.now = func() time.Time { return at }
	people(t, s, "ann")
	r := room(t, s, "gen", "ann")
	ev := post(t, s, r.ID, "ann", "m1", "x")
	if !ev.At.Equal(at) || ev.At.Location() != time.UTC {
		t.Fatalf("returned At %v (%v), want %v in UTC", ev.At, ev.At.Location(), at)
	}
	evs := history(t, s, r.ID)
	if got := evs[len(evs)-1].At; !got.Equal(at) || got.Location() != time.UTC {
		t.Fatalf("stored At %v", got)
	}
}

func TestRemovedHandleCannotBeRegisteredAgain(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann", "bob")
	if err := s.RemovePerson("bob"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePerson(model.Person{Handle: "bob"}); !errors.Is(err, ErrExists) {
		t.Fatalf("re-create: %v, want ErrExists", err)
	}
	if _, err := s.CreateInvite("bob", "", time.Hour); !errors.Is(err, ErrExists) {
		t.Fatalf("invite: %v, want ErrExists", err)
	}
	if _, err := s.CreateAgentInvite("bob", "", "ann", time.Hour); !errors.Is(err, ErrExists) {
		t.Fatalf("agent invite: %v, want ErrExists", err)
	}
}

func TestRedeemInviteIssuedBeforeRemovalFails(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann")
	code, err := s.CreateInvite("bob", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemInvite(code, "fpb", []byte("k")); err != nil {
		t.Fatal(err)
	}
	code2, err := s.CreateInvite("bob", "", time.Hour) // second device
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RemovePerson("bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemInvite(code2, "fpb2", []byte("k")); !errors.Is(err, ErrExists) {
		t.Fatalf("redeem for removed handle: %v, want ErrExists", err)
	}
	if ok, _ := s.HasKey("fpb2"); ok {
		t.Fatal("key registered for a removed handle")
	}
}

func TestRemovedPersonIsInvisible(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann", "bob")
	if err := s.AddKey("bob", "fpb", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RemovePerson("bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Person("bob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Person: %v", err)
	}
	if _, err := s.PersonByKey("fpb"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("PersonByKey: %v", err)
	}
	all, _ := s.People()
	if len(all) != 1 || all[0].Handle != "ann" {
		t.Fatalf("People: %+v", all)
	}
	if n, _ := s.PersonCount(); n != 1 {
		t.Fatalf("PersonCount %d, want 1", n)
	}
	if _, err := s.OpenDM("ann", "bob"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("OpenDM with removed: %v, want ErrNotFound", err)
	}
	if err := s.AddKey("bob", "fpx", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AddKey for removed: %v", err)
	}
	if err := s.SetAdmin("bob", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetAdmin for removed: %v", err)
	}
}

func TestDMHistorySurvivesRemoval(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann", "bob")
	dm, _ := s.OpenDM("ann", "bob")
	post(t, s, dm.ID, "bob", "m1", "from bob")
	post(t, s, dm.ID, "ann", "m2", "from ann")
	if err := s.RemovePerson("bob"); err != nil {
		t.Fatal(err)
	}
	evs, err := s.History(dm.ID, 0, 50)
	if err != nil || len(evs) != 2 {
		t.Fatalf("History: %+v %v", evs, err)
	}
	if evs[0].Actor != "bob" || string(evs[0].Body.Data) != "from bob" {
		t.Fatalf("authored event changed: %+v", evs[0])
	}
	if ok, _ := s.IsMember(dm.ID, "ann"); !ok {
		t.Fatal("remaining member lost access")
	}
	cs, _ := s.ConversationsFor("ann")
	if len(cs) != 1 || cs[0].ID != dm.ID || len(cs[0].Members) != 1 {
		t.Fatalf("ConversationsFor(ann): %+v", cs)
	}
}

func TestSetAdminRefusesAgentGrant(t *testing.T) {
	s := newStore(t)
	people(t, s, "ann")
	if err := s.CreatePerson(model.Person{Handle: "bot", Agent: true, Owner: "ann"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAdmin("bot", true); !errors.Is(err, ErrAgent) || err.Error() != "agent accounts cannot be admins: bot" {
		t.Fatalf("SetAdmin(agent) = %v, want ErrAgent", err)
	}
	if p, _ := s.Person("bot"); p.Admin {
		t.Fatal("agent was made admin")
	}
}

// ConversationsFor batches its member, last-seq and read-marker queries; each
// row must still match what a single Conversation load and readSeq give.
func TestConversationsForMatchesSingleLoads(t *testing.T) {
	s := newStore(t)
	people(t, s, "alice", "bob", "carol", "dave")
	dm, err := s.OpenDM("bob", "alice") // no events, never read
	if err != nil {
		t.Fatal(err)
	}
	zeta := room(t, s, "zeta", "carol") // members join out of handle order
	for _, h := range []string{"bob", "alice"} {
		if _, err := s.AddMember(zeta.ID, "carol", h); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 3 {
		post(t, s, zeta.ID, "carol", fmt.Sprintf("z%d", i), "hi")
	}
	if err := s.SetRead("alice", zeta.ID, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRead("bob", zeta.ID, 6); err != nil {
		t.Fatal(err)
	}
	alpha := room(t, s, "alpha", "alice") // never read by alice
	if _, err := s.AddMember(alpha.ID, "alice", "carol"); err != nil {
		t.Fatal(err)
	}
	post(t, s, alpha.ID, "carol", "a1", "hi")
	if err := s.SetRead("carol", alpha.ID, 3); err != nil {
		t.Fatal(err)
	}
	room(t, s, "elsewhere", "dave") // alice is not a member

	got, err := s.ConversationsFor("alice")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		members       string
		last, readSeq int64
	}{
		dm.ID:    {"alice,bob", 0, 0},
		zeta.ID:  {"alice,bob,carol", 6, 2},
		alpha.ID: {"alice,carol", 3, 0},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d conversations, want %d: %+v", len(got), len(want), got)
	}
	for i, c := range got {
		if i > 0 && got[i-1].ID >= c.ID {
			t.Fatalf("not ordered by id: %s before %s", got[i-1].ID, c.ID)
		}
		w, ok := want[c.ID]
		if !ok {
			t.Fatalf("unexpected conversation %s", c.ID)
		}
		if m := strings.Join(c.Members, ","); m != w.members || c.LastSeq != w.last || c.ReadSeq != w.readSeq {
			t.Fatalf("%s: members=%s last=%d read=%d, want %s %d %d", c.ID, m, c.LastSeq, c.ReadSeq, w.members, w.last, w.readSeq)
		}
		one, err := s.Conversation(c.ID)
		if err != nil {
			t.Fatal(err)
		}
		if one.ReadSeq, err = s.readSeq("alice", c.ID); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(one, c) {
			t.Fatalf("batched %+v, single load %+v", c, one)
		}
	}
}

func TestBlankMessageUsesMsgIndex(t *testing.T) {
	s := newStore(t)
	rows, err := s.db.Query(`EXPLAIN QUERY PLAN `+blankMessageSQL, "c", "m", "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if p := strings.Join(plan, "; "); !strings.Contains(p, "USING INDEX events_msg") {
		t.Fatalf("plan = %s, want events_msg", p)
	}
}
