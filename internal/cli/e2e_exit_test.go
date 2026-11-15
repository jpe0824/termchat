package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jpe0824/termchat/internal/server"
)

func TestExitCodeNotFoundAndAmbiguous(t *testing.T) {
	_, a, _ := pair(t)
	a.ok("dm", "bob")
	a.do("read", "#nowhere").fail(t, 3, "no such chat: #nowhere")
	a.do("read", "@zed").fail(t, 3, "no such chat: @zed")
	a.do("send", "nowhere", "hi").fail(t, 3, "no such chat: nowhere")
	a.do("mark-read", "#nowhere").fail(t, 3, "no such chat: #nowhere")
	a.do("wait", "#nowhere", "--timeout", "1s").fail(t, 3, "no such chat: #nowhere")

	a.ok("room", "create", "bob")
	a.do("read", "bob").failContains(t, 3, `ambiguous: "bob" matches `)
	a.ok("read", "@bob")
	a.ok("read", "#bob")
}

func TestExitCodeUsage(t *testing.T) {
	_, a, _ := pair(t)
	a.do("read").fail(t, 2, "termchat read takes 1 argument(s), got 0")
	a.do("read", "a", "b").fail(t, 2, "termchat read takes 1 argument(s), got 2")
	a.do("chats", "extra").fail(t, 2, "termchat chats takes 0 argument(s), got 1")
	a.do("read", "@bob", "-n", "0").fail(t, 2, "-n must be at least 1")
	a.do("search", "x", "-n", "0").fail(t, 2, "-n must be at least 1")
	a.do("search").fail(t, 2, "termchat search needs a query")
	a.do("wait", "a", "b").fail(t, 2, "termchat wait takes at most 1 argument(s), got 2")
	a.do("status", "--bogus").fail(t, 2, "unknown flag: --bogus")
	a.do("daemon", "bogus").fail(t, 2, `unknown command "bogus" for "termchat daemon"`)
	a.do("status", "--account", "zed").fail(t, 2, `unknown account "zed"; the configured account is "alice"`)
}

func TestExitCodeNotAuthorisedAfterKeyRemoved(t *testing.T) {
	s, a, b := pair(t)
	a.ok("dm", "bob")
	b.ok("chats") // bob's account is fine while his key is valid
	if _, err := server.RemovePersonFully(s.Store, "alice", "bob", nil); err != nil {
		t.Fatal(err)
	}
	s.restart(t)
	eventually(t, "bob's key to be refused", func() bool {
		m, _ := b.jsonOf("status").(map[string]any)
		return m["state"] == "not_authorised"
	})
	// status is a report: exit 0 even though the account is unusable.
	b.ok("status")
	b.do("room", "create", "after-removal").fail(t, 4, "not connected to the server; try again once it is reachable")
	b.do("device", "add").fail(t, 4, "not connected to the server; try again once it is reachable")
}

func TestJSONWhenPipedAndTableOnTerminal(t *testing.T) {
	_, a, _ := pair(t)
	a.ok("dm", "bob")
	piped := a.ok("chats")
	var cs []chatView
	if err := json.Unmarshal([]byte(piped), &cs); err != nil || len(cs) != 1 {
		t.Fatalf("piped chats is not JSON: %q (%v)", piped, err)
	}
	if explicit := a.ok("chats", "--json"); explicit != piped {
		t.Fatalf("--json = %q, piped = %q", explicit, piped)
	}
	table := a.tableOK("chats")
	if json.Valid([]byte(table)) || !strings.HasPrefix(table, "@bob  ") {
		t.Fatalf("table = %q, want an aligned text row starting with @bob", table)
	}
}

func TestFailureWritesNothingToStdout(t *testing.T) {
	_, a, _ := pair(t)
	a.do("read", "#nowhere").fail(t, 3, "no such chat: #nowhere")
	a.do("room", "join", "nowhere").failContains(t, 3, "nowhere")
	a.do("search").fail(t, 2, "termchat search needs a query")
}
