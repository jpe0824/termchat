package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/jpe0824/termchat/internal/server"
)

func TestJoinAutostartsOneDaemonAndReusesIt(t *testing.T) {
	s := startServer(t)
	p := newPerson(t)
	if n := len(p.daemons()); n != 0 {
		t.Fatalf("%d daemons before any command", n)
	}
	code, err := server.InitFirstAdmin(s.Store, "alice", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	out := p.ok("join", s.addr, "--invite", code)
	var joined struct{ Account, Handle string }
	if err := json.Unmarshal([]byte(out), &joined); err != nil || joined.Handle != "alice" || joined.Account != "alice" {
		t.Fatalf("join output = %q (%v)", out, err)
	}
	first := p.daemons()
	if len(first) != 1 {
		t.Fatalf("join left %d background processes, want 1", len(first))
	}
	p.ok("status")
	p.ok("chats")
	if again := p.daemons(); len(again) != 1 || again[0] != first[0] {
		t.Fatalf("later commands changed the background process: before %v, after %v", first, again)
	}
}

func TestStatusReportsAccountAndState(t *testing.T) {
	s := startServer(t)
	p := newPerson(t)
	s.joinAs(t, p, "alice", "Alice")

	fp := ssh.FingerprintSHA256(s.Server.HostKey())
	got := p.jsonOf("status")
	want := map[string]any{"account": "alice", "handle": "alice", "state": "connected", "pending": float64(0), "syncing": false,
		"server_key": fp}
	m := got.(map[string]any)
	if len(m) != len(want) {
		t.Fatalf("status keys = %v, want %v", m, want)
	}
	for k, v := range want {
		if m[k] != v {
			t.Fatalf("status %s = %v, want %v", k, m[k], v)
		}
	}
	want2 := "account     alice\nhandle      @alice\nstate       connected\npending     0\nsyncing     no\nserver key  " + fp + "\n"
	if table := p.tableOK("status", "--account", "alice"); table != want2 {
		t.Fatalf("status table:\n%s\nwant:\n%s", table, want2)
	}
}

func TestJoinTableOutput(t *testing.T) {
	s := startServer(t)
	a, b := newPerson(t), newPerson(t)
	s.joinAs(t, a, "alice", "Alice")
	code, _, err := server.InviteHandle(s.Store, "bob", "Bob")
	if err != nil {
		t.Fatal(err)
	}
	b.ok("daemon", "start")
	want := "joined " + s.addr + " as @bob\nserver key " + ssh.FingerprintSHA256(s.Server.HostKey()) + "\n"
	if out := b.tableOK("join", s.addr, "--invite", code); out != want {
		t.Fatalf("join table = %q", out)
	}
}

func TestStatusWithoutAccountExits4(t *testing.T) {
	skipOnWindows(t)
	p := newPerson(t)
	p.do("status").fail(t, 4, "no account configured; join a server first")
	p.do("chats").fail(t, 4, "no account configured; join a server first")
}

func TestJoinBadInviteExits4(t *testing.T) {
	s := startServer(t)
	p := newPerson(t)
	r := p.do("join", s.addr, "--invite", "not-a-real-code")
	r.failContains(t, 4, "invite")
	p.do("join", s.addr).fail(t, 2, "--invite is required")
}

func TestOfflineStatusIsAReport(t *testing.T) {
	s := startServer(t)
	p := newPerson(t)
	s.joinAs(t, p, "alice", "Alice")
	if err := s.StopServer(); err != nil {
		t.Fatal(err)
	}
	eventually(t, "status offline", func() bool {
		m, _ := p.jsonOf("status").(map[string]any)
		return m["state"] == "offline" || m["state"] == "connecting"
	})
}

func TestDaemonStartAndStop(t *testing.T) {
	skipOnWindows(t)
	p := newPerson(t)
	p.ok("daemon", "stop") // none running: quiet success
	if out := p.ok("daemon", "stop"); out != "" {
		t.Fatalf("daemon stop with none running printed %q", out)
	}
	out := p.ok("daemon", "start")
	if !strings.Contains(out, `"state":"offline"`) || len(p.daemons()) != 1 {
		t.Fatalf("daemon start: %q, %d processes", out, len(p.daemons()))
	}
	if out := p.tableOK("daemon", "start"); out != "background process running (no account)\n" {
		t.Fatalf("daemon start table = %q", out)
	}
	pid := p.daemons()[0]
	p.ok("daemon", "start")
	if d := p.daemons(); len(d) != 1 || d[0] != pid {
		t.Fatalf("second daemon start changed processes: %v", d)
	}
	if out := p.ok("daemon", "stop"); out != "" {
		t.Fatalf("daemon stop printed %q", out)
	}
	eventually(t, "the background process to exit", func() bool { return len(p.daemons()) == 0 })
}

func TestAccountFlagMustMatch(t *testing.T) {
	s := startServer(t)
	p := newPerson(t)
	s.joinAs(t, p, "alice", "Alice")
	p.do("status", "--account", "carol").fail(t, 2, `unknown account "carol"; the configured account is "alice"`)
	p.ok("status", "--account", "alice")
}

// stateOf reads the person's connection state from status.
func stateOf(p *person) string {
	var st struct{ State string }
	r := p.do("status", "--json")
	if r.code != 0 || json.Unmarshal([]byte(r.out), &st) != nil {
		return ""
	}
	return st.State
}

// A device whose key was revoked joins again with a new invite, in place of
// the account it had, without anyone editing config.toml.
func TestRevokedDeviceJoinsAgain(t *testing.T) {
	s := startServer(t)
	a, b := newPerson(t), newPerson(t)
	s.joinAs(t, a, "alice", "Alice")
	s.joinAs(t, b, "bob", "Bob")
	keys, err := s.Store.Keys("bob")
	if err != nil || len(keys) != 1 {
		t.Fatalf("Keys(bob) = %v, %v", keys, err)
	}
	if err := s.Store.RemoveKey(keys[0].Fingerprint); err != nil {
		t.Fatal(err)
	}
	s.Server.Kick(keys[0].Fingerprint)
	eventually(t, "bob not_authorised", func() bool { return stateOf(b) == "not_authorised" })

	// Joining another server is still refused.
	b.do("join", "127.0.0.1:1", "--invite", "AAAAAAAAAA").fail(t, 1, "invalid request: an account is already configured")

	code, existing, err := server.InviteHandle(s.Store, "bob", "")
	if err != nil || !existing {
		t.Fatalf("InviteHandle = %v, existing %v", err, existing)
	}
	b.ok("join", s.addr, "--invite", code)
	eventually(t, "bob connected again", func() bool { return stateOf(b) == "connected" })
	if keys, _ := s.Store.Keys("bob"); len(keys) != 1 {
		t.Fatalf("bob has %d keys, want 1", len(keys))
	}
}
