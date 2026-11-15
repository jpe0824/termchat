package server_test

import (
	"testing"

	"github.com/jpe0824/termchat/internal/proto"
	"github.com/jpe0824/termchat/internal/testutil"
)

// redeemRefused redeems code from a fresh key and requires not_authorised.
func redeemRefused(t *testing.T, ch *chat, code string) {
	t.Helper()
	c := ch.h.Dial(t, testutil.NewKey(t))
	testutil.Hello(t, c)
	f := redeem(t, c, code)
	if f.Error == nil || f.Error.Code != proto.CodeNotAuthorised {
		t.Fatalf("redeem = %+v, want %s", f, proto.CodeNotAuthorised)
	}
}

func keyCount(t *testing.T, ch *chat, handle string) int {
	t.Helper()
	keys, err := ch.h.Store.Keys(handle)
	if err != nil {
		t.Fatalf("Keys(%s): %v", handle, err)
	}
	return len(keys)
}

// A member who prepares an agent invite for a handle before that person joins
// must not be able to add a device to the account created later.
func TestAgentInviteCannotJoinAccountCreatedLater(t *testing.T) {
	ch := newChat(t, "alice", "mallory")
	if err := ch.h.Store.SetAdmin("alice", true); err != nil {
		t.Fatal(err)
	}
	a, m := ch.dial("alice"), ch.dial("mallory")
	var agent proto.AgentCreateRes
	m.do(proto.TypeAgentCreate, proto.AgentCreateReq{Handle: "bob"}, &agent)
	var inv proto.AdminInviteRes
	a.do(proto.TypeAdminInvite, proto.AdminInviteReq{Handle: "bob", DisplayName: "Bob"}, &inv)

	_, bob := redeemAs(t, ch, inv.Code)
	if bob.Handle != "bob" || bob.Agent {
		t.Fatalf("bob = %+v", bob)
	}
	redeemRefused(t, ch, agent.Code)
	if n := keyCount(t, ch, "bob"); n != 1 {
		t.Fatalf("bob has %d keys, want 1", n)
	}
}

// The reverse order: the agent invite is redeemed first, so the handle now
// belongs to an agent the member owns; the person's own invite must not add
// their key to it.
func TestPersonInviteCannotJoinAccountCreatedLater(t *testing.T) {
	ch := newChat(t, "alice", "mallory")
	if err := ch.h.Store.SetAdmin("alice", true); err != nil {
		t.Fatal(err)
	}
	a, m := ch.dial("alice"), ch.dial("mallory")
	var agent proto.AgentCreateRes
	m.do(proto.TypeAgentCreate, proto.AgentCreateReq{Handle: "bob"}, &agent)
	var inv proto.AdminInviteRes
	a.do(proto.TypeAdminInvite, proto.AdminInviteReq{Handle: "bob", DisplayName: "Bob"}, &inv)

	_, bot := redeemAs(t, ch, agent.Code)
	if !bot.Agent || bot.Owner != "mallory" {
		t.Fatalf("agent = %+v", bot)
	}
	redeemRefused(t, ch, inv.Code)
	if n := keyCount(t, ch, "bob"); n != 1 {
		t.Fatalf("bob has %d keys, want 1", n)
	}
}

// Two new-person invites for one handle: only the first redeemed creates the
// account, the second is refused rather than adding a device.
func TestSecondPersonInviteForSameHandleIsRefused(t *testing.T) {
	ch := newChat(t, "alice")
	if err := ch.h.Store.SetAdmin("alice", true); err != nil {
		t.Fatal(err)
	}
	a := ch.dial("alice")
	var one, two proto.AdminInviteRes
	a.do(proto.TypeAdminInvite, proto.AdminInviteReq{Handle: "carol"}, &one)
	a.do(proto.TypeAdminInvite, proto.AdminInviteReq{Handle: "carol"}, &two)
	redeemAs(t, ch, one.Code)
	redeemRefused(t, ch, two.Code)
	if n := keyCount(t, ch, "carol"); n != 1 {
		t.Fatalf("carol has %d keys, want 1", n)
	}
}

// A device invite adds a key to the caller's own account and nothing else.
func TestDeviceInviteAddsToOwnAccount(t *testing.T) {
	ch := newChat(t, "alice")
	a := ch.dial("alice")
	var inv proto.DeviceInviteRes
	a.do(proto.TypeDeviceInvite, nil, &inv)
	if _, p := redeemAs(t, ch, inv.Code); p.Handle != "alice" {
		t.Fatalf("redeemed as %+v", p)
	}
	if n := keyCount(t, ch, "alice"); n != 2 {
		t.Fatalf("alice has %d keys, want 2", n)
	}
}
