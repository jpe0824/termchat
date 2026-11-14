package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

type chatView struct {
	ID     string  `json:"id"`
	Kind   string  `json:"kind"`
	Name   string  `json:"name"`
	Unread int     `json:"unread"`
	LastAt *string `json:"last_at"`
	Joined *bool   `json:"joined"`
}

func (p *person) chats(args ...string) []chatView {
	p.t.Helper()
	var cs []chatView
	decode(p.t, p.ok(append([]string{"chats"}, append(args, "--json")...)...), &cs)
	return cs
}

// hasText reports whether the JSON read of chat contains a message with text.
func (p *person) hasText(chat, text string) bool {
	for _, m := range p.messages("read", chat) {
		if m.Text == text {
			return true
		}
	}
	return false
}

func TestJoinSendReadEndToEnd(t *testing.T) {
	_, a, b := pair(t)
	a.ok("dm", "bob")
	a.ok("send", "@bob", "hello bob")
	eventually(t, "bob to see the message", func() bool {
		r := b.do("read", "@alice", "--json")
		return r.code == 0 && strings.Contains(r.out, "hello bob")
	})
	ms := b.messages("read", "@alice")
	if len(ms) != 1 {
		t.Fatalf("bob reads %d messages, want 1: %+v", len(ms), ms)
	}
	m := ms[0]
	if m.Text != "hello bob" || m.Author != "alice" || m.Seq < 1 || m.MsgID == "" || m.Conv == "" ||
		m.Agent || m.Edited || m.Deleted || m.Status != "" || m.Error != "" || len(m.Mentions) != 0 {
		t.Fatalf("message = %+v", m)
	}
	if at, err := time.Parse(time.RFC3339, m.At); err != nil || !strings.HasSuffix(m.At, "Z") || time.Since(at) > time.Minute {
		t.Fatalf("at = %q (%v), want a recent RFC 3339 UTC time", m.At, err)
	}
	// Alice sees her own message in order with bob's answer.
	b.ok("send", "@alice", "hello alice")
	eventually(t, "alice to see the answer", func() bool { return a.hasText("@bob", "hello alice") })
	got := a.messages("read", "@bob")
	if len(got) != 2 || got[0].Text != "hello bob" || got[1].Text != "hello alice" || got[0].Seq >= got[1].Seq {
		t.Fatalf("alice reads %+v, want her message then bob's, oldest first", got)
	}
}

func TestChatsListsUnreadMostRecentFirst(t *testing.T) {
	_, a, b := pair(t)
	if out := b.ok("chats"); out != "[]\n" {
		t.Fatalf("empty chats = %q, want []", out)
	}
	a.ok("dm", "bob")
	a.ok("send", "@bob", "one")
	a.ok("send", "@bob", "two")
	eventually(t, "bob's unread count", func() bool {
		cs := b.chats()
		return len(cs) == 1 && cs[0].Unread == 2
	})
	cs := b.chats()
	if c := cs[0]; c.Kind != "dm" || c.Name != "alice" || c.ID == "" || c.LastAt == nil || c.Joined != nil {
		t.Fatalf("chat = %+v", c)
	}
	if _, err := time.Parse(time.RFC3339, *cs[0].LastAt); err != nil || !strings.HasSuffix(*cs[0].LastAt, "Z") {
		t.Fatalf("last_at = %q, want RFC 3339 UTC", *cs[0].LastAt)
	}

	// A joined room without messages sorts after the DM; once it has the newest
	// message it sorts first.
	a.ok("room", "create", "platform")
	b.ok("room", "join", "#platform")
	cs = b.chats()
	if len(cs) != 2 || cs[0].Kind != "dm" || cs[1].Name != "platform" || cs[1].LastAt != nil {
		t.Fatalf("chats = %+v, want the dm, then the empty room with last_at null", cs)
	}
	a.ok("send", "#platform", "room news")
	eventually(t, "room message", func() bool { return b.hasText("#platform", "room news") })
	if cs = b.chats(); cs[0].Name != "platform" || cs[0].Unread != 1 || cs[1].Name != "alice" {
		t.Fatalf("chats = %+v, want the room first", cs)
	}
}

func TestChatsAllListsOpenRoomsNotJoined(t *testing.T) {
	_, a, b := pair(t)
	a.ok("room", "create", "lounge")
	eventually(t, "chats --all to list the open room", func() bool {
		for _, c := range b.chats("--all") {
			if c.Name == "lounge" {
				return true
			}
		}
		return false
	})
	if cs := b.chats(); len(cs) != 0 {
		t.Fatalf("chats without --all = %+v, want []", cs)
	}
	var lounge *chatView
	for _, c := range b.chats("--all") {
		if c.Name == "lounge" {
			lounge = &c
		}
	}
	if lounge.Kind != "room" || lounge.Joined == nil || *lounge.Joined || lounge.Unread != 0 || lounge.LastAt != nil {
		t.Fatalf("open room = %+v", lounge)
	}
	table := b.tableOK("chats", "--all")
	if !strings.Contains(table, "#lounge") || !strings.Contains(table, "not joined") {
		t.Fatalf("table = %q", table)
	}
}

func TestReadDoesNotMarkReadButMarkReadDoes(t *testing.T) {
	_, a, b := pair(t)
	a.ok("dm", "bob")
	a.ok("send", "@bob", "needle in a haystack")
	eventually(t, "unread", func() bool { cs := b.chats(); return len(cs) == 1 && cs[0].Unread == 1 })

	b.ok("read", "@alice")
	b.tableOK("read", "@alice")
	b.ok("search", "needle")
	b.ok("chats")
	if cs := b.chats(); cs[0].Unread != 1 {
		t.Fatalf("unread after read, search and chats = %d, want 1", cs[0].Unread)
	}
	if out := b.ok("mark-read", "@alice"); out != "" {
		t.Fatalf("mark-read printed %q", out)
	}
	eventually(t, "unread cleared", func() bool { return b.chats()[0].Unread == 0 })
}

func TestReadLimitAndBefore(t *testing.T) {
	_, a, b := pair(t)
	a.ok("dm", "bob")
	for i := 1; i <= 5; i++ {
		a.ok("send", "@bob", fmt.Sprintf("m%d", i))
	}
	eventually(t, "all five", func() bool { return b.hasText("@alice", "m5") })
	texts := func(ms []msgView) string {
		var s []string
		for _, m := range ms {
			s = append(s, m.Text)
		}
		return strings.Join(s, ",")
	}
	last3 := b.messages("read", "@alice", "-n", "3")
	if got := texts(last3); got != "m3,m4,m5" {
		t.Fatalf("read -n 3 = %s, want m3,m4,m5", got)
	}
	older := b.messages("read", "@alice", "-n", "3", "--before", fmt.Sprint(last3[0].Seq))
	if got := texts(older); got != "m1,m2" {
		t.Fatalf("read --before = %s, want m1,m2", got)
	}
}

func TestSearchFindsMessagesNewestFirst(t *testing.T) {
	_, a, b := pair(t)
	a.ok("dm", "bob")
	a.ok("send", "@bob", "deploy one")
	a.ok("send", "@bob", "lunch")
	a.ok("send", "@bob", "deploy two")
	eventually(t, "messages", func() bool { return len(b.messages("search", "deploy")) == 2 })
	ms := b.messages("search", "deploy")
	if ms[0].Text != "deploy two" || ms[1].Text != "deploy one" {
		t.Fatalf("search = %+v, want newest first", ms)
	}
	if got := b.messages("search", "deploy", "-n", "1"); len(got) != 1 || got[0].Text != "deploy two" {
		t.Fatalf("search -n 1 = %+v", got)
	}
	if out := b.ok("search", "nothing-matches-this"); out != "[]\n" {
		t.Fatalf("empty search = %q, want []", out)
	}
	if table := b.tableOK("search", "deploy"); !strings.Contains(table, "deploy two") || !strings.Contains(table, "@alice") {
		t.Fatalf("search table = %q", table)
	}
}

func TestChatRefNormalised(t *testing.T) {
	_, a, b := pair(t)
	a.ok("room", "create", "platform")
	a.ok("dm", "bob")
	a.ok("send", "@bob", "hi")
	a.ok("send", "#platform", "room hi")
	eventually(t, "bob has the dm", func() bool { return b.do("read", "@alice", "--json").code == 0 })
	b.ok("room", "join", "platform")
	eventually(t, "bob sees the room message", func() bool { return b.hasText("#platform", "room hi") })
	for _, ref := range []string{"@Alice", "alice", "@alice", " @ALICE "} {
		if ms := b.messages("read", ref); len(ms) != 1 || ms[0].Text != "hi" {
			t.Fatalf("read %q = %+v", ref, ms)
		}
	}
	for _, ref := range []string{"#Platform", "platform", "#platform"} {
		if ms := b.messages("read", ref); len(ms) != 1 || ms[0].Text != "room hi" {
			t.Fatalf("read %q = %+v", ref, ms)
		}
	}
	id := chatID(t, b, "alice")
	if ms := b.messages("read", id); len(ms) != 1 {
		t.Fatalf("read by id = %+v", ms)
	}
}

func TestTableOutputSanitised(t *testing.T) {
	_, a, b := pair(t)
	a.ok("dm", "bob")
	const evil = "\x1b[2Jboom\x07\x1b]0;title\x07 end"
	a.ok("send", "@bob", evil)
	eventually(t, "message", func() bool { return len(b.messages("search", "end")) == 1 })

	for _, args := range [][]string{{"read", "@alice"}, {"search", "end"}} {
		out := b.tableOK(args...)
		if strings.ContainsAny(out, "\x1b\x07") || !strings.Contains(out, "boom end") {
			t.Fatalf("table %v = %q, want inert text containing %q", args, out, "boom end")
		}
	}
	raw := b.ok("read", "@alice", "--json")
	if strings.ContainsAny(raw, "\x1b\x07") {
		t.Fatalf("JSON carries raw control bytes: %q", raw)
	}
	if ms := b.messages("read", "@alice"); ms[0].Text != evil {
		t.Fatalf("JSON text = %q, want the original %q", ms[0].Text, evil)
	}
}

func TestReadMarksEditedDeletedAndMultiline(t *testing.T) {
	_, a, b := pair(t)
	a.ok("dm", "bob")
	a.ok("send", "@bob", "keep\nsecond line")
	a.ok("send", "@bob", "will change")
	a.ok("send", "@bob", "will vanish")
	eventually(t, "messages", func() bool { return b.hasText("@alice", "will vanish") })
	ms := a.messages("read", "@bob")
	a.edit(ms[1].Conv, ms[1].MsgID, "changed")
	a.delete(ms[2].Conv, ms[2].MsgID)
	eventually(t, "edit and delete reach bob", func() bool {
		got := b.messages("read", "@alice")
		return len(got) == 3 && got[1].Edited && got[2].Deleted
	})
	out := b.tableOK("read", "@alice")
	for _, want := range []string{"keep\n    second line", "(edited)  changed", "(deleted)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("table lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "will vanish") {
		t.Fatalf("deleted text is shown:\n%s", out)
	}
	var one []msgView
	if err := json.Unmarshal([]byte(b.ok("read", "@alice", "--json")), &one); err != nil || one[1].Text != "changed" {
		t.Fatalf("json = %+v (%v)", one, err)
	}
}
