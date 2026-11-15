package cli

import (
	"strings"
	"testing"
)

type invited struct {
	Code string `json:"code"`
	Join string `json:"join"`
}

func (p *person) invited(args ...string) invited {
	p.t.Helper()
	var v invited
	decode(p.t, p.ok(append(args, "--json")...), &v)
	if v.Code == "" {
		p.t.Fatalf("%v gave no code", args)
	}
	return v
}

func (p *person) handle() string {
	p.t.Helper()
	m, _ := p.jsonOf("status").(map[string]any)
	h, _ := m["handle"].(string)
	return h
}

func TestDeviceAddJoinsAnotherDeviceToTheAccount(t *testing.T) {
	s, a, _ := pair(t)
	inv := a.invited("device", "add")
	if want := "termchat join " + s.addr + " --invite " + inv.Code; inv.Join != want {
		t.Fatalf("join = %q, want %q", inv.Join, want)
	}
	table := a.tableOK("device", "add")
	lines := strings.Split(strings.TrimSuffix(table, "\n"), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "invite code: ") || lines[1] != "on the other device, run:" ||
		lines[2] != "  termchat join "+s.addr+" --invite "+strings.TrimPrefix(lines[0], "invite code: ") {
		t.Fatalf("device add table:\n%s", table)
	}

	second := newPerson(t)
	second.ok("join", s.addr, "--invite", inv.Code)
	eventually(t, "second device", func() bool { return second.handle() == "alice" })
}

func TestAgentCreateAndAgentTag(t *testing.T) {
	s, a, _ := pair(t)
	inv := a.invited("agent", "create", "bot", "--name", "Helper Bot")
	if !strings.HasPrefix(inv.Join, "termchat join "+s.addr+" --invite ") {
		t.Fatalf("join = %q", inv.Join)
	}
	bot := newPerson(t)
	bot.ok("join", s.addr, "--invite", inv.Code)
	eventually(t, "the agent to connect", func() bool { return bot.handle() == "bot" })

	bot.ok("dm", "alice")
	bot.ok("send", "@alice", "beep boop")
	eventually(t, "alice to read the agent", func() bool { return a.hasText("@bot", "beep boop") })
	m := a.messages("read", "@bot")[0]
	if !m.Agent || m.Author != "bot" {
		t.Fatalf("message = %+v, want agent true", m)
	}
	if out := a.tableOK("read", "@bot"); !strings.Contains(out, "@bot [agent]") {
		t.Fatalf("table lacks the agent tag:\n%s", out)
	}
	a.do("agent", "create", "Not Valid").failContains(t, 2, `invalid handle "Not Valid"`)
}

func TestInviteIsForAdmins(t *testing.T) {
	s, a, b := pair(t)
	inv := a.invited("invite", "carol", "--name", "Carol")
	carol := newPerson(t)
	carol.ok("join", s.addr, "--invite", inv.Code)
	eventually(t, "carol to connect", func() bool { return carol.handle() == "carol" })
	if table := a.tableOK("invite", "dave"); !strings.HasPrefix(table, "invite code: ") || !strings.Contains(table, "  termchat join "+s.addr+" --invite ") {
		t.Fatalf("invite table:\n%s", table)
	}
	b.do("invite", "dave").fail(t, 4, "forbidden: admin only")
}
