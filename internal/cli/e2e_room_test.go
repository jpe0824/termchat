package cli

import (
	"strings"
	"testing"
)

type convView struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func (p *person) conv(args ...string) convView {
	p.t.Helper()
	var c convView
	decode(p.t, p.ok(append(args, "--json")...), &c)
	return c
}

func (p *person) hasChat(name string, all bool) bool {
	args := []string{}
	if all {
		args = append(args, "--all")
	}
	for _, c := range p.chats(args...) {
		if c.Name == name {
			return true
		}
	}
	return false
}

func TestDMOpensOrCreates(t *testing.T) {
	_, a, b := pair(t)
	c := a.conv("dm", "bob")
	if c.Kind != "dm" || c.Name != "bob" || c.ID == "" {
		t.Fatalf("dm = %+v", c)
	}
	if again := a.conv("dm", "@Bob"); again != c {
		t.Fatalf("second dm = %+v, want %+v", again, c)
	}
	if out := a.tableOK("dm", "bob"); out != c.ID+"\n" {
		t.Fatalf("dm table = %q, want the id", out)
	}
	eventually(t, "bob to list the dm", func() bool { return b.hasChat("alice", false) })
	a.do("dm", "Bad Name!").fail(t, 2, `invalid handle "Bad Name!": use lowercase letters, digits, - and _, starting with a letter or digit, at most 32 characters`)
	a.do("dm", "nobody").fail(t, 3, "no such person: nobody")
}

func TestRoomLifecycle(t *testing.T) {
	_, a, b := pair(t)
	a.ok("room", "create", "lounge")
	team := a.conv("room", "create", "#Team", "--private")
	if team.Kind != "room" || team.Name != "team" || team.ID == "" {
		t.Fatalf("create = %+v", team)
	}
	if b.hasChat("team", true) {
		t.Fatal("a private room is visible to a stranger")
	}
	if got := a.conv("room", "invite", "team", "bob"); got.ID != team.ID {
		t.Fatalf("invite = %+v", got)
	}
	eventually(t, "bob to be invited", func() bool { return b.hasChat("team", false) })

	if got := a.conv("room", "rename", "team", "crew"); got.ID != team.ID || got.Name != "crew" {
		t.Fatalf("rename = %+v", got)
	}
	eventually(t, "bob to see the new name", func() bool { return b.hasChat("crew", false) })

	if got := a.conv("room", "remove", "crew", "bob"); got.ID != team.ID {
		t.Fatalf("remove = %+v", got)
	}
	eventually(t, "bob to lose the room", func() bool { return !b.hasChat("crew", false) })

	joined := b.conv("room", "join", "lounge")
	if joined.Kind != "room" || joined.Name != "lounge" || joined.ID == "" {
		t.Fatalf("join = %+v", joined)
	}
	if !b.hasChat("lounge", false) {
		t.Fatal("bob does not list the room he joined")
	}
	b.conv("room", "leave", "#lounge")
	eventually(t, "bob to leave", func() bool { return !b.hasChat("lounge", false) && b.hasChat("lounge", true) })

	if out := a.tableOK("room", "create", "tables"); !strings.HasPrefix(out, "created #tables ") {
		t.Fatalf("create table = %q", out)
	}
}

func TestRoomErrors(t *testing.T) {
	_, a, b := pair(t)
	a.ok("room", "create", "lounge")
	a.do("room", "create", "bad name").fail(t, 2, `invalid room name "bad name": use lowercase letters, digits, - and _, starting with a letter or digit, at most 32 characters`)
	a.do("room", "invite", "lounge", "BAD HANDLE").failContains(t, 2, `invalid handle "BAD HANDLE"`)
	a.do("room", "join", "nowhere").failContains(t, 3, "nowhere")
	a.do("room", "create").fail(t, 2, "termchat room create takes 1 argument(s), got 0")
	a.do("room", "bogus").fail(t, 2, `unknown command "bogus" for "termchat room"`)
	b.do("room", "rename", "lounge", "mine").failContains(t, 4, "forbidden")
}

func TestOpenRoomCreatedAfterConnectIsVisibleAndJoinable(t *testing.T) {
	_, a, b := pair(t)
	// The join works at once: a room the cache does not know is looked up on the server.
	a.ok("room", "create", "later")
	if got := b.conv("room", "join", "#later"); got.Name != "later" || got.Kind != "room" {
		t.Fatalf("join = %+v", got)
	}
	var joined *chatView
	for _, c := range b.chats() {
		if c.Name == "later" {
			joined = &c
		}
	}
	if joined == nil || joined.Joined != nil {
		t.Fatalf("chats lacks the joined room: %+v", joined)
	}

	// The list of rooms not joined may lag by the daemon's refresh window, so wait for it.
	a.ok("room", "create", "other")
	eventually(t, "chats --all to list the new open room", func() bool {
		for _, c := range b.chats("--all") {
			if c.Name == "other" {
				return c.Kind == "room" && c.Joined != nil && !*c.Joined
			}
		}
		return false
	})
}
